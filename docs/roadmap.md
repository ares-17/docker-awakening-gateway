---
title: Roadmap
nav_order: 12
---

# Docker Awakening Gateway — Roadmap

## ✅ Completed

### Core
- [x] **On-demand container startup** — containers sleep until a request arrives, then are started via Docker API
- [x] **Transparent reverse proxy** — once running, requests are proxied with zero loading page overhead
- [x] **Concurrency-safe start** — per-container mutex prevents duplicate start attempts on concurrent requests
- [x] **WebSocket support** — upgrade requests are tunnelled via raw TCP hijack to the backend
- [x] **Host-header routing** — O(1) lookup maps `Host` header → container config; supports N containers on one gateway
- [x] **Query-param fallback** — `?container=NAME` for testing without DNS

### Configuration & Operations
- [x] **YAML config file** (`config.yaml`) — per-container settings, mounted via volume
- [x] **`CONFIG_PATH` env override** — point to any path for the config file
- [x] **Config validation at startup** — gateway fails-fast if `config.yaml` is missing required fields or contains duplicate definitions
- [x] **Config hot-reload** — `docker kill -s HUP docker-gateway` reloads `config.yaml` at runtime without dropping connections
- [x] **Label-based auto-discovery** — gateway reads Docker labels (`dag.host`, `dag.target_port`, etc.) to automatically discover containers
- [x] **Per-container `start_timeout`** — max time to wait for docker start + TCP probe
- [x] **Per-container `idle_timeout`** — auto-stop containers idle longer than threshold (0 = disabled)
- [x] **Per-container `target_port`**, **`network`**, **`redirect_path`**
- [x] **Global `log_lines`** — number of container log lines shown in the loading UI
- [x] **Configurable discovery interval** — `gateway.discovery_interval` or `DISCOVERY_INTERVAL` env var

### Reliability
- [x] **TCP readiness probe** — after Docker reports "running", dial `ip:port` until the app responds
- [x] **HTTP health probe** — optionally call a container's `/health` endpoint to confirm readiness
- [x] **Early crash detection** — if container enters `exited`/`dead` during start, fail immediately
- [x] **Start state tracking** — `starting` / `running` / `failed` states with error message, exported via `/_health`
- [x] **Idle watcher goroutine** — background loop (every 60s) auto-stops containers exceeding `idle_timeout`
- [x] **Multi-network support** — resolves container IP from a named Docker network; falls back to first available
- [x] **Graceful shutdown** — `SIGTERM`/`SIGINT` triggers `http.Server.Shutdown()` with grace period

### Security
- [x] **Read-only Docker socket** — gateway only needs `ContainerInspect`, `ContainerStart`, `ContainerStop`, `ContainerLogs`
- [x] **Distroless final image** (`gcr.io/distroless/static`) — no shell, no package manager, ~22 MB
- [x] **Rate limiter on internal endpoints** — 1 req/s per IP on `/_health` and `/_logs`
- [x] **XSS-safe log rendering** — log lines injected via `textContent`, not `innerHTML`
- [x] **Vendored dependencies** — no network access needed during Docker build
- [x] **Admin endpoint authentication** — optional basic-auth or bearer token to protect `/_status/*` and `/_metrics`
- [x] **CORS / CSRF protection on `/_status/wake`** — prevent cross-origin container start abuse
- [x] **Rate limiter memory cleanup** — periodic eviction of stale IPs to prevent unbounded memory growth
- [x] **Trusted proxy configuration** — only trust `X-Forwarded-For` from known upstream proxies

### Proxy Headers
- [x] **`X-Forwarded-For`** — appends client IP to the forwarding chain
- [x] **`X-Real-IP`** — original client IP (not overwritten if already set upstream)
- [x] **`X-Forwarded-Proto`** — upstream value preserved; defaults to `http`
- [x] **`X-Forwarded-Host`** — original `Host` header value

### Frontend (loading page)
- [x] **Animated loading page** — dark-themed, breathing container icon, barber-pole progress bar
- [x] **Live log box** — polls `/_logs` every 3s, renders last N lines with auto-scroll
- [x] **Inline error state** — on `status=failed`, swaps progress bar for error box in-place; shows retry button
- [x] **Auto-redirect on ready** — polls `/_health` every 2s; navigates to `redirect_path` when running

### Admin & Observability
- [x] **`/_status` dashboard** — HTML admin page with live status, heartbeat bars, uptime, last request, dark/light mode
- [x] **`/_status/api` JSON endpoint** — snapshot of all containers, polled every 5s
- [x] **`/_status/wake` action** — POST endpoint to trigger container start from dashboard
- [x] **Prometheus `/metrics` endpoint** — per-container counters for requests, starts, durations, idle stops

### Groups & Dependencies
- [x] **Container grouping / round-robin routing** — start a group of containers, load-balance across replicas
- [x] **Dependency-ordered startup** — `depends_on` triggers topological sort before proxying

### Scheduling
- [x] **Cron-based start scheduling** — `schedule_start` cron expression triggers proactive container start
- [x] **Cron-based stop scheduling** — `schedule_stop` cron expression triggers proactive container stop
- [x] **Access window enforcement** — when both are set, requests outside the window get HTTP 503 with an offline page showing next scheduled start
- [x] **Cron via Docker labels** — `dag.schedule_start` and `dag.schedule_stop` for label-discovered containers
- [x] **Hot-reload for schedules** — cron jobs re-registered atomically on `SIGHUP`
- [x] **Idle countdown in `/_status`** — live colour-coded countdown bar (green → amber → red) for running containers with `idle_timeout`

### Quality
- [x] **Structured logging** — Go 1.21+ `log/slog` JSON-structured output
- [x] **Discovery change detection** — only reload when merged config actually differs
- [x] **Unit tests** — table-driven tests for config, discovery, rate limiter, proxy routing, security, scheduling

---

## 📅 Medium-term

- [ ] **Customisable loading page** — per-container colour/logo/message overrides
- [ ] **Weighted load balancing** — support `strategy: weighted` with per-container relative weights

---

## 🔭 Long-term

- [ ] **Multi-instance / distributed state** — share `startStates` and `lastSeen` via Redis or etcd
- [ ] **Built-in TLS termination** — ACME/Let's Encrypt via `golang.org/x/crypto/acme/autocert`

---

## Known Limitations (by design)

- **Single host only** — communicates with the local Docker socket; remote Docker hosts not supported
- **HTTP only** — TLS expected to be handled by an upstream proxy (Nginx, Caddy, Traefik)
- **In-memory state** — start states and activity timestamps reset on gateway restart
