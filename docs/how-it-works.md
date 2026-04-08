---
title: How It Works
nav_order: 3
---

# How It Works
{: .no_toc }

<details open markdown="block">
  <summary>Contents</summary>
  {: .text-delta }
- TOC
{:toc}
</details>

---

## Request Lifecycle

Every incoming HTTP request follows this decision path:

```
 Incoming Request (Host: my-app.example.com)
       │
       ▼
 Resolve Host header → ContainerConfig  (O(1) lookup)
       │
       ├─ No match → 404 Not Found
       │
       ├─ Match is a GROUP → round-robin pick → member ContainerConfig
       │
       └─ Match is a CONTAINER
              │
              ├─ schedule gate: IsInScheduleWindow?
              │       └─ outside window → HTTP 503 Offline Page (static, no polling)
              │
              ├─ container running?
              │       │
              │       ├─ YES: dependencies all running?
              │       │           ├─ YES → RecordActivity → Reverse Proxy → ✅
              │       │           └─ NO  → start deps async → Loading Page
              │       │
              │       └─ NO → InitStartState → start container async → Loading Page
              │
              └─ Loading Page
                     │
                     ├─ browser polls /_health every 2s
                     ├─ browser polls /_logs  every 3s  (live log box)
                     │
                     └─ status = "running" → redirect to redirect_path ✅
                        status = "failed"  → inline error box shown 🔴
```

---

## Component Architecture

```
docker-gateway/
├── main.go                    # Entry point: load config → start idle watcher → serve
├── config.yaml                # Per-container configuration (mounted via Docker volume)
│
└── gateway/
    ├── config.go              # YAML structs, loader, validation, host index, group index
    ├── docker.go              # Docker client: inspect, start, stop, logs, IP resolution
    ├── manager.go             # Concurrency-safe start states, idle auto-stop watcher
    ├── server.go              # HTTP server, routing, proxy headers, WebSocket tunnelling
    ├── scheduler.go           # ScheduleManager (cron jobs), IsInScheduleWindow
    ├── discovery.go           # Label-based container auto-discovery, config merging
    ├── group.go               # Round-robin GroupRouter
    ├── metrics.go             # Prometheus counter/histogram registration and recording
    ├── admin_auth.go          # Basic Auth / Bearer Token middleware
    └── templates/
        ├── loading.html       # Awakening page: log box + barber-pole progress + JS polling
        ├── error.html         # Failure state page
        ├── scheduled.html     # Offline page: HTTP 503, next scheduled start time
        └── status.html        # Admin status dashboard (dark/light, idle countdown)
```

### `manager.go` — Concurrency-safe State Machine

The `ContainerManager` tracks per-container start state (`starting` / `running` / `failed`) behind a `sync.RWMutex`. A per-container `sync.Mutex` (via `sync.Map`) ensures that if 100 requests arrive simultaneously for a sleeping container, only **one** goroutine calls `docker start` — the others serve the loading page immediately and wait for the shared state to transition.

### `discovery.go` — Label Polling

A background goroutine polls the Docker daemon every `discovery_interval` (default 15 s) for containers carrying `dag.enabled=true`. Discovered containers are merged with the static `config.yaml` configuration — static definitions always win on host conflicts.

### `server.go` — Proxy & WebSocket

HTTP proxying uses Go's standard `httputil.ReverseProxy`. WebSocket upgrades are detected and handled via raw TCP hijack + bidirectional `io.Copy`, so WebSocket connections pass through without modification.

---

## Internal Endpoints

These endpoints are excluded from the reverse proxy and handled by the gateway itself:

| Endpoint | Auth | Description |
|----------|------|-------------|
| `/_health?container=NAME` | ❌ | `{"status":"starting"\|"running"\|"failed"}` — polled by loading page JS |
| `/_logs?container=NAME` | ❌ | `{"lines":["..."]}` — last N log lines, polled every 3 s |
| `/_status` | 🔒 optional | Admin dashboard HTML page |
| `/_status/api` | 🔒 optional | JSON snapshot of all containers (polled every 5 s by dashboard) |
| `/_status/wake?container=NAME` | 🔒 optional | POST — triggers container start from dashboard |
| `/_metrics` | 🔒 optional | Prometheus metrics endpoint |

> Rate limiting: `/_health` and `/_logs` are limited to **1 request/s per IP** to protect against polling abuse.

---

## Timeout Behaviour

```
start_timeout  — from the moment the gateway triggers docker start
    │
    └─► container enters "running" + readiness probe passes → proxy request
    └─► timeout exceeded → error page shown

idle_timeout   — checked every 60 seconds (background goroutine)
    │
    └─► last request > idle_timeout ago AND container running → docker stop
    └─► next request arrives → back to start_timeout path
```

Both timeouts are configured **per container**. Setting `idle_timeout: 0` (the default) disables auto-stop.

---

## Cron Scheduling

The `ScheduleManager` runs a background cron loop (powered by `robfig/cron/v3`) that fires per-container start/stop jobs at the configured times. It is synchronised with config hot-reloads: on every `SIGHUP`, all existing entries are removed and re-registered from the updated config atomically.

```
schedule_start  — cron fires → manager.EnsureRunning(ctx, cfg)
schedule_stop   — cron fires → client.StopContainer(ctx, name)

handleRequest   — IsInScheduleWindow(cfg, now)
    │
    ├─ prevStart > prevStop  →  inside window  →  proxy normally
    └─ prevStop  ≥ prevStart →  outside window →  HTTP 503 scheduled.html
```

See **[Scheduling →](scheduling.md)** for cron syntax, examples, and the offline page.
