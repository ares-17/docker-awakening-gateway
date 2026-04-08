---
layout: home
title: Home
nav_order: 1
---

# Docker Awakening Gateway
{: .fs-9 }

An ultra-lightweight reverse proxy that **wakes sleeping Docker containers on demand** — with a live loading page, idle auto-stop, and zero-overhead proxying once the container is up.
{: .fs-5 .fw-300 }

[Get Started](getting-started.md){: .btn .btn-primary .fs-5 .mb-4 .mb-md-0 .mr-2 }
[Configuration Reference](configuration.md){: .btn .fs-5 .mb-4 .mb-md-0 }

---

<table>
<tr>
<td width="50%">
<img src="assets/images/awakening-dark.png" alt="Loading page — container awakening" />
</td>
<td width="50%">
<img src="assets/images/dashboard-dark.png" alt="Admin status dashboard" />
</td>
</tr>
</table>

## Why?

In home labs, edge devices, and resource-constrained environments, keeping every service running 24/7 wastes RAM and CPU. The gateway lets containers **sleep when idle** and **wake instantly on first request**, without any user-facing action required.

## Feature Highlights

| Feature | Details |
|---------|---------|
| **On-demand startup** | Container sleeps until a request arrives — then wakes automatically |
| **Live loading page** | Animated UI with real-time container logs while the container boots |
| **Idle auto-stop** | Configurable `idle_timeout` per container; background watcher stops idle containers |
| **Cron scheduling** | Per-container `schedule_start` / `schedule_stop` cron expressions; requests outside the active window get a styled offline page |
| **Idle countdown** | `/_status` dashboard shows a live colour-coded countdown bar to the next idle stop |
| **Transparent proxy** | Zero overhead once the container is running — full HTTP + WebSocket support |
| **Label discovery** | Add `dag.enabled=true` to any container; no static config needed |
| **Dependency ordering** | `depends_on` with topological sort — start `postgres` before `app` automatically |
| **Load balancing** | Round-robin across container groups behind a single hostname |
| **Admin dashboard** | `/_status` page with live container status, heartbeat bars, start/stop actions |
| **Prometheus metrics** | Per-container counters for requests, starts, durations, idle stops |
| **Admin auth** | Optional Basic Auth or Bearer Token on `/_status` and `/_metrics` |
| **Hot-reload** | `docker kill -s HUP` reloads `config.yaml` without dropping connections |
| **Ultra-lightweight** | Static Go binary, distroless final image — **~22 MB** |

## Quick look

```
 Request → my-app.example.com
      │
      ├─ container running? ──YES──► Reverse Proxy → response
      │
      └─ container stopped?
             ├──► Show loading page (live logs + progress bar)
             └──► docker start → TCP/HTTP readiness probe
                                       │
                                 browser polls /_health
                                       │
                                 status = "running"
                                       │
                                 redirect to redirect_path
```

---

## Navigation

- **[Getting Started](getting-started.md)** — install, quick start, test scenarios
- **[How It Works](how-it-works.md)** — request lifecycle, component architecture
- **[Configuration](configuration.md)** — all options for `config.yaml` and Docker labels
- **[Scheduling](scheduling.md)** — cron-based start/stop, offline page, idle countdown in dashboard
- **[Security](security.md)** — admin auth, trusted proxies, Docker socket, distroless
- **[Hot-Reload](hot-reload.md)** — live config updates without restarts
- **[Groups & Dependencies](groups-and-dependencies.md)** — load balancing, dependency ordering
- **[Health Probe & Discovery](health-probe-and-discovery.md)** — HTTP readiness probes, discovery interval tuning
- **[Prometheus Monitoring](prometheus.md)** — metrics, Grafana dashboards, PromQL examples
- **[Testing](testing.md)** — running the test suite, test coverage breakdown
- **[Roadmap](roadmap.md)** — what's done, what's planned
