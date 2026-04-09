# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [1.1.0] - 2026-04-09

### Added

- **Cron schedule display on status dashboard** — container cards now show
  a "Scheduled Activities" block (start/stop cron expressions, timezone,
  next event) or a "Scheduled Downtime" block (red) when outside the
  configured schedule window
- Schedule fields exposed in `/_status/api` JSON response:
  `schedule_start`, `schedule_stop`, `schedule_timezone`,
  `scheduled_downtime`, `next_scheduled_start`

### Fixed

- Material Symbols font now loaded in `status.html` so schedule icons
  render correctly instead of showing as raw text
- Schedule block background colour resolves correctly in dark mode
  (`bg-bg-dark` instead of the undefined `bg-background-dark`)

### Changed

- Light mode card definition improved: visible box-shadow, `border-slate-200`,
  stronger text contrast (`slate-500` labels), icon box uses `bg-blue-50`
- Schedule block text sizes increased for readability

## [1.0.0] - 2026-04-09

### Added

- **On-demand container awakening** — reverse proxy wakes stopped Docker containers on
  first request, shows an animated loading page with live log streaming, then redirects
  transparently once the container is ready
- **Host-header routing** via `config.yaml` (static configuration, hot-reloadable via `SIGHUP`)
- **Docker label auto-discovery** — containers opt in with `dag.enabled=true` and configure
  themselves via labels (`dag.host`, `dag.target_port`, `dag.start_timeout`, `dag.idle_timeout`,
  `dag.health_path`, `dag.icon`, `dag.network`, `dag.redirect_path`, `dag.depends_on`,
  `dag.schedule_start`, `dag.schedule_stop`, `dag.schedule_timezone`)
- **Container dependency ordering** — `depends_on` with topological sort ensures
  dependencies start and become healthy before dependents
- **Round-robin load balancing** across container groups (`GroupConfig`)
- **TCP and HTTP readiness probes** to confirm container availability before redirecting
- **Idle watcher** — automatically stops containers after configurable inactivity period
- **Cron-based scheduling** (`schedule_start`, `schedule_stop`) with global and
  per-container `schedule_timezone` support (IANA timezone names)
- **Scheduled downtime page** served when a request arrives outside the configured
  schedule window, showing the next scheduled start time
- **Admin status dashboard** at `/_status` with real-time heartbeat and idle countdowns
- **Admin REST API** at `/_status/api` and manual wake endpoint at `/_status/wake`
- **Admin authentication** — `none` (default), HTTP Basic, or Bearer token, configurable
  via `config.yaml` or `ADMIN_AUTH_*` environment variables
- **Prometheus metrics** at `/_metrics` — request counters/histograms, container start
  duration, idle stop counters
- **WebSocket proxying** via HTTP connection hijacking
- **Per-IP rate limiting** (1 req/s) on polling and admin endpoints
- **Trusted proxy support** for correct `X-Forwarded-For` / `X-Forwarded-Proto` handling
- **Structured JSON logging** via `log/slog` with graceful shutdown on `SIGTERM`/`SIGINT`
- **Multi-stage Docker build** — Go 1.24 Alpine builder, distroless final image (~22 MB)
- Unit tests for config loading, scheduler, admin auth, discovery, manager, and server

[Unreleased]: https://github.com/ares-17/docker-gateway/compare/v1.1.0...HEAD
[1.1.0]: https://github.com/ares-17/docker-gateway/compare/v1.0.0...v1.1.0
[1.0.0]: https://github.com/ares-17/docker-gateway/releases/tag/v1.0.0
