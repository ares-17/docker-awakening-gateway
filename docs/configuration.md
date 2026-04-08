---
title: Configuration
nav_order: 4
---

# Configuration Reference
{: .no_toc }

<details open markdown="block">
  <summary>Contents</summary>
  {: .text-delta }
- TOC
{:toc}
</details>

The Docker Awakening Gateway provides two distinct ways to manage containers: **Label-based Auto-Discovery** and **Static Configuration**. You can use either method exclusively, or combine them — static definitions always win on host conflicts.

---

## 1. Label-based Auto-Discovery (Recommended)

Attach Docker labels directly to your containers. The gateway polls the Docker daemon every `discovery_interval` (default: 15 s) for containers carrying `dag.enabled=true`.

### Required Labels

| Label | Example | Description |
|-------|---------|-------------|
| `dag.enabled` | `true` | Tells the gateway to manage this container |
| `dag.host` | `app.example.com` | `Host` header to match incoming traffic against |

### Optional Labels

| Label | Default | Description |
|-------|---------|-------------|
| `dag.target_port` | `80` | Port the container listens on |
| `dag.start_timeout` | `60s` | Max time to wait for container boot before error page |
| `dag.idle_timeout` | `0` (disabled) | Inactivity time before auto-stop (e.g. `15m`, `1h`) |
| `dag.network` | `""` | Docker network to resolve container IP from |
| `dag.redirect_path` | `/` | URL path to redirect to after successful boot |
| `dag.icon` | `docker` | [Simple Icons](https://simpleicons.org/) slug for the `/_status` dashboard |
| `dag.health_path` | `""` | HTTP path (e.g. `/healthz`) for readiness probe instead of TCP |
| `dag.depends_on` | `""` | Comma-separated container names to start first (e.g. `postgres,redis`) |
| `dag.schedule_start` | `""` | Cron expression to start the container proactively (e.g. `0 8 * * 1-5`) |
| `dag.schedule_stop` | `""` | Cron expression to stop the container proactively (e.g. `0 20 * * 1-5`) |

### Example

```yaml
services:
  my-app:
    image: my-app:latest
    container_name: my-app
    labels:
      - "dag.enabled=true"
      - "dag.host=my-app.localhost"
      - "dag.target_port=3000"
      - "dag.start_timeout=120s"
      - "dag.idle_timeout=30m"
      - "dag.icon=nodedotjs"
      - "dag.health_path=/healthz"
```

---

## 2. Static Configuration (`config.yaml`)

The gateway loads `config.yaml` from `/etc/gateway/config.yaml` by default. Override the path with the `CONFIG_PATH` environment variable.

### Global Settings (`gateway:`)

```yaml
gateway:
  port: "8080"              # Listening port (default: 8080)
  log_lines: 30             # Log lines shown in the loading page UI
  discovery_interval: "15s" # How often to poll Docker for labeled containers

  trusted_proxies:          # CIDRs whose X-Forwarded-For is trusted for rate limiting
    - "10.0.0.0/8"
    - "172.16.0.0/12"
    - "192.168.0.0/16"

  admin_auth:               # Optional auth on /_status/* and /_metrics (see below)
    method: "none"          # "none" (default), "basic", or "bearer"
```

> [!NOTE]
> `gateway.port` and `admin_auth` settings are **not hot-reloaded** — a container restart is required to change them. All other settings are applied on `SIGHUP`.

#### Admin Auth
{: #admin-auth }

Protect the admin and metrics endpoints with authentication:

**Basic Auth (browser login dialog):**

```yaml
gateway:
  admin_auth:
    method: "basic"
    username: "admin"
    password: "s3cret-passw0rd"
```

**Bearer Token (Prometheus / automation):**

```yaml
gateway:
  admin_auth:
    method: "bearer"
    token: "my-super-secret-token"
```

**Environment variable overrides** (higher priority than YAML):

| Variable | Description |
|----------|-------------|
| `ADMIN_AUTH_METHOD` | `none`, `basic`, or `bearer` |
| `ADMIN_AUTH_USERNAME` | Username (required for `basic`) |
| `ADMIN_AUTH_PASSWORD` | Password (required for `basic`) |
| `ADMIN_AUTH_TOKEN` | Token (required for `bearer`) |

See **[Security →](security.md)** for full details, protected endpoints, and usage examples.

---

### Static Container Definitions (`containers:`)

```yaml
containers:
  - name: "my-app"               # (Required) Docker container name
    host: "my-app.example.com"   # (Required) Host header to match
    target_port: "3000"          # (Default: 80)
    start_timeout: "120s"        # (Default: 60s)
    idle_timeout: "30m"          # (Default: 0 — disabled)
    network: "backend-net"       # (Default: "" — first attached network)
    redirect_path: "/login"      # (Default: /)
    icon: "postgresql"           # (Default: docker)
    health_path: "/healthz"      # (Default: "" — TCP probe)
    depends_on: ["postgres"]     # (Default: [])
    schedule_start: "0 8 * * 1-5"  # (Default: "" — disabled) cron to start proactively
    schedule_stop:  "0 20 * * 1-5" # (Default: "" — disabled) cron to stop proactively
```

> [!NOTE]
> When both `schedule_start` and `schedule_stop` are set, requests outside the active window are blocked with an HTTP 503 offline page. See **[Scheduling →](scheduling.md)** for full details and examples.

---

### Container Groups (`groups:`)

Groups map a single host to multiple containers for **round-robin load balancing**:

```yaml
groups:
  - name: "api-cluster"
    host: "api.example.com"
    strategy: "round-robin"        # (Default: round-robin)
    containers: ["api-1", "api-2", "api-3"]
```

See **[Groups & Dependencies →](groups-and-dependencies.md)** for full documentation.

---

## Hot-Reloading

Send `SIGHUP` to reload `config.yaml` without dropping connections:

```bash
docker kill -s HUP docker-gateway
```

See **[Hot-Reload →](hot-reload.md)** for what is and isn't reloaded.

---

## Mixing Both Methods

You can freely mix static config and label discovery:

- Use `config.yaml` only for the `gateway:` global block; let containers auto-discover via labels.
- Define critical containers statically in `config.yaml`; let development containers join via labels.

**Conflict resolution:** If the same `Host` is claimed both in `config.yaml` and via a label, the **static definition wins** and the label conflict is logged and skipped.
