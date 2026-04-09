package gateway

import (
	"fmt"
	"log/slog"
	"os"
	"reflect"
	"time"

	"gopkg.in/yaml.v3"
)

// Equal reports whether two GatewayConfig values are semantically identical.
// Used by DiscoveryManager to skip no-op config reloads.
func (c *GatewayConfig) Equal(other *GatewayConfig) bool {
	if c == nil || other == nil {
		return c == other
	}
	return reflect.DeepEqual(c, other)
}

// GatewayConfig is the top-level config structure parsed from config.yaml
type GatewayConfig struct {
	Gateway    GlobalConfig      `yaml:"gateway"`
	Containers []ContainerConfig `yaml:"containers"`
	Groups     []GroupConfig     `yaml:"groups"`
}

// GroupConfig defines a load-balanced group of containers behind a single host.
type GroupConfig struct {
	// Name is the logical group name (e.g. "api-cluster")
	Name string `yaml:"name"`
	// Host is the incoming Host header that routes to this group
	Host string `yaml:"host"`
	// Strategy is the load-balancing algorithm. (default: "round-robin")
	Strategy string `yaml:"strategy"`
	// Containers is the ordered list of container names in this group
	Containers []string `yaml:"containers"`
}

// AdminAuthConfig holds optional authentication settings for admin endpoints
// (/_status/*, /_metrics). When Method is "none" (the default), no authentication
// is enforced and the gateway behaves exactly as before this feature.
type AdminAuthConfig struct {
	// Method is the authentication scheme: "none", "basic", or "bearer".
	// Default: "none" (no authentication). Overridable via ADMIN_AUTH_METHOD env var.
	Method string `yaml:"method"`
	// Username is required when Method is "basic". Overridable via ADMIN_AUTH_USERNAME.
	Username string `yaml:"username"`
	// Password is required when Method is "basic". Overridable via ADMIN_AUTH_PASSWORD.
	Password string `yaml:"password"`
	// Token is required when Method is "bearer". Overridable via ADMIN_AUTH_TOKEN.
	Token string `yaml:"token"`
}

// GlobalConfig holds gateway-wide settings
type GlobalConfig struct {
	// Port the gateway listens on (default: "8080")
	Port string `yaml:"port"`
	// LogLines is the number of container log lines shown in the loading page (default: 30)
	LogLines int `yaml:"log_lines"`
	// TrustedProxies is a list of CIDR blocks (e.g. "10.0.0.0/8") whose
	// X-Forwarded-For header is trusted for rate-limiting purposes.
	// If empty, the gateway always uses RemoteAddr. (default: [])
	TrustedProxies []string `yaml:"trusted_proxies"`
	// DiscoveryInterval controls how often Docker labels are polled for
	// auto-discovery. Overridable via DISCOVERY_INTERVAL env var. (default: 15s)
	DiscoveryInterval time.Duration `yaml:"discovery_interval"`
	// AdminAuth configures optional authentication for admin endpoints.
	// See AdminAuthConfig for details. (default: method "none")
	AdminAuth AdminAuthConfig `yaml:"admin_auth"`
	// ScheduleTimezone is the IANA timezone name used to interpret schedule_start
	// and schedule_stop cron expressions (e.g. "Europe/Rome", "America/New_York").
	// Default: "" uses the process's local timezone (time.Local).
	// Overridable via SCHEDULE_TIMEZONE env var.
	ScheduleTimezone string `yaml:"schedule_timezone"`
}

// ContainerConfig holds per-container settings
type ContainerConfig struct {
	// Name is the Docker container name to manage
	Name string `yaml:"name"`
	// Host is the incoming Host header to match (e.g. "myapp.localhost")
	Host string `yaml:"host"`
	// TargetPort is the port on the container to proxy to (default: "80")
	TargetPort string `yaml:"target_port"`
	// StartTimeout is the maximum time to wait for the container to start.
	// After this duration the error page is shown. (default: 60s)
	StartTimeout time.Duration `yaml:"start_timeout"`
	// IdleTimeout is how long the container may be idle (no incoming requests)
	// before it is automatically stopped. 0 means never auto-stop. (default: 0)
	IdleTimeout time.Duration `yaml:"idle_timeout"`
	// Network is an optional Docker network name. When set, GetContainerAddress
	// will look up the container IP on this specific network. If empty, the
	// first available network is used. (default: "")
	Network string `yaml:"network"`
	// RedirectPath is the URL path the browser is sent to once the container is
	// running. Useful when the web UI is not at "/". (default: "/")
	RedirectPath string `yaml:"redirect_path"`
	// Icon is an optional Simple Icons slug (e.g. "nginx", "redis", "postgresql").
	// Displayed on the /_status dashboard card. See https://simpleicons.org
	// for available slugs. (default: "docker")
	Icon string `yaml:"icon"`
	// HealthPath is an optional HTTP endpoint (e.g. "/health") called instead
	// of a raw TCP dial to confirm container readiness. When empty the gateway
	// falls back to a TCP probe. (default: "")
	HealthPath string `yaml:"health_path"`
	// DependsOn lists container names that must be running before this one starts.
	// Dependencies are started in topological order and must pass their readiness
	// probe before the next one begins. (default: [])
	DependsOn []string `yaml:"depends_on"`
	// ScheduleStart is an optional standard 5-field cron expression (e.g. "0 8 * * 1-5")
	// that triggers a proactive container start. When combined with ScheduleStop,
	// requests outside the active window are blocked with a 503 page.
	ScheduleStart string `yaml:"schedule_start"`
	// ScheduleStop is an optional standard 5-field cron expression (e.g. "0 20 * * 1-5")
	// that triggers a proactive container stop.
	ScheduleStop string `yaml:"schedule_stop"`
}

// LoadConfig reads and parses the YAML config file.
// The path is taken from the CONFIG_PATH env var (default: /etc/gateway/config.yaml).
func LoadConfig() (*GatewayConfig, error) {
	path := os.Getenv("CONFIG_PATH")
	if path == "" {
		path = "/etc/gateway/config.yaml"
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read config file %q: %w", path, err)
	}

	var cfg GatewayConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("cannot parse config file %q: %w", path, err)
	}

	applyDefaults(&cfg)

	// Allow DISCOVERY_INTERVAL env var to override the YAML / default value.
	if envInterval := os.Getenv("DISCOVERY_INTERVAL"); envInterval != "" {
		if d, err := time.ParseDuration(envInterval); err == nil {
			cfg.Gateway.DiscoveryInterval = d
		} else {
			slog.Warn("invalid DISCOVERY_INTERVAL env var, using default", "value", envInterval, "error", err)
		}
	}

	// Allow ADMIN_AUTH_* env vars to override YAML values.
	if envMethod := os.Getenv("ADMIN_AUTH_METHOD"); envMethod != "" {
		cfg.Gateway.AdminAuth.Method = envMethod
	}
	if envUser := os.Getenv("ADMIN_AUTH_USERNAME"); envUser != "" {
		cfg.Gateway.AdminAuth.Username = envUser
	}
	if envPass := os.Getenv("ADMIN_AUTH_PASSWORD"); envPass != "" {
		cfg.Gateway.AdminAuth.Password = envPass
	}
	if envToken := os.Getenv("ADMIN_AUTH_TOKEN"); envToken != "" {
		cfg.Gateway.AdminAuth.Token = envToken
	}

	if envTZ := os.Getenv("SCHEDULE_TIMEZONE"); envTZ != "" {
		cfg.Gateway.ScheduleTimezone = envTZ
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	return &cfg, nil
}

// resolveLocation parses an IANA timezone name and returns the corresponding
// *time.Location. Returns time.Local when name is empty.
func resolveLocation(name string) (*time.Location, error) {
	if name == "" {
		return time.Local, nil
	}
	return time.LoadLocation(name)
}

// Validate checks if the loaded configuration is valid.
func (c *GatewayConfig) Validate() error {
	if c.Gateway.Port == "" {
		return fmt.Errorf("gateway.port cannot be empty")
	}

	if _, err := resolveLocation(c.Gateway.ScheduleTimezone); err != nil {
		return fmt.Errorf("schedule_timezone: invalid IANA timezone %q: %w", c.Gateway.ScheduleTimezone, err)
	}

	// Validate admin_auth settings.
	switch c.Gateway.AdminAuth.Method {
	case "", "none":
		// ok — no authentication
	case "basic":
		if c.Gateway.AdminAuth.Username == "" || c.Gateway.AdminAuth.Password == "" {
			return fmt.Errorf("admin_auth: method=basic requires non-empty username and password")
		}
	case "bearer":
		if c.Gateway.AdminAuth.Token == "" {
			return fmt.Errorf("admin_auth: method=bearer requires non-empty token")
		}
	default:
		return fmt.Errorf("admin_auth: unknown method %q (allowed: none, basic, bearer)",
			c.Gateway.AdminAuth.Method)
	}

	seenNames := make(map[string]bool)
	seenHosts := make(map[string]bool)

	// Build a set of all container names for reference checking.
	nameSet := make(map[string]bool, len(c.Containers))
	for _, ctr := range c.Containers {
		nameSet[ctr.Name] = true
	}

	// Build a set of containers that are group members (they don't need host).
	groupMembers := make(map[string]bool)
	for _, g := range c.Groups {
		for _, cn := range g.Containers {
			groupMembers[cn] = true
		}
	}

	// Build a set of containers that are dependencies (they don't need host).
	depTargets := make(map[string]bool)
	for _, ctr := range c.Containers {
		for _, dep := range ctr.DependsOn {
			depTargets[dep] = true
		}
	}

	for i, ctr := range c.Containers {
		if ctr.Name == "" {
			return fmt.Errorf("container #%d is missing required field 'name'", i+1)
		}

		// Host is required only if the container is NOT solely a group member or dependency.
		needsHost := !groupMembers[ctr.Name] && !depTargets[ctr.Name]
		if ctr.Host == "" && needsHost {
			return fmt.Errorf("container %q is missing required field 'host'", ctr.Name)
		}
		if ctr.TargetPort == "" {
			return fmt.Errorf("container %q is missing required field 'target_port'", ctr.Name)
		}

		if seenNames[ctr.Name] {
			return fmt.Errorf("duplicate container name found: %q", ctr.Name)
		}
		seenNames[ctr.Name] = true

		if ctr.Host != "" {
			if seenHosts[ctr.Host] {
				return fmt.Errorf("duplicate host mapped: %q (in container %q)", ctr.Host, ctr.Name)
			}
			seenHosts[ctr.Host] = true
		}

		// Validate depends_on references exist.
		for _, dep := range ctr.DependsOn {
			if !nameSet[dep] {
				return fmt.Errorf("container %q depends on unknown container %q", ctr.Name, dep)
			}
			if dep == ctr.Name {
				return fmt.Errorf("container %q cannot depend on itself", ctr.Name)
			}
		}

		// Validate schedule expressions if present.
		if ctr.ScheduleStart != "" || ctr.ScheduleStop != "" {
			if err := validateScheduleCompatibility(ctr.ScheduleStart, ctr.ScheduleStop, c.Gateway.ScheduleTimezone); err != nil {
				return fmt.Errorf("container %q: %w", ctr.Name, err)
			}
		}
	}

	// Validate groups.
	seenGroupNames := make(map[string]bool)
	for i, g := range c.Groups {
		if g.Name == "" {
			return fmt.Errorf("group #%d is missing required field 'name'", i+1)
		}
		if g.Host == "" {
			return fmt.Errorf("group %q is missing required field 'host'", g.Name)
		}
		if len(g.Containers) == 0 {
			return fmt.Errorf("group %q has no containers", g.Name)
		}
		if seenGroupNames[g.Name] {
			return fmt.Errorf("duplicate group name found: %q", g.Name)
		}
		seenGroupNames[g.Name] = true

		// Group host must not conflict with container hosts or other group hosts.
		if seenHosts[g.Host] {
			return fmt.Errorf("group %q host %q conflicts with an existing host", g.Name, g.Host)
		}
		seenHosts[g.Host] = true

		for _, cn := range g.Containers {
			if !nameSet[cn] {
				return fmt.Errorf("group %q references unknown container %q", g.Name, cn)
			}
		}
	}

	// Detect dependency cycles via DFS.
	if err := detectDependencyCycles(c.Containers); err != nil {
		return err
	}

	return nil
}

// detectDependencyCycles performs a DFS-based cycle check on the depends_on graph.
func detectDependencyCycles(containers []ContainerConfig) error {
	// Build adjacency list.
	deps := make(map[string][]string, len(containers))
	for _, c := range containers {
		deps[c.Name] = c.DependsOn
	}

	const (
		unvisited = 0
		visiting  = 1
		visited   = 2
	)
	state := make(map[string]int, len(containers))

	var visit func(name string, path []string) error
	visit = func(name string, path []string) error {
		if state[name] == visited {
			return nil
		}
		if state[name] == visiting {
			return fmt.Errorf("dependency cycle detected: %s → %s",
				joinPath(path), name)
		}
		state[name] = visiting
		for _, dep := range deps[name] {
			if err := visit(dep, append(path, name)); err != nil {
				return err
			}
		}
		state[name] = visited
		return nil
	}

	for _, c := range containers {
		if state[c.Name] == unvisited {
			if err := visit(c.Name, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

// joinPath joins a cycle path for human-readable error messages.
func joinPath(path []string) string {
	result := ""
	for i, p := range path {
		if i > 0 {
			result += " → "
		}
		result += p
	}
	return result
}

// applyDefaults fills in sensible defaults for any unset field.
func applyDefaults(cfg *GatewayConfig) {
	if cfg.Gateway.Port == "" {
		cfg.Gateway.Port = "8080"
	}
	if cfg.Gateway.LogLines == 0 {
		cfg.Gateway.LogLines = 30
	}
	if cfg.Gateway.DiscoveryInterval == 0 {
		cfg.Gateway.DiscoveryInterval = 15 * time.Second
	}
	if cfg.Gateway.AdminAuth.Method == "" {
		cfg.Gateway.AdminAuth.Method = "none"
	}

	for i := range cfg.Containers {
		c := &cfg.Containers[i]
		if c.TargetPort == "" {
			c.TargetPort = "80"
		}
		if c.StartTimeout == 0 {
			c.StartTimeout = 60 * time.Second
		}
		// IdleTimeout 0 means "never auto-stop" — no default override needed
		if c.RedirectPath == "" {
			c.RedirectPath = "/"
		}
		if c.Icon == "" {
			c.Icon = "docker"
		}
	}

	for i := range cfg.Groups {
		g := &cfg.Groups[i]
		if g.Strategy == "" {
			g.Strategy = "round-robin"
		}
	}
}

// BuildHostIndex returns a map from Host header value → ContainerConfig for O(1) lookup.
func BuildHostIndex(cfg *GatewayConfig) map[string]*ContainerConfig {
	idx := make(map[string]*ContainerConfig, len(cfg.Containers))
	for i := range cfg.Containers {
		if cfg.Containers[i].Host != "" {
			idx[cfg.Containers[i].Host] = &cfg.Containers[i]
		}
	}
	return idx
}

// BuildGroupHostIndex returns a map from Host header value → GroupConfig for O(1) lookup.
func BuildGroupHostIndex(cfg *GatewayConfig) map[string]*GroupConfig {
	idx := make(map[string]*GroupConfig, len(cfg.Groups))
	for i := range cfg.Groups {
		if cfg.Groups[i].Host != "" {
			idx[cfg.Groups[i].Host] = &cfg.Groups[i]
		}
	}
	return idx
}

// BuildContainerMap returns a map from container name → ContainerConfig for quick lookup.
func BuildContainerMap(cfg *GatewayConfig) map[string]*ContainerConfig {
	m := make(map[string]*ContainerConfig, len(cfg.Containers))
	for i := range cfg.Containers {
		m[cfg.Containers[i].Name] = &cfg.Containers[i]
	}
	return m
}
