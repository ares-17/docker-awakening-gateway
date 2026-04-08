# Design: Cron Scheduling & Idle Timeout Improvements

**Date:** 2026-04-08  
**Status:** Approved  
**Scope:** Two features — (1) cron-based start/stop scheduling per container, (2) idle timeout countdown in the status dashboard.

---

## 1. Background & Goals

Docker Awakening Gateway already supports on-demand container wake-up and idle timeout auto-stop. This design adds:

1. **Cron scheduling** — containers can be started and/or stopped at precise times via cron expressions, configurable in `config.yaml` and via Docker labels.
2. **Blocked access page** — when both `schedule_start` and `schedule_stop` are set and the current time is outside the active window, requests are intercepted and a static "offline" page is shown with the next scheduled start time.
3. **Idle timeout visibility** — the `/_status` dashboard shows a live countdown to the next idle-triggered stop for running containers.

---

## 2. Configuration

### 2.1 New fields in `ContainerConfig` (`gateway/config.go`)

```go
ScheduleStart string `yaml:"schedule_start"` // cron expr, e.g. "0 8 * * 1-5"
ScheduleStop  string `yaml:"schedule_stop"`  // cron expr, e.g. "0 20 * * 1-5"
```

Both fields are **independently optional**:

| Present fields | Behaviour |
|---|---|
| Only `schedule_start` | Gateway proactively starts container at cron time. No access blocking. |
| Only `schedule_stop`  | Gateway proactively stops container at cron time. No access blocking. |
| Both | Proactive start+stop AND access blocked outside the active window. |
| Neither | Unchanged behaviour. |

### 2.2 Validation

When both fields are present, the gateway verifies compatibility by computing the next 10 occurrences of each expression over a 7-day window and asserting they never fire in the same minute. Malformed cron expressions are rejected at config load time.

### 2.3 New Docker labels (`gateway/docker.go`)

```
dag.schedule_start   →  ContainerConfig.ScheduleStart
dag.schedule_stop    →  ContainerConfig.ScheduleStop
```

---

## 3. ScheduleManager (`gateway/scheduler.go`)

New component, single responsibility: register and execute cron start/stop jobs.

```go
type ScheduleManager struct {
    cron    *cron.Cron                   // robfig/cron/v3, minute-resolution
    client  *DockerClient
    manager *ContainerManager
    mu      sync.Mutex
    entries map[string][]cron.EntryID    // containerName → [startID?, stopID?]
}
```

### 3.1 Public API

| Method | Description |
|---|---|
| `NewScheduleManager(client, manager)` | Constructor |
| `Start(ctx context.Context)` | Starts the internal cron loop |
| `Sync(containers []ContainerConfig)` | Diffs registered entries; removes stale, adds new. Idempotent. Called on startup and every hot-reload. |
| `Stop()` | Gracefully stops the cron loop |

### 3.2 Job behaviour

- **Start job**: calls `manager.EnsureRunning(ctx, cfg)` with a context deadline equal to `cfg.StartTimeout`.
- **Stop job**: calls `client.StopContainer(ctx, cfg.Name)` directly.

### 3.3 Integration points

- `main.go` / `server.go`: `ScheduleManager` is constructed alongside `ContainerManager` and `Server`.
- `Server.ReloadConfig` calls `scheduleManager.Sync(newCfg.Containers)` after swapping the config.

### 3.4 Dependency

`github.com/robfig/cron/v3` — added via `go get` + `go mod vendor`.

---

## 4. Access control: `IsInScheduleWindow`

Function signature (in `gateway/scheduler.go`):

```go
func IsInScheduleWindow(cfg *ContainerConfig, now time.Time) (allowed bool, nextStart time.Time)
```

**Algorithm** (only active when both `ScheduleStart` and `ScheduleStop` are set):

```
prevStart = last firing of schedule_start ≤ now
prevStop  = last firing of schedule_stop  ≤ now

if prevStart > prevStop  →  allowed = true
else                     →  allowed = false, nextStart = next firing of schedule_start after now
```

If either field is empty → returns `(true, zero)` unconditionally.

### 4.1 Integration in `handleRequest` (`gateway/server.go`)

After resolving `ContainerConfig`, before any start logic:

```go
if allowed, nextStart := IsInScheduleWindow(cfg, time.Now()); !allowed {
    s.serveScheduledPage(w, r, cfg, nextStart)
    return
}
```

---

## 5. New template: `gateway/templates/scheduled.html`

Static page served with `HTTP 503`. Same visual style as `loading.html` (Tailwind CDN, JetBrains Mono, `bg-dark` / `surface-dark` palette).

### Template data

```go
type scheduledPageData struct {
    ContainerName string
    NextStart     string  // formatted: "Mon 09 Jun · 08:00"
}
```

### Visual layout

- Header badge: `STATUS: OFFLINE` (red)
- Container icon (same animated grid, static variant)
- Title: `Container [name] is offline`
- Subtitle: `This service is not available right now`
- Mono line: `Next scheduled start: Mon 09 Jun · 08:00`
- No JS polling — fully static response

---

## 6. Idle timeout countdown in `/_status`

### 6.1 API change (`handleStatusAPI`)

Each container entry in the JSON response gains two new fields:

```json
{
  "name": "my-app",
  "status": "running",
  "idle_timeout_sec": 1800,
  "idle_remaining_sec": 423
}
```

- `idle_timeout_sec`: 0 if not configured.
- `idle_remaining_sec`: seconds until idle stop; -1 if container has never served a request; 0 if not applicable (stopped / no idle_timeout).

Calculation:

```go
remaining = int64(cfg.IdleTimeout.Seconds()) - int64(time.Since(lastSeen).Seconds())
if remaining < 0 { remaining = 0 }
```

### 6.2 UI change (`status.html`)

For each running container card where `idle_timeout_sec > 0`:

- Text line: `⏱ idle stop in Xm Ys` (updated on every 5s poll)
- Thin progress bar below the status badge, linearly depleting from full (just started) to empty (about to stop), colour transitions from `status-running` green → amber → red in the last 20%.

---

## 7. Testing

| Test name | File | What it covers |
|---|---|---|
| `TestScheduleCompatibility` | `gateway/scheduler_test.go` | Valid pairs, same-minute conflict, malformed expressions |
| `TestIsInScheduleWindow` | `gateway/scheduler_test.go` | Inside window, outside window, only start, only stop, midnight boundary |
| `TestScheduleManagerSync` | `gateway/scheduler_test.go` | Entry count after Sync, re-Sync idempotency, removal of stale entries |
| `TestIdleRemainingCalc` | `gateway/manager_test.go` | Correct seconds with mock lastSeen, zero/negative clamp, no-lastSeen case |

All tests follow the existing pattern in `gateway/*_test.go` (table-driven, no external test helpers beyond the standard library).

---

## 8. Files changed / created

| File | Change |
|---|---|
| `gateway/config.go` | Add `ScheduleStart`, `ScheduleStop` to `ContainerConfig`; add validation |
| `gateway/docker.go` | Add `dag.schedule_start`, `dag.schedule_stop` label parsing |
| `gateway/scheduler.go` | **New** — `ScheduleManager`, `IsInScheduleWindow` |
| `gateway/scheduler_test.go` | **New** — all scheduler tests |
| `gateway/server.go` | Wire `ScheduleManager`; add schedule check in `handleRequest`; enrich `handleStatusAPI` |
| `gateway/manager_test.go` | Add `TestIdleRemainingCalc` |
| `gateway/templates/scheduled.html` | **New** — offline/blocked page |
| `gateway/templates/status.html` | Add idle countdown bar and text |
| `main.go` | Construct and wire `ScheduleManager` |
| `go.mod` / `go.sum` / `vendor/` | Add `robfig/cron/v3` |
