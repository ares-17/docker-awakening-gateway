# docker-gateway — interactive demo

**docker-gateway** is a lightweight reverse proxy that wakes stopped Docker containers on demand. When a request arrives for a sleeping container, the gateway starts it, streams its logs live on a loading page, and transparently proxies the request once the container is ready.

This Codespaces environment has three demo containers pre-configured. All containers start **stopped** — the gateway wakes them when you send a request.

---

## Scenarios

### 1. Awakening animation (slow-app)

Sends a request to a container with a 15-second boot time. You'll see the live loading page with log streaming and a progress bar.

```bash
curl -si -H 'Host: slow-app.localhost' http://localhost:8080/ | head -20
```

The first call returns the loading page HTML (the gateway wakes the container in the background). Poll `/_health` until the container is running, then the browser would auto-redirect. Re-send the curl after ~15s to get the proxied response.

### 2. Error page (fail-app)

This container always crashes on startup. The gateway detects the failure and serves an error page.

```bash
curl -si -H 'Host: fail-app.localhost' http://localhost:8080/ | head -20
```

### 3. Real application (Uptime Kuma dashboard)

A production-style monitoring app with a natural boot time (~10s).

```bash
curl -si -H 'Host: dashboard.localhost' http://localhost:8080/ | head -20
```

---

## Admin dashboard

The status dashboard is always accessible — no Host routing required:

```bash
curl -s http://localhost:8080/_status/api | jq .
```

Or open the port 8080 preview in VS Code and navigate to `/_status`.

The dashboard shows:
- Container state (stopped / starting / running / failed)
- Idle countdown and last-seen timestamp
- Per-container metrics
- Wake / stop buttons

---

## Metrics

Prometheus-compatible metrics are exposed at:

```bash
curl -s http://localhost:8080/_metrics
```

---

## Useful commands

```bash
# Watch gateway logs in real time
docker compose -f docker-compose.demo.yml logs -f gateway

# See all container states
docker compose -f docker-compose.demo.yml ps

# Stop everything
docker compose -f docker-compose.demo.yml down
```

---

## How it works

1. A request arrives with a `Host` header (e.g. `slow-app.localhost`)
2. The gateway looks up the container mapped to that host
3. If the container is stopped → starts it, serves the loading page, polls `/_health` every 2s
4. Once the container is healthy → browser redirects, gateway proxies all subsequent requests
5. After `idle_timeout` of inactivity → gateway stops the container automatically

Full documentation: [docker-gateway docs](https://ares-17.github.io/docker-gateway)
