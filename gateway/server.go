package gateway

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const gatewayVersion = "0.3.0"

//go:embed templates/*.html
var templatesFS embed.FS

// Server handles HTTP traffic for the gateway.
type Server struct {
	manager      *ContainerManager
	configMu     sync.RWMutex
	cfg          *GatewayConfig
	hostIndex    map[string]*ContainerConfig
	groupIndex   map[string]*GroupConfig
	containerMap map[string]*ContainerConfig
	trustedCIDRs []*net.IPNet
	tmpl         *template.Template
	rateLimiter  *rateLimiter
	groupRouter  *GroupRouter
	scheduler    *ScheduleManager
	schedLoc     *time.Location // resolved from gateway.schedule_timezone; never nil (defaults to time.Local)
	httpServer   *http.Server
}

func NewServer(manager *ContainerManager, scheduler *ScheduleManager, cfg *GatewayConfig) (*Server, error) {
	tmpl, err := template.ParseFS(templatesFS, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("failed to parse templates: %w", err)
	}

	loc, _ := resolveLocation(cfg.Gateway.ScheduleTimezone) // already validated; error impossible

	return &Server{
		manager:      manager,
		scheduler:    scheduler,
		schedLoc:     loc,
		cfg:          cfg,
		hostIndex:    BuildHostIndex(cfg),
		groupIndex:   BuildGroupHostIndex(cfg),
		containerMap: BuildContainerMap(cfg),
		trustedCIDRs: parseTrustedProxies(cfg.Gateway.TrustedProxies),
		tmpl:         tmpl,
		rateLimiter:  newRateLimiter(1 * time.Second),
		groupRouter:  NewGroupRouter(),
	}, nil
}

// Start listens for HTTP traffic and blocks until ctx is cancelled.
// On cancellation it performs a graceful shutdown with a 15-second deadline.
func (s *Server) Start(ctx context.Context) error {
	mux := http.NewServeMux()

	// ── Functional endpoints (NOT protected by auth) ──
	mux.HandleFunc("/_health", s.handleHealth)
	mux.HandleFunc("/_logs", s.handleLogs)

	// ── Admin endpoints (protected by optional auth middleware) ──
	authCfg := &s.GetConfig().Gateway.AdminAuth
	mux.Handle("/_status", adminAuthMiddleware(
		http.HandlerFunc(s.handleStatusPage), authCfg))
	mux.Handle("/_status/api", adminAuthMiddleware(
		http.HandlerFunc(s.handleStatusAPI), authCfg))
	mux.Handle("/_status/wake", adminAuthMiddleware(
		http.HandlerFunc(s.handleStatusWake), authCfg))
	mux.Handle("/_metrics", adminAuthMiddleware(
		promhttp.Handler(), authCfg))

	// ── Catch-all ──
	mux.HandleFunc("/", s.handleRequest)

	s.httpServer = &http.Server{
		Addr:         ":" + s.GetConfig().Gateway.Port,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// Start rate limiter cleanup goroutine
	s.rateLimiter.startCleanup(ctx, 5*time.Minute)

	// Run ListenAndServe in a goroutine so we can wait for ctx cancellation.
	errCh := make(chan error, 1)
	go func() {
		slog.Info("gateway started", "version", gatewayVersion, "port", s.GetConfig().Gateway.Port)
		if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
		close(errCh)
	}()

	// Block until the root context is cancelled or ListenAndServe fails.
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	// Graceful shutdown with a 15-second deadline.
	const shutdownGrace = 15 * time.Second
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer shutdownCancel()

	slog.Info("shutting down gateway", "grace_period", shutdownGrace)
	return s.httpServer.Shutdown(shutdownCtx)
}

// ─── Config Hot-Reload ────────────────────────────────────────────────────────

// ReloadConfig safely swaps the active configuration.
func (s *Server) ReloadConfig(newCfg *GatewayConfig) {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	s.cfg = newCfg
	loc, _ := resolveLocation(newCfg.Gateway.ScheduleTimezone)
	s.schedLoc = loc
	s.hostIndex = BuildHostIndex(newCfg)
	s.groupIndex = BuildGroupHostIndex(newCfg)
	s.containerMap = BuildContainerMap(newCfg)
	s.trustedCIDRs = parseTrustedProxies(newCfg.Gateway.TrustedProxies)
	s.scheduler.Sync(newCfg.Containers, s.schedLoc)
}

// GetConfig safely retrieves the current configuration.
func (s *Server) GetConfig() *GatewayConfig {
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	return s.cfg
}

// ─── Request routing ──────────────────────────────────────────────────────────

// resolveConfig maps an incoming request to its ContainerConfig by Host header.
// Returns nil if no container matches (groups are checked separately via resolveGroup).
func (s *Server) resolveConfig(r *http.Request) *ContainerConfig {
	s.configMu.RLock()
	defer s.configMu.RUnlock()

	host := r.Host
	if cfg, ok := s.hostIndex[host]; ok {
		return cfg
	}
	// Strip port and retry
	if idx := strings.LastIndex(host, ":"); idx != -1 {
		if cfg, ok := s.hostIndex[host[:idx]]; ok {
			return cfg
		}
	}
	// Query-param fallback for testing: ?container=my-app
	if name := r.URL.Query().Get("container"); name != "" {
		for i := range s.cfg.Containers {
			if s.cfg.Containers[i].Name == name {
				return &s.cfg.Containers[i]
			}
		}
	}
	return nil
}

// resolveGroup maps an incoming request to its GroupConfig by Host header.
func (s *Server) resolveGroup(r *http.Request) *GroupConfig {
	s.configMu.RLock()
	defer s.configMu.RUnlock()

	host := r.Host
	if g, ok := s.groupIndex[host]; ok {
		return g
	}
	if idx := strings.LastIndex(host, ":"); idx != -1 {
		if g, ok := s.groupIndex[host[:idx]]; ok {
			return g
		}
	}
	return nil
}

// metricsResponseWriter wraps http.ResponseWriter to capture the HTTP status code.
type metricsResponseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (m *metricsResponseWriter) WriteHeader(statusCode int) {
	m.statusCode = statusCode
	m.ResponseWriter.WriteHeader(statusCode)
}

// ─── Main handler ─────────────────────────────────────────────────────────────

func (s *Server) handleRequest(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/_health" || r.URL.Path == "/_logs" || strings.HasPrefix(r.URL.Path, "/_status") || r.URL.Path == "/_metrics" {
		http.NotFound(w, r)
		return
	}

	// Try group routing first, then individual container.
	if group := s.resolveGroup(r); group != nil {
		s.handleGroupRequest(w, r, group)
		return
	}

	// Read cfg and schedLoc atomically under a single lock to
	// prevent a concurrent hot-reload from swapping the config between reads.
	s.configMu.RLock()
	var cfg *ContainerConfig
	host := r.Host
	if c, ok := s.hostIndex[host]; ok {
		cfg = c
	} else if idx := strings.LastIndex(host, ":"); idx != -1 {
		if c, ok := s.hostIndex[host[:idx]]; ok {
			cfg = c
		}
	}
	if cfg == nil {
		if name := r.URL.Query().Get("container"); name != "" {
			for i := range s.cfg.Containers {
				if s.cfg.Containers[i].Name == name {
					cfg = &s.cfg.Containers[i]
					break
				}
			}
		}
	}
	schedLoc := s.schedLoc
	s.configMu.RUnlock()

	if cfg == nil {
		http.NotFound(w, r)
		return
	}

	// Determine effective timezone: per-container overrides global.
	effectiveLoc := schedLoc
	if cfg.ScheduleTimezone != "" {
		if l, err := resolveLocation(cfg.ScheduleTimezone); err == nil {
			effectiveLoc = l
		}
	}

	// Schedule gate: block access outside the configured cron window.
	if allowed, nextStart := IsInScheduleWindow(cfg, time.Now(), effectiveLoc); !allowed {
		s.serveScheduledPage(w, r, cfg, nextStart, effectiveLoc)
		return
	}

	start := time.Now()
	mw := &metricsResponseWriter{ResponseWriter: w, statusCode: http.StatusOK}

	// Defer recording the HTTP request metric
	defer func() {
		duration := time.Since(start).Seconds()
		RecordRequest(cfg.Name, strconv.Itoa(mw.statusCode), duration)
	}()

	ctx := r.Context()
	status, err := s.manager.client.GetContainerStatus(ctx, cfg.Name)
	if err != nil {
		if strings.Contains(err.Error(), "No such container") {
			s.serveErrorPage(mw, r, cfg, "Container not found in Docker daemon")
		} else {
			s.serveErrorPage(mw, r, cfg, fmt.Sprintf("Docker error: %v", err))
		}
		return
	}

	if status == "running" {
		// If there are dependencies, ensure they are running too.
		if len(cfg.DependsOn) > 0 {
			allContainers := s.GetConfig().Containers
			for _, depName := range cfg.DependsOn {
				depStatus, _ := s.manager.client.GetContainerStatus(ctx, depName)
				if depStatus != "running" {
					// Dependency not running — trigger async start of deps + container
					s.manager.InitStartState(cfg.Name)
					go func() {
						bgCtx, cancel := context.WithTimeout(context.Background(), cfg.StartTimeout+10*time.Second)
						defer cancel()
						if err := s.manager.EnsureDepsRunning(bgCtx, cfg.Name, allContainers); err != nil {
							slog.Error("dependency start error", "container", cfg.Name, "error", err)
						}
					}()
					s.serveLoadingPage(mw, r, cfg)
					return
				}
			}
		}
		s.manager.RecordActivity(cfg.Name)
		s.proxyRequest(mw, r, cfg)
		return
	}

	// Container not running — pre-set state and trigger async start (with deps)
	s.manager.InitStartState(cfg.Name)
	go func() {
		bgCtx, cancel := context.WithTimeout(context.Background(), cfg.StartTimeout+10*time.Second)
		defer cancel()
		allContainers := s.GetConfig().Containers
		if len(cfg.DependsOn) > 0 {
			if err := s.manager.EnsureDepsRunning(bgCtx, cfg.Name, allContainers); err != nil {
				slog.Error("dependency start error", "container", cfg.Name, "error", err)
				return
			}
		}
		if err := s.manager.EnsureRunning(bgCtx, cfg); err != nil {
			slog.Error("async start error", "container", cfg.Name, "error", err)
		}
	}()

	s.serveLoadingPage(mw, r, cfg)
}

// handleGroupRequest handles requests routed to a container group.
// It picks a member via round-robin and proxies (or serves loading page).
func (s *Server) handleGroupRequest(w http.ResponseWriter, r *http.Request, group *GroupConfig) {
	// Pick the target member for this request via round-robin.
	pickedName := s.groupRouter.Pick(group)

	s.configMu.RLock()
	pickedCfg, ok := s.containerMap[pickedName]
	s.configMu.RUnlock()

	if !ok {
		http.Error(w, fmt.Sprintf("group %q member %q not found", group.Name, pickedName), http.StatusInternalServerError)
		return
	}

	start := time.Now()
	mw := &metricsResponseWriter{ResponseWriter: w, statusCode: http.StatusOK}
	defer func() {
		duration := time.Since(start).Seconds()
		RecordRequest(pickedCfg.Name, strconv.Itoa(mw.statusCode), duration)
	}()

	ctx := r.Context()
	status, err := s.manager.client.GetContainerStatus(ctx, pickedCfg.Name)
	if err != nil || status != "running" {
		// Not all members running — trigger async group startup.
		for _, mn := range group.Containers {
			s.manager.InitStartState(mn)
		}
		go func() {
			allContainers := s.GetConfig().Containers
			// Use the max start_timeout among group members.
			var maxTimeout time.Duration
			for _, mn := range group.Containers {
				if mc, exists := s.containerMap[mn]; exists && mc.StartTimeout > maxTimeout {
					maxTimeout = mc.StartTimeout
				}
			}
			if maxTimeout == 0 {
				maxTimeout = 60 * time.Second
			}
			bgCtx, cancel := context.WithTimeout(context.Background(), maxTimeout+10*time.Second)
			defer cancel()
			if err := s.manager.EnsureGroupRunning(bgCtx, group, allContainers); err != nil {
				slog.Error("group start error", "group", group.Name, "error", err)
			}
		}()
		s.serveLoadingPage(mw, r, pickedCfg)
		return
	}

	s.manager.RecordActivity(pickedCfg.Name)
	s.proxyRequest(mw, r, pickedCfg)
}

// ─── Internal endpoints ───────────────────────────────────────────────────────

// handleHealth returns {"status":"starting"|"running"|"failed","error":"..."}.
// The loading page JS polls this to know when to redirect or show inline error.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if !s.rateLimiter.Allow(s.clientIP(r)) {
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}

	cfg := s.resolveConfig(r)
	if cfg == nil {
		http.Error(w, "unknown container", http.StatusBadRequest)
		return
	}

	status, errMsg := s.manager.GetStartState(cfg.Name)

	// If no start attempt recorded yet, fall back to Docker status
	if status == "unknown" {
		dockerStatus, err := s.manager.client.GetContainerStatus(r.Context(), cfg.Name)
		if err == nil && dockerStatus == "running" {
			status = "running"
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status": status,
		"error":  errMsg,
	})
}

// handleLogs returns {"lines":["..."]} with the last N log lines.
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	if !s.rateLimiter.Allow(s.clientIP(r)) {
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}

	cfg := s.resolveConfig(r)
	if cfg == nil {
		http.Error(w, "unknown container", http.StatusBadRequest)
		return
	}

	lines, err := s.manager.client.GetContainerLogs(r.Context(), cfg.Name, s.cfg.Gateway.LogLines)
	if err != nil {
		lines = []string{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string][]string{"lines": lines})
}

// ─── Proxy ────────────────────────────────────────────────────────────────────

// isWebSocketRequest returns true if the request is a WebSocket upgrade.
func isWebSocketRequest(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket") &&
		strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade")
}

// proxyRequest forwards an HTTP (or WebSocket) request to the target container.
func (s *Server) proxyRequest(w http.ResponseWriter, r *http.Request, cfg *ContainerConfig) {
	ip, err := s.manager.client.GetContainerAddress(r.Context(), cfg.Name, cfg.Network)
	if err != nil {
		s.serveErrorPage(w, r, cfg, fmt.Sprintf("Networking error: %v", err))
		return
	}

	addr := fmt.Sprintf("%s:%s", ip, cfg.TargetPort)

	if isWebSocketRequest(r) {
		s.proxyWebSocket(w, r, addr)
		return
	}

	targetURL, _ := url.Parse("http://" + addr)
	proxy := httputil.NewSingleHostReverseProxy(targetURL)

	// Pass client IP information to the backend
	setForwardedHeaders(r, ip)

	r.URL.Host = targetURL.Host
	r.URL.Scheme = targetURL.Scheme
	r.Host = targetURL.Host

	proxy.ServeHTTP(w, r)
}

// proxyWebSocket tunnels a WebSocket upgrade through a raw TCP connection.
// It hijacks the client conn and opens a new TCP connection to the backend,
// then copies bidirectionally.
func (s *Server) proxyWebSocket(w http.ResponseWriter, r *http.Request, backendAddr string) {
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "WebSocket proxying not supported by this server", http.StatusInternalServerError)
		return
	}

	backend, err := net.DialTimeout("tcp", backendAddr, 10*time.Second)
	if err != nil {
		http.Error(w, fmt.Sprintf("WebSocket backend unreachable: %v", err), http.StatusBadGateway)
		return
	}
	defer backend.Close()

	clientConn, _, err := hijacker.Hijack()
	if err != nil {
		return
	}
	defer clientConn.Close()

	// Forward the original upgrade request to the backend
	if err := r.Write(backend); err != nil {
		return
	}

	// Bidirectional copy until one side closes
	done := make(chan struct{}, 2)
	copy := func(dst io.Writer, src io.Reader) {
		io.Copy(dst, src) //nolint:errcheck
		done <- struct{}{}
	}
	go copy(backend, clientConn)
	go copy(clientConn, backend)
	<-done
}

// setForwardedHeaders adds X-Forwarded-For, X-Real-IP and X-Forwarded-Proto
// to the outgoing request so the backend can see the original client IP.
func setForwardedHeaders(r *http.Request, serverIP string) {
	clientIP, _, _ := net.SplitHostPort(r.RemoteAddr)

	// X-Forwarded-For: append our client IP to any existing chain
	if prior := r.Header.Get("X-Forwarded-For"); prior != "" {
		r.Header.Set("X-Forwarded-For", prior+", "+clientIP)
	} else {
		r.Header.Set("X-Forwarded-For", clientIP)
	}

	// X-Real-IP: the original client (not set if already present upstream)
	if r.Header.Get("X-Real-IP") == "" {
		r.Header.Set("X-Real-IP", clientIP)
	}

	// X-Forwarded-Proto: respect existing upstream value (not overwritten if already set)
	if r.Header.Get("X-Forwarded-Proto") == "" {
		r.Header.Set("X-Forwarded-Proto", "http")
	}
	r.Header.Set("X-Forwarded-Host", r.Host)
}

// clientIP returns the real client IP for rate-limiting purposes.
// It trusts X-Forwarded-For ONLY if RemoteAddr is from a configured trusted proxy.
func (s *Server) clientIP(r *http.Request) string {
	directIP, _, _ := net.SplitHostPort(r.RemoteAddr)

	s.configMu.RLock()
	trusted := s.trustedCIDRs
	s.configMu.RUnlock()

	if len(trusted) > 0 && isTrustedProxy(directIP, trusted) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.SplitN(xff, ",", 2)
			return strings.TrimSpace(parts[0])
		}
	}
	return directIP
}

// isTrustedProxy checks if the given IP falls within any of the trusted CIDR blocks.
func isTrustedProxy(ip string, cidrs []*net.IPNet) bool {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	for _, cidr := range cidrs {
		if cidr.Contains(parsed) {
			return true
		}
	}
	return false
}

// parseTrustedProxies converts string CIDR notation into parsed IPNet structs.
func parseTrustedProxies(proxies []string) []*net.IPNet {
	var cidrs []*net.IPNet
	for _, p := range proxies {
		_, cidr, err := net.ParseCIDR(p)
		if err != nil {
			slog.Warn("invalid trusted_proxies CIDR", "cidr", p, "error", err)
			continue
		}
		cidrs = append(cidrs, cidr)
	}
	return cidrs
}

// validateOrigin blocks cross-origin POST requests from browsers.
// Requests without an Origin header (curl, scripts) are allowed through.
func validateOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true // non-browser client
	}
	parsed, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return parsed.Host == r.Host
}

// ─── Rate limiter ─────────────────────────────────────────────────────────────

// rateLimiter enforces a minimum interval between requests per IP.
type rateLimiter struct {
	mu          sync.Mutex
	lastSeen    map[string]time.Time
	minInterval time.Duration
}

func newRateLimiter(minInterval time.Duration) *rateLimiter {
	return &rateLimiter{
		lastSeen:    make(map[string]time.Time),
		minInterval: minInterval,
	}
}

// Allow returns true if this IP is allowed to proceed (not rate-limited).
func (rl *rateLimiter) Allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	last, ok := rl.lastSeen[ip]
	if !ok || time.Since(last) >= rl.minInterval {
		rl.lastSeen[ip] = time.Now()
		return true
	}
	return false
}

// startCleanup periodically evicts stale entries from the rate limiter.
func (rl *rateLimiter) startCleanup(ctx context.Context, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				rl.evictStale()
			}
		}
	}()
}

// evictStale removes IPs whose last access is older than 2× the rate limit interval.
func (rl *rateLimiter) evictStale() {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	cutoff := time.Now().Add(-2 * rl.minInterval)
	for ip, last := range rl.lastSeen {
		if last.Before(cutoff) {
			delete(rl.lastSeen, ip)
		}
	}
}

// calcIdleRemaining returns seconds until idle-triggered stop.
//   - Returns 0 if idleTimeout is disabled (zero).
//   - Returns -1 if the container has never served a request (hasSeen=false).
//   - Returns remaining seconds clamped to [0, ∞) otherwise.
func calcIdleRemaining(idleTimeout time.Duration, lastSeen time.Time, hasSeen bool, now time.Time) int64 {
	if idleTimeout == 0 {
		return 0
	}
	if !hasSeen {
		return -1
	}
	remaining := int64(idleTimeout.Seconds()) - int64(now.Sub(lastSeen).Seconds())
	if remaining < 0 {
		remaining = 0
	}
	return remaining
}

// ─── Template data structs ────────────────────────────────────────────────────

type loadingData struct {
	ContainerName string
	RequestID     string
	RequestPath   string
	RedirectPath  string
	StartTimeout  string
}

type errorData struct {
	ContainerName string
	Error         string
	RequestID     string
	RequestPath   string
}

type scheduledData struct {
	ContainerName string
	NextStart     string // e.g. "Tue 14 Apr · 08:00" or empty
}

type statusPageData struct {
	Version string
}

type statusContainerJSON struct {
	Name         string  `json:"name"`
	Host         string  `json:"host"`
	Status       string  `json:"status"`
	StartState   string  `json:"start_state"`
	Image        string  `json:"image"`
	Icon         string  `json:"icon"`
	TargetPort   string  `json:"target_port"`
	StartTimeout string  `json:"start_timeout"`
	IdleTimeout  string  `json:"idle_timeout"`
	StartedAt        *string `json:"started_at,omitempty"`
	LastRequest      *string `json:"last_request,omitempty"`
	IdleTimeoutSec   int64   `json:"idle_timeout_sec"`
	IdleRemainingSec int64   `json:"idle_remaining_sec"`
	Network          string  `json:"network"`
	// Schedule
	ScheduleStart      string `json:"schedule_start"`
	ScheduleStop       string `json:"schedule_stop"`
	ScheduleTimezone   string `json:"schedule_timezone"`
	ScheduledDowntime  bool   `json:"scheduled_downtime"`
	NextScheduledStart string `json:"next_scheduled_start"`
}

type statusAPIResponse struct {
	Containers []statusContainerJSON `json:"containers"`
	UpdatedAt  string                `json:"updated_at"`
}

func requestID(prefix string) string {
	return fmt.Sprintf("%s-%x", prefix, time.Now().UnixNano()%0xFFFFFF)
}

func (s *Server) serveLoadingPage(w http.ResponseWriter, r *http.Request, cfg *ContainerConfig) {
	data := loadingData{
		ContainerName: cfg.Name,
		RequestID:     requestID("req"),
		RequestPath:   r.URL.Path,
		RedirectPath:  cfg.RedirectPath,
		StartTimeout:  cfg.StartTimeout.String(),
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "loading.html", data); err != nil {
		slog.Error("template render failed", "template", "loading", "error", err)
	}
}

func (s *Server) serveScheduledPage(w http.ResponseWriter, r *http.Request, cfg *ContainerConfig, nextStart time.Time, loc *time.Location) {
	next := ""
	if !nextStart.IsZero() {
		next = nextStart.In(loc).Format("Mon 02 Jan · 15:04")
	}
	data := scheduledData{
		ContainerName: cfg.Name,
		NextStart:     next,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusServiceUnavailable)
	if err := s.tmpl.ExecuteTemplate(w, "scheduled.html", data); err != nil {
		slog.Error("template render failed", "template", "scheduled", "error", err)
	}
}

func (s *Server) serveErrorPage(w http.ResponseWriter, r *http.Request, cfg *ContainerConfig, errMsg string) {
	data := errorData{
		ContainerName: cfg.Name,
		Error:         errMsg,
		RequestID:     requestID("err"),
		RequestPath:   r.URL.Path,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusBadGateway)
	if err := s.tmpl.ExecuteTemplate(w, "error.html", data); err != nil {
		slog.Error("template render failed", "template", "error", "error", err)
	}
}

// ─── Status dashboard handlers ────────────────────────────────────────────────

// handleStatusPage serves the status dashboard HTML page.
func (s *Server) handleStatusPage(w http.ResponseWriter, r *http.Request) {
	data := statusPageData{
		Version: gatewayVersion,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "status.html", data); err != nil {
		slog.Error("template render failed", "template", "status", "error", err)
		http.Error(w, "Failed to render status page", http.StatusInternalServerError)
	}
}

// handleStatusAPI returns a JSON snapshot of all managed containers.
// Polled every ~5s by the status dashboard JS.
func (s *Server) handleStatusAPI(w http.ResponseWriter, r *http.Request) {
	if !s.rateLimiter.Allow(s.clientIP(r)) {
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}

	ctx := r.Context()
	cfg := s.GetConfig()
	result := statusAPIResponse{
		UpdatedAt:  time.Now().UTC().Format(time.RFC3339),
		Containers: make([]statusContainerJSON, 0, len(cfg.Containers)),
	}

	for i := range cfg.Containers {
		c := &cfg.Containers[i]
		entry := statusContainerJSON{
			Name:         c.Name,
			Host:         c.Host,
			Icon:         c.Icon,
			TargetPort:   c.TargetPort,
			StartTimeout: c.StartTimeout.String(),
			IdleTimeout:  c.IdleTimeout.String(),
			Network:      c.Network,
		}

		// Gateway-level start state
		startState, _ := s.manager.GetStartState(c.Name)
		entry.StartState = startState

		// Docker inspect for live status + image + timestamps
		info, err := s.manager.client.InspectContainer(ctx, c.Name)
		if err != nil {
			entry.Status = "unknown"
			entry.Image = "?"
		} else {
			entry.Status = info.Status
			entry.Image = info.Image
			if !info.StartedAt.IsZero() {
				ts := info.StartedAt.UTC().Format(time.RFC3339)
				entry.StartedAt = &ts
			}
		}

		// Last request from in-memory activity tracker
		if t, ok := s.manager.GetLastSeen(c.Name); ok {
			ts := t.UTC().Format(time.RFC3339)
			entry.LastRequest = &ts
		}

		// Idle timeout countdown fields.
		now := time.Now()
		lastSeen, hasSeen := s.manager.GetLastSeen(c.Name)
		entry.IdleTimeoutSec = int64(c.IdleTimeout.Seconds())
		entry.IdleRemainingSec = calcIdleRemaining(c.IdleTimeout, lastSeen, hasSeen, now)

		// Schedule fields.
		entry.ScheduleStart = c.ScheduleStart
		entry.ScheduleStop = c.ScheduleStop
		entry.ScheduleTimezone = c.ScheduleTimezone
		if c.ScheduleStart != "" && c.ScheduleStop != "" {
			s.configMu.RLock()
			effectiveLoc := s.schedLoc
			s.configMu.RUnlock()
			if c.ScheduleTimezone != "" {
				if perLoc, err := resolveLocation(c.ScheduleTimezone); err == nil {
					effectiveLoc = perLoc
				}
			}
			allowed, nextStart := IsInScheduleWindow(c, now, effectiveLoc)
			entry.ScheduledDowntime = !allowed
			if !nextStart.IsZero() {
				entry.NextScheduledStart = nextStart.In(effectiveLoc).Format("Mon 02 Jan · 15:04")
			}
		}

		result.Containers = append(result.Containers, entry)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// handleStatusWake triggers a container start from the dashboard.
func (s *Server) handleStatusWake(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !validateOrigin(r) {
		http.Error(w, "cross-origin request blocked", http.StatusForbidden)
		return
	}
	if !s.rateLimiter.Allow(s.clientIP(r)) {
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}

	name := r.URL.Query().Get("container")
	if name == "" {
		http.Error(w, "missing container parameter", http.StatusBadRequest)
		return
	}

	cfg := s.GetConfig()
	var targetCfg *ContainerConfig
	for i := range cfg.Containers {
		if cfg.Containers[i].Name == name {
			targetCfg = &cfg.Containers[i]
			break
		}
	}
	if targetCfg == nil {
		http.Error(w, "unknown container", http.StatusBadRequest)
		return
	}

	// Trigger async start
	s.manager.InitStartState(targetCfg.Name)
	go func() {
		bgCtx, cancel := context.WithTimeout(context.Background(), targetCfg.StartTimeout+10*time.Second)
		defer cancel()
		if err := s.manager.EnsureRunning(bgCtx, targetCfg); err != nil {
			slog.Error("status-wake start error", "container", targetCfg.Name, "error", err)
		}
	}()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}
