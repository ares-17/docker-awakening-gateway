---
title: Scheduling
nav_order: 5
---

# Scheduling
{: .no_toc }

<details open markdown="block">
  <summary>Contents</summary>
  {: .text-delta }
- TOC
{:toc}
</details>

The gateway supports two scheduling features:

- **Cron scheduling** — proactively start and/or stop containers at configured times using standard 5-field cron expressions.
- **Idle countdown** — the `/_status` dashboard shows a live countdown bar to the next idle-triggered stop for every running container with an `idle_timeout` set.

---

## Cron Scheduling

### How it works

Each container accepts two independent, optional cron fields: `schedule_start` and `schedule_stop`.

| Field | Behaviour when set |
|---|---|
| `schedule_start` only | Gateway starts the container at the scheduled time. No access blocking. |
| `schedule_stop` only | Gateway stops the container at the scheduled time. No access blocking. |
| Both | Proactive start + stop **and** requests outside the active window are blocked with an offline page (HTTP 503). |
| Neither | Unchanged on-demand behaviour. |

### Window detection

When both fields are set, the gateway determines whether the current moment is inside an active window using the last-fired times of each expression:

```
prevStart = last firing of schedule_start ≤ now
prevStop  = last firing of schedule_stop  ≤ now

prevStart > prevStop  →  inside window  → proxy request normally
prevStop  ≥ prevStart →  outside window → serve offline page (HTTP 503)
```

This approach handles overnight schedules (e.g. start at 22:00, stop at 06:00) correctly without any extra configuration.

### Offline page

When a request arrives outside the scheduled window, the gateway returns HTTP **503** and serves a styled offline page instead of a loading page or error. The page shows:

- `STATUS: OFFLINE` badge (red)
- Container name
- Next scheduled start time (e.g. `Mon 14 Apr · 08:00`)

No JavaScript polling — the page is fully static.

---

## Configuration

### Via `config.yaml`

Add `schedule_start` and/or `schedule_stop` to any container definition:

```yaml
containers:
  - name: "my-app"
    host: "my-app.example.com"
    target_port: "3000"
    # Start at 08:00 Mon–Fri, stop at 20:00 Mon–Fri
    schedule_start: "0 8 * * 1-5"
    schedule_stop:  "0 20 * * 1-5"
```

### Via Docker labels

```yaml
services:
  my-app:
    image: my-app:latest
    labels:
      - "dag.enabled=true"
      - "dag.host=my-app.localhost"
      - "dag.schedule_start=0 8 * * 1-5"
      - "dag.schedule_stop=0 20 * * 1-5"
```

### Cron expression format

Standard 5-field cron (minute, hour, day-of-month, month, day-of-week):

```
┌─── minute (0–59)
│ ┌─── hour (0–23)
│ │ ┌─── day of month (1–31)
│ │ │ ┌─── month (1–12)
│ │ │ │ ┌─── day of week (0–7, 0 and 7 = Sunday)
│ │ │ │ │
* * * * *
```

| Expression | Meaning |
|---|---|
| `0 8 * * 1-5` | 08:00 Monday–Friday |
| `0 20 * * 1-5` | 20:00 Monday–Friday |
| `0 22 * * *` | 22:00 every day |
| `0 6 * * *` | 06:00 every day |
| `30 7 * * 1` | 07:30 every Monday |
| `0 0 1 * *` | Midnight on the 1st of every month |

---

## Compatibility validation

When both `schedule_start` and `schedule_stop` are present, the gateway validates that the two expressions do not fire at the same minute within the next 7 days. An invalid or conflicting pair causes the gateway to **refuse to start** (or reject the hot-reload), with a clear error message:

```
container "my-app": schedule_start and schedule_stop fire at the same time (Mon 13 Apr 08:00)
```

Malformed cron expressions also produce a validation error at load time.

---

## Examples

### Office hours (Mon–Fri 08:00–20:00)

```yaml
schedule_start: "0 8 * * 1-5"
schedule_stop:  "0 20 * * 1-5"
```

### Overnight (start at 22:00, stop at 06:00 every day)

```yaml
schedule_start: "0 22 * * *"
schedule_stop:  "0 6 * * *"
```

### Only scheduled start (no access blocking, just proactive wake)

```yaml
schedule_start: "0 8 * * *"
# No schedule_stop — container runs until idle_timeout kicks in
idle_timeout: "2h"
```

### Only scheduled stop (force shutdown at midnight regardless of activity)

```yaml
schedule_stop: "0 0 * * *"
```

---

## Idle Countdown in `/_status`

For every container that is **running** and has an `idle_timeout` configured, the `/_status` dashboard shows a live countdown to the next idle-triggered stop:

```
⏱ idle stop in 7m 3s   [████████████████░░░░░░░] 78%
```

The bar depletes linearly and changes colour as time runs out:

| Remaining | Colour |
|---|---|
| > 40 % | Green |
| 20 – 40 % | Amber |
| < 20 % | Red |

If the container has never served a request since the gateway started, the bar shows `idle stop: no activity yet` instead.

The countdown is updated every 5 seconds via the existing `/_status/api` polling. Two new fields are included in each container entry:

```json
{
  "name": "my-app",
  "status": "running",
  "idle_timeout_sec": 1800,
  "idle_remaining_sec": 423
}
```

| Field | Type | Description |
|---|---|---|
| `idle_timeout_sec` | `int64` | Configured idle timeout in seconds; `0` if disabled |
| `idle_remaining_sec` | `int64` | Seconds until auto-stop; `-1` if no activity recorded yet; `0` if already at limit |

---

## Hot-reload behaviour

Scheduling is fully hot-reload compatible. When you send `SIGHUP`, the `ScheduleManager` re-registers all cron jobs atomically:

1. All existing cron entries are removed.
2. New entries are registered from the updated config.
3. Container jobs that were removed from config are not re-added.

No running containers are affected during the reload.

See **[Hot-Reload →](hot-reload.md)** for full details.
