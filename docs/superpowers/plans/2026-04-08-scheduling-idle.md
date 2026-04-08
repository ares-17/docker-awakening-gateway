# Cron Scheduling & Idle Timeout Improvements — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add cron-based start/stop scheduling per container and an idle-timeout countdown to the `/_status` dashboard.

**Architecture:** A new `ScheduleManager` (robfig/cron/v3) registers per-container jobs and syncs on every hot-reload. `IsInScheduleWindow` blocks out-of-schedule requests in `handleRequest`. A new `scheduled.html` template (same dark-theme style) serves the "offline" page. The status API gains two new numeric fields for the JS countdown.

**Tech Stack:** Go 1.24, `github.com/robfig/cron/v3`, Tailwind CDN, JetBrains Mono.

---

## File map

| File | Action | Responsibility |
|---|---|---|
| `go.mod` / `go.sum` / `vendor/` | Modify | Add robfig/cron/v3 dependency |
| `gateway/config.go` | Modify | `ScheduleStart`/`ScheduleStop` fields + validation |
| `gateway/config_test.go` | Modify | `TestScheduleConfigValidation` |
| `gateway/docker.go` | Modify | Parse `dag.schedule_start` / `dag.schedule_stop` labels |
| `gateway/scheduler.go` | **Create** | `ScheduleManager`, `IsInScheduleWindow`, `prevFiring`, `validateScheduleCompatibility` |
| `gateway/scheduler_test.go` | **Create** | `TestScheduleCompatibility`, `TestIsInScheduleWindow`, `TestScheduleManagerSync` |
| `gateway/server.go` | Modify | Wire `ScheduleManager`; `serveScheduledPage`; schedule check in `handleRequest`; `calcIdleRemaining`; enrich `handleStatusAPI` |
| `gateway/manager_test.go` | Modify | `TestIdleRemainingCalc` |
| `gateway/templates/scheduled.html` | **Create** | "Offline" blocked page |
| `gateway/templates/status.html` | Modify | Idle countdown bar + text in container cards |
| `main.go` | Modify | Construct and wire `ScheduleManager` |

---

## Task 1: Add robfig/cron/v3 dependency

**Files:**
- Modify: `go.mod`, `go.sum`, `vendor/`

- [ ] **Step 1: Add the dependency**

```bash
cd /home/aress/Documenti/progetti/docker-gateway
go get github.com/robfig/cron/v3
```

Expected: `go.mod` and `go.sum` updated.

- [ ] **Step 2: Vendor it**

```bash
go mod vendor
```

Expected: `vendor/github.com/robfig/cron/` directory created.

- [ ] **Step 3: Verify build still passes**

```bash
go build -mod=vendor -o /dev/null .
```

Expected: no errors.

- [ ] **Step 4: Commit**

```bash
git add go.mod go.sum vendor/
git commit -m "chore: add robfig/cron/v3 for cron scheduling"
```

---

## Task 2: Config fields, validation, Docker labels

**Files:**
- Modify: `gateway/config.go`
- Modify: `gateway/config_test.go`
- Modify: `gateway/docker.go`

- [ ] **Step 1: Write the failing test in `gateway/config_test.go`**

Add after the existing `TestGatewayConfigEqual` function:

```go
func TestScheduleConfigValidation(t *testing.T) {
	base := func() *GatewayConfig {
		return &GatewayConfig{
			Gateway: GlobalConfig{Port: "8080", LogLines: 30,
				DiscoveryInterval: 15 * time.Second,
				AdminAuth:         AdminAuthConfig{Method: "none"}},
			Containers: []ContainerConfig{
				{Name: "app", Host: "app.local", TargetPort: "80",
					StartTimeout: 60 * time.Second},
			},
		}
	}

	tests := []struct {
		name          string
		scheduleStart string
		scheduleStop  string
		wantErr       bool
	}{
		{"no schedule", "", "", false},
		{"only start valid", "0 8 * * 1-5", "", false},
		{"only stop valid", "", "0 18 * * 1-5", false},
		{"both valid no overlap", "0 8 * * 1-5", "0 18 * * 1-5", false},
		{"both valid overnight", "0 22 * * *", "0 6 * * *", false},
		{"same minute conflict", "0 8 * * *", "0 8 * * *", true},
		{"invalid start expr", "not-a-cron", "0 18 * * *", true},
		{"invalid stop expr", "0 8 * * *", "not-a-cron", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := base()
			cfg.Containers[0].ScheduleStart = tt.scheduleStart
			cfg.Containers[0].ScheduleStop = tt.scheduleStop
			err := cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test -mod=vendor ./gateway/ -run TestScheduleConfigValidation -v
```

Expected: FAIL — `ScheduleStart` field not found.

- [ ] **Step 3: Add fields to `ContainerConfig` in `gateway/config.go`**

In `ContainerConfig`, after the `DependsOn` field:

```go
	// ScheduleStart is an optional standard 5-field cron expression (e.g. "0 8 * * 1-5")
	// that triggers a proactive container start. When combined with ScheduleStop,
	// requests outside the active window are blocked with a 503 page.
	ScheduleStart string `yaml:"schedule_start"`
	// ScheduleStop is an optional standard 5-field cron expression (e.g. "0 20 * * 1-5")
	// that triggers a proactive container stop.
	ScheduleStop string `yaml:"schedule_stop"`
```

- [ ] **Step 4: Add validation call in `Validate()` in `gateway/config.go`**

Inside the `for i, ctr := range c.Containers` loop, after the `depends_on` validation block and before the closing brace:

```go
		// Validate schedule expressions if present.
		if ctr.ScheduleStart != "" || ctr.ScheduleStop != "" {
			if err := validateScheduleCompatibility(ctr.ScheduleStart, ctr.ScheduleStop); err != nil {
				return fmt.Errorf("container %q: %w", ctr.Name, err)
			}
		}
```

- [ ] **Step 5: Run tests — will still fail because `validateScheduleCompatibility` is not defined yet**

```bash
go test -mod=vendor ./gateway/ -run TestScheduleConfigValidation -v
```

Expected: compile error — `undefined: validateScheduleCompatibility`.

- [ ] **Step 6: Create stub in `gateway/scheduler.go` to unblock compilation**

Create `gateway/scheduler.go` with just the stub (full implementation comes in Task 3):

```go
package gateway

import (
	"fmt"
	"time"

	"github.com/robfig/cron/v3"
)

// validateScheduleCompatibility returns an error if the two cron expressions
// are malformed or would fire at the same minute within the next 7 days.
func validateScheduleCompatibility(startExpr, stopExpr string) error {
	var startSched, stopSched cron.Schedule
	var err error

	if startExpr != "" {
		startSched, err = cron.ParseStandard(startExpr)
		if err != nil {
			return fmt.Errorf("schedule_start: invalid cron expression %q: %w", startExpr, err)
		}
	}
	if stopExpr != "" {
		stopSched, err = cron.ParseStandard(stopExpr)
		if err != nil {
			return fmt.Errorf("schedule_stop: invalid cron expression %q: %w", stopExpr, err)
		}
	}

	// Only check for conflicts when both are set.
	if startSched == nil || stopSched == nil {
		return nil
	}

	now := time.Now().Truncate(time.Minute)
	window := now.Add(7 * 24 * time.Hour)

	startMinutes := make(map[time.Time]bool)
	t := now
	for i := 0; i < 10; i++ {
		t = startSched.Next(t)
		if t.IsZero() || t.After(window) {
			break
		}
		startMinutes[t.Truncate(time.Minute)] = true
	}

	t = now
	for i := 0; i < 10; i++ {
		t = stopSched.Next(t)
		if t.IsZero() || t.After(window) {
			break
		}
		key := t.Truncate(time.Minute)
		if startMinutes[key] {
			return fmt.Errorf("schedule_start and schedule_stop fire at the same time (%s)",
				key.Format("Mon 02 Jan 15:04"))
		}
	}
	return nil
}

// IsInScheduleWindow reports whether now falls within an active schedule window.
// Returns (true, zero) when no schedule is configured or only one direction is set.
// Returns (false, nextStart) when both schedules are set and we are outside the window.
func IsInScheduleWindow(cfg *ContainerConfig, now time.Time) (allowed bool, nextStart time.Time) {
	// Stub — full implementation in Task 3.
	return true, time.Time{}
}

// prevFiring returns the most recent time the schedule fired at or before now,
// using a 7-day lookback window. Returns (zero, false) if no firing found.
func prevFiring(schedule cron.Schedule, now time.Time) (time.Time, bool) {
	lookback := now.Add(-7 * 24 * time.Hour)
	t := schedule.Next(lookback)
	if t.IsZero() || t.After(now) {
		return time.Time{}, false
	}
	for {
		next := schedule.Next(t)
		if next.IsZero() || next.After(now) {
			return t, true
		}
		t = next
	}
}
```

- [ ] **Step 7: Run test to verify it now passes**

```bash
go test -mod=vendor ./gateway/ -run TestScheduleConfigValidation -v
```

Expected: PASS.

- [ ] **Step 8: Add Docker label parsing in `gateway/docker.go`**

After the `dag.depends_on` block (around line 142), add:

```go
		if val, ok := c.Labels["dag.schedule_start"]; ok && val != "" {
			cfg.ScheduleStart = val
		}
		if val, ok := c.Labels["dag.schedule_stop"]; ok && val != "" {
			cfg.ScheduleStop = val
		}
```

- [ ] **Step 9: Verify build**

```bash
go build -mod=vendor -o /dev/null .
go test -mod=vendor ./gateway/...
```

Expected: all tests pass.

- [ ] **Step 10: Commit**

```bash
git add gateway/config.go gateway/config_test.go gateway/docker.go gateway/scheduler.go
git commit -m "feat: add ScheduleStart/ScheduleStop config fields and Docker labels"
```

---

## Task 3: Implement `IsInScheduleWindow` and full scheduler tests

**Files:**
- Modify: `gateway/scheduler.go`
- Create: `gateway/scheduler_test.go`

- [ ] **Step 1: Write the failing tests in `gateway/scheduler_test.go`**

```go
package gateway

import (
	"testing"
	"time"
)

// ─── validateScheduleCompatibility ───────────────────────────────────────────

func TestScheduleCompatibility(t *testing.T) {
	tests := []struct {
		name    string
		start   string
		stop    string
		wantErr bool
	}{
		{"both empty", "", "", false},
		{"only start valid", "0 8 * * 1-5", "", false},
		{"only stop valid", "", "0 18 * * 1-5", false},
		{"both valid daily", "0 8 * * *", "0 20 * * *", false},
		{"both valid overnight", "0 22 * * *", "0 6 * * *", false},
		{"same minute conflict", "0 8 * * *", "0 8 * * *", true},
		{"invalid start", "not-a-cron", "0 8 * * *", true},
		{"invalid stop", "0 8 * * *", "not-a-cron", true},
		{"both invalid", "bad", "bad", true},
		{"zero-minute split valid", "0 0 * * *", "30 0 * * *", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateScheduleCompatibility(tt.start, tt.stop)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateScheduleCompatibility(%q, %q) error = %v, wantErr %v",
					tt.start, tt.stop, err, tt.wantErr)
			}
		})
	}
}

// ─── IsInScheduleWindow ───────────────────────────────────────────────────────

func TestIsInScheduleWindow(t *testing.T) {
	// Reference point: Monday 2026-04-13 10:00:00 UTC
	// schedule_start: "0 8 * * 1-5" → fires at 08:00 Mon-Fri
	// schedule_stop:  "0 18 * * 1-5" → fires at 18:00 Mon-Fri
	mon10am := time.Date(2026, 4, 13, 10, 0, 0, 0, time.UTC) // inside window
	mon20pm := time.Date(2026, 4, 13, 20, 0, 0, 0, time.UTC) // outside window (after stop)
	mon07am := time.Date(2026, 4, 13, 7, 0, 0, 0, time.UTC)  // outside window (before start)
	tue08am := time.Date(2026, 4, 14, 8, 0, 0, 0, time.UTC)  // exactly on start boundary

	weekdayStart := "0 8 * * 1-5"
	weekdayStop := "0 18 * * 1-5"

	tests := []struct {
		name          string
		cfg           ContainerConfig
		now           time.Time
		wantAllowed   bool
		wantNextStart bool // true = nextStart should be non-zero
	}{
		{
			name:        "no schedule always allowed",
			cfg:         ContainerConfig{},
			now:         mon10am,
			wantAllowed: true,
		},
		{
			name:        "only start schedule always allowed",
			cfg:         ContainerConfig{ScheduleStart: weekdayStart},
			now:         mon10am,
			wantAllowed: true,
		},
		{
			name:        "only stop schedule always allowed",
			cfg:         ContainerConfig{ScheduleStop: weekdayStop},
			now:         mon10am,
			wantAllowed: true,
		},
		{
			name:          "both: inside window (10am)",
			cfg:           ContainerConfig{ScheduleStart: weekdayStart, ScheduleStop: weekdayStop},
			now:           mon10am,
			wantAllowed:   true,
			wantNextStart: false,
		},
		{
			name:          "both: outside window after stop (8pm)",
			cfg:           ContainerConfig{ScheduleStart: weekdayStart, ScheduleStop: weekdayStop},
			now:           mon20pm,
			wantAllowed:   false,
			wantNextStart: true,
		},
		{
			name:          "both: outside window before start (7am Monday)",
			cfg:           ContainerConfig{ScheduleStart: weekdayStart, ScheduleStop: weekdayStop},
			now:           mon07am,
			wantAllowed:   false,
			wantNextStart: true,
		},
		{
			name:          "exactly on start boundary is inside window",
			cfg:           ContainerConfig{ScheduleStart: weekdayStart, ScheduleStop: weekdayStop},
			now:           tue08am,
			wantAllowed:   true,
			wantNextStart: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			allowed, nextStart := IsInScheduleWindow(&tt.cfg, tt.now)
			if allowed != tt.wantAllowed {
				t.Errorf("IsInScheduleWindow() allowed = %v, want %v", allowed, tt.wantAllowed)
			}
			if tt.wantNextStart && nextStart.IsZero() {
				t.Error("expected non-zero nextStart when outside window")
			}
			if !tt.wantNextStart && !nextStart.IsZero() {
				t.Errorf("expected zero nextStart when inside/no window, got %v", nextStart)
			}
		})
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test -mod=vendor ./gateway/ -run "TestScheduleCompatibility|TestIsInScheduleWindow" -v
```

Expected: `TestIsInScheduleWindow` cases for "outside window" fail because `IsInScheduleWindow` stub always returns `true`.

- [ ] **Step 3: Implement `IsInScheduleWindow` in `gateway/scheduler.go`**

Replace the `IsInScheduleWindow` stub with:

```go
func IsInScheduleWindow(cfg *ContainerConfig, now time.Time) (allowed bool, nextStart time.Time) {
	if cfg.ScheduleStart == "" || cfg.ScheduleStop == "" {
		return true, time.Time{}
	}

	startSched, err1 := cron.ParseStandard(cfg.ScheduleStart)
	stopSched, err2 := cron.ParseStandard(cfg.ScheduleStop)
	if err1 != nil || err2 != nil {
		// Invalid expressions — don't block access.
		return true, time.Time{}
	}

	prevStart, hasStart := prevFiring(startSched, now)
	prevStop, hasStop := prevFiring(stopSched, now)

	if !hasStart {
		// No start has fired yet — before the first scheduled start.
		return false, startSched.Next(now)
	}
	if !hasStop {
		// Start fired but stop hasn't yet — we're in the window.
		return true, time.Time{}
	}
	if prevStart.After(prevStop) {
		return true, time.Time{}
	}
	return false, startSched.Next(now)
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test -mod=vendor ./gateway/ -run "TestScheduleCompatibility|TestIsInScheduleWindow" -v
```

Expected: all cases PASS.

- [ ] **Step 5: Commit**

```bash
git add gateway/scheduler.go gateway/scheduler_test.go
git commit -m "feat: implement IsInScheduleWindow and schedule compatibility validation"
```

---

## Task 4: Implement ScheduleManager

**Files:**
- Modify: `gateway/scheduler.go`
- Modify: `gateway/scheduler_test.go`

- [ ] **Step 1: Write the failing test — append to `gateway/scheduler_test.go`**

```go
// ─── ScheduleManager ─────────────────────────────────────────────────────────

func TestScheduleManagerSync(t *testing.T) {
	sm := NewScheduleManager(nil, nil)
	// Do NOT start the cron — we only test entry registration, not execution.

	containers := []ContainerConfig{
		{Name: "app", ScheduleStart: "0 8 * * *", ScheduleStop: "0 20 * * *", StartTimeout: 60 * time.Second},
		{Name: "db", ScheduleStart: "0 7 * * *", StartTimeout: 30 * time.Second},
		{Name: "cache"}, // no schedule
	}

	t.Run("initial sync registers correct entries", func(t *testing.T) {
		sm.Sync(containers)

		// app has start+stop = 2 cron entries; db has start only = 1 entry; cache = 0
		if got := len(sm.cron.Entries()); got != 3 {
			t.Errorf("expected 3 cron entries, got %d", got)
		}
		if _, ok := sm.entries["app"]; !ok {
			t.Error("expected entries for 'app'")
		}
		if len(sm.entries["app"]) != 2 {
			t.Errorf("expected 2 entries for 'app', got %d", len(sm.entries["app"]))
		}
		if _, ok := sm.entries["db"]; !ok {
			t.Error("expected entry for 'db'")
		}
		if len(sm.entries["db"]) != 1 {
			t.Errorf("expected 1 entry for 'db', got %d", len(sm.entries["db"]))
		}
		if _, ok := sm.entries["cache"]; ok {
			t.Error("unexpected entry for 'cache' (no schedule configured)")
		}
	})

	t.Run("re-sync with updated schedule removes and re-adds entries", func(t *testing.T) {
		updated := []ContainerConfig{
			{Name: "app", ScheduleStart: "0 9 * * *", ScheduleStop: "0 21 * * *", StartTimeout: 60 * time.Second},
		}
		sm.Sync(updated)

		if got := len(sm.cron.Entries()); got != 2 {
			t.Errorf("after re-sync expected 2 cron entries, got %d", got)
		}
		if _, ok := sm.entries["db"]; ok {
			t.Error("expected 'db' entry removed after re-sync")
		}
	})

	t.Run("sync with nil removes all entries", func(t *testing.T) {
		sm.Sync(nil)

		if got := len(sm.cron.Entries()); got != 0 {
			t.Errorf("after empty sync expected 0 cron entries, got %d", got)
		}
		if len(sm.entries) != 0 {
			t.Errorf("after empty sync expected empty entries map, got %d keys", len(sm.entries))
		}
	})
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test -mod=vendor ./gateway/ -run TestScheduleManagerSync -v
```

Expected: FAIL — `NewScheduleManager` not defined.

- [ ] **Step 3: Implement `ScheduleManager` — append to `gateway/scheduler.go`**

Add these imports to the existing import block (update `gateway/scheduler.go` top):

```go
package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
)
```

Then append the `ScheduleManager` type and methods after the existing functions:

```go
// ScheduleManager registers and executes per-container cron start/stop jobs.
// Call Sync on startup and on every config hot-reload.
type ScheduleManager struct {
	cron    *cron.Cron
	client  *DockerClient
	manager *ContainerManager

	mu      sync.Mutex
	entries map[string][]cron.EntryID // containerName → registered entry IDs
}

// NewScheduleManager creates a ScheduleManager. Call Start to begin execution.
func NewScheduleManager(client *DockerClient, manager *ContainerManager) *ScheduleManager {
	return &ScheduleManager{
		cron:    cron.New(),
		client:  client,
		manager: manager,
		entries: make(map[string][]cron.EntryID),
	}
}

// Start begins executing registered cron jobs. Stops when ctx is cancelled.
func (sm *ScheduleManager) Start(ctx context.Context) {
	sm.cron.Start()
	go func() {
		<-ctx.Done()
		sm.cron.Stop()
	}()
}

// Sync diffs the registered cron entries against the provided container list.
// It removes all existing entries and re-registers from scratch, making it
// safe to call repeatedly on config hot-reloads.
func (sm *ScheduleManager) Sync(containers []ContainerConfig) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Remove all existing entries.
	for name, ids := range sm.entries {
		for _, id := range ids {
			sm.cron.Remove(id)
		}
		delete(sm.entries, name)
	}

	// Register entries for containers that have at least one schedule field.
	for _, c := range containers {
		if c.ScheduleStart == "" && c.ScheduleStop == "" {
			continue
		}
		cfg := c // capture loop variable for closures
		var ids []cron.EntryID

		if cfg.ScheduleStart != "" {
			id, err := sm.cron.AddFunc(cfg.ScheduleStart, func() {
				ctx, cancel := context.WithTimeout(context.Background(), cfg.StartTimeout)
				defer cancel()
				sm.manager.InitStartState(cfg.Name)
				if err := sm.manager.EnsureRunning(ctx, &cfg); err != nil {
					slog.Error("scheduled start failed", "container", cfg.Name, "error", err)
				} else {
					slog.Info("scheduled start succeeded", "container", cfg.Name)
				}
			})
			if err != nil {
				slog.Error("failed to register schedule_start", "container", cfg.Name, "error", err)
				continue
			}
			ids = append(ids, id)
		}

		if cfg.ScheduleStop != "" {
			id, err := sm.cron.AddFunc(cfg.ScheduleStop, func() {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				if err := sm.client.StopContainer(ctx, cfg.Name); err != nil {
					slog.Error("scheduled stop failed", "container", cfg.Name, "error", err)
				} else {
					slog.Info("scheduled stop succeeded", "container", cfg.Name)
				}
			})
			if err != nil {
				slog.Error("failed to register schedule_stop", "container", cfg.Name, "error", err)
				continue
			}
			ids = append(ids, id)
		}

		if len(ids) > 0 {
			sm.entries[cfg.Name] = ids
		}
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test -mod=vendor ./gateway/ -run TestScheduleManagerSync -v
```

Expected: all subtests PASS.

- [ ] **Step 5: Run full test suite**

```bash
go test -mod=vendor ./gateway/...
```

Expected: all tests pass.

- [ ] **Step 6: Commit**

```bash
git add gateway/scheduler.go gateway/scheduler_test.go
git commit -m "feat: implement ScheduleManager with cron-based start/stop jobs"
```

---

## Task 5: Create `scheduled.html` template

**Files:**
- Create: `gateway/templates/scheduled.html`

- [ ] **Step 1: Create the template**

Create `gateway/templates/scheduled.html`:

```html
<!DOCTYPE html>
<html lang="en">

<head>
  <meta charset="utf-8" />
  <meta content="width=device-width, initial-scale=1.0" name="viewport" />
  <title>Offline — {{ .ContainerName }}</title>
  <link href="https://fonts.googleapis.com" rel="preconnect" />
  <link crossorigin="" href="https://fonts.gstatic.com" rel="preconnect" />
  <link
    href="https://fonts.googleapis.com/css2?family=JetBrains+Mono:wght@400;500&family=Space+Grotesk:wght@300;400;500;600;700&display=swap"
    rel="stylesheet" />
  <script src="https://cdn.tailwindcss.com?plugins=forms,container-queries"></script>
  <script>
    tailwind.config = {
      theme: {
        extend: {
          colors: {
            "bg-dark": "#0d1117",
            "surface-dark": "#161b22",
            "text-main": "#c9d1d9",
            "text-muted": "#8b949e",
            "border-dark": "#30363d",
            "offline-red": "#f85149",
            "offline-surface": "#1a0d0d",
          },
          fontFamily: {
            "display": ["Space Grotesk", "sans-serif"],
            "mono": ["JetBrains Mono", "monospace"],
          },
          backgroundImage: {
            'technical-grid': "linear-gradient(to right, rgba(255,255,255,0.05) 1px, transparent 1px), linear-gradient(to bottom, rgba(255,255,255,0.05) 1px, transparent 1px)",
          },
        },
      },
    }
  </script>
  <style>
    .bg-grid-pattern { background-size: 20px 20px; }
  </style>
</head>

<body
  class="bg-bg-dark text-text-main font-display min-h-screen flex flex-col overflow-hidden relative selection:bg-offline-red selection:text-white antialiased">
  <div class="absolute inset-0 bg-technical-grid bg-grid-pattern z-0 pointer-events-none"></div>

  <div class="relative z-10 flex flex-col items-center justify-center flex-grow w-full h-full p-4 sm:p-6">
    <div class="w-full max-w-[600px] bg-surface-dark border border-border-dark rounded shadow-lg overflow-hidden">

      {{/* ── Header ─────────────────────────────────────────── */}}
      <div class="px-6 py-5 border-b border-border-dark flex items-center justify-between">
        <div class="inline-flex items-center gap-2 px-3 py-1 rounded-full bg-bg-dark border border-offline-red/40">
          <span class="relative flex h-2 w-2">
            <span class="relative inline-flex rounded-full h-2 w-2 bg-offline-red"></span>
          </span>
          <span class="font-mono text-xs font-medium tracking-wide text-offline-red">STATUS: OFFLINE</span>
        </div>
        <div class="hidden sm:flex gap-1.5 opacity-20">
          <div class="w-2.5 h-2.5 rounded-full bg-text-main"></div>
          <div class="w-2.5 h-2.5 rounded-full bg-text-main"></div>
          <div class="w-2.5 h-2.5 rounded-full bg-text-main"></div>
        </div>
      </div>

      {{/* ── Main area ───────────────────────────────────────── */}}
      <div class="p-8 sm:p-10 flex flex-col items-center text-center">

        {{/* Container icon — static, red-tinted */}}
        <div class="mb-8 relative">
          <div
            class="w-16 h-16 border-2 border-offline-red/50 flex flex-wrap content-center justify-center gap-1 p-1 bg-surface-dark relative z-10">
            <div class="w-1 h-1 bg-offline-red/60 rounded-[1px]"></div>
            <div class="w-1 h-1 bg-offline-red/40 rounded-[1px]"></div>
            <div class="w-1 h-1 bg-offline-red/60 rounded-[1px]"></div>
            <div class="w-1 h-1 bg-offline-red/40 rounded-[1px]"></div>
            <div class="w-1 h-1 bg-offline-red/60 rounded-[1px]"></div>
            <div class="w-1 h-1 bg-offline-red/40 rounded-[1px]"></div>
          </div>
          <div
            class="absolute top-1/2 left-1/2 -translate-x-1/2 -translate-y-1/2 w-[1px] h-[160%] bg-border-dark -z-0">
          </div>
          <div
            class="absolute top-1/2 left-1/2 -translate-x-1/2 -translate-y-1/2 w-[160%] h-[1px] bg-border-dark -z-0">
          </div>
        </div>

        <h1 class="text-xl sm:text-2xl font-semibold leading-tight mb-2 text-text-main tracking-tight">
          Container <span class="font-mono text-offline-red text-lg sm:text-xl">[{{ .ContainerName }}]</span> is offline
        </h1>
        <p class="font-mono text-xs sm:text-sm text-text-muted mb-6">
          This service is not available right now
        </p>

        {{/* Offline info box */}}
        <div class="w-full bg-offline-surface border border-offline-red/30 rounded-sm p-5 text-left">
          <div class="flex items-center gap-2 mb-3">
            <svg class="w-4 h-4 text-offline-red flex-shrink-0" fill="none" stroke="currentColor" viewBox="0 0 24 24">
              <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2"
                d="M12 8v4l3 3m6-3a9 9 0 11-18 0 9 9 0 0118 0z" />
            </svg>
            <span class="font-mono text-xs font-bold text-offline-red uppercase tracking-wider">Scheduled Downtime</span>
          </div>
          {{ if .NextStart }}
          <p class="font-mono text-xs text-text-muted">
            Next scheduled start:
            <span class="text-text-main font-medium">{{ .NextStart }}</span>
          </p>
          {{ else }}
          <p class="font-mono text-xs text-text-muted">No upcoming start scheduled.</p>
          {{ end }}
        </div>

      </div>
    </div>
  </div>

  {{/* Footer */}}
  <div class="fixed bottom-0 left-0 w-full py-4 px-6 text-center sm:text-left bg-transparent z-20 pointer-events-none">
    <p class="font-mono text-[10px] text-text-muted opacity-50 uppercase tracking-wider">
      docker awakening gateway <span class="mx-2 text-border-dark">|</span> access blocked by schedule
    </p>
  </div>

</body>
</html>
```

- [ ] **Step 2: Verify it compiles (templates are embedded at build time)**

```bash
go build -mod=vendor -o /dev/null .
```

Expected: no errors.

- [ ] **Step 3: Commit**

```bash
git add gateway/templates/scheduled.html
git commit -m "feat: add scheduled.html offline page template"
```

---

## Task 6: Wire schedule check into server and main

**Files:**
- Modify: `gateway/server.go`
- Modify: `main.go`

- [ ] **Step 1: Add `scheduledData` struct and `serveScheduledPage` to `gateway/server.go`**

After the `errorData` struct (around line 620), add:

```go
type scheduledData struct {
	ContainerName string
	NextStart     string // e.g. "Tue 14 Apr · 08:00" or empty
}
```

After `serveErrorPage`, add:

```go
func (s *Server) serveScheduledPage(w http.ResponseWriter, r *http.Request, cfg *ContainerConfig, nextStart time.Time) {
	next := ""
	if !nextStart.IsZero() {
		next = nextStart.UTC().Format("Mon 02 Jan · 15:04")
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
```

- [ ] **Step 2: Add `scheduler` field to `Server` struct in `gateway/server.go`**

In the `Server` struct, after `groupRouter *GroupRouter`:

```go
	scheduler    *ScheduleManager
```

- [ ] **Step 3: Update `NewServer` signature and body in `gateway/server.go`**

Change `NewServer` signature from:

```go
func NewServer(manager *ContainerManager, cfg *GatewayConfig) (*Server, error) {
```

to:

```go
func NewServer(manager *ContainerManager, scheduler *ScheduleManager, cfg *GatewayConfig) (*Server, error) {
```

In the `return &Server{...}` block, add:

```go
		scheduler:    scheduler,
```

- [ ] **Step 4: Add schedule check in `handleRequest` in `gateway/server.go`**

In `handleRequest`, after `cfg := s.resolveConfig(r)` / `if cfg == nil` block and before the `start := time.Now()` line, add:

```go
	// Schedule gate: block access outside the configured cron window.
	if allowed, nextStart := IsInScheduleWindow(cfg, time.Now()); !allowed {
		s.serveScheduledPage(w, r, cfg, nextStart)
		return
	}
```

- [ ] **Step 5: Call `Sync` in `ReloadConfig` in `gateway/server.go`**

In `ReloadConfig`, after updating `s.trustedCIDRs`:

```go
	s.scheduler.Sync(newCfg.Containers)
```

- [ ] **Step 6: Update `main.go` to construct and wire `ScheduleManager`**

In `main.go`, after `manager := gateway.NewContainerManager(dockerClient)`, add:

```go
	// Initialize Cron Scheduler
	scheduler := gateway.NewScheduleManager(dockerClient, manager)
```

Change:

```go
	server, err := gateway.NewServer(manager, cfg)
```

to:

```go
	server, err := gateway.NewServer(manager, scheduler, cfg)
```

After `discoveryManager.Start(...)` and before the signal handler, add:

```go
	// Start scheduler and register initial jobs.
	scheduler.Start(ctx)
	scheduler.Sync(cfg.Containers)
	slog.Info("scheduler started")
```

- [ ] **Step 7: Build and run all tests**

```bash
go build -mod=vendor -o /dev/null .
go test -mod=vendor ./gateway/...
```

Expected: build and all tests pass.

- [ ] **Step 8: Commit**

```bash
git add gateway/server.go main.go
git commit -m "feat: wire ScheduleManager into server — schedule gate in handleRequest"
```

---

## Task 7: Idle timeout countdown in `/_status`

**Files:**
- Modify: `gateway/server.go`
- Modify: `gateway/manager_test.go`
- Modify: `gateway/templates/status.html`

- [ ] **Step 1: Write the failing test in `gateway/manager_test.go`**

Append after `TestStartState_ConcurrentAccess`:

```go
// ─── calcIdleRemaining ────────────────────────────────────────────────────────

func TestCalcIdleRemaining(t *testing.T) {
	now := time.Date(2026, 4, 13, 10, 0, 0, 0, time.UTC)

	tests := []struct {
		name        string
		idleTimeout time.Duration
		lastSeen    time.Time
		hasSeen     bool
		want        int64
	}{
		{
			name:        "no idle timeout returns 0",
			idleTimeout: 0,
			lastSeen:    now.Add(-5 * time.Minute),
			hasSeen:     true,
			want:        0,
		},
		{
			name:        "never seen returns -1",
			idleTimeout: 30 * time.Minute,
			hasSeen:     false,
			want:        -1,
		},
		{
			name:        "recent activity returns positive remaining",
			idleTimeout: 30 * time.Minute,
			lastSeen:    now.Add(-5 * time.Minute),
			hasSeen:     true,
			want:        25 * 60, // 25 minutes in seconds
		},
		{
			name:        "just expired clamps to zero",
			idleTimeout: 30 * time.Minute,
			lastSeen:    now.Add(-35 * time.Minute),
			hasSeen:     true,
			want:        0,
		},
		{
			name:        "exactly at boundary",
			idleTimeout: 10 * time.Minute,
			lastSeen:    now.Add(-10 * time.Minute),
			hasSeen:     true,
			want:        0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := calcIdleRemaining(tt.idleTimeout, tt.lastSeen, tt.hasSeen, now)
			if got != tt.want {
				t.Errorf("calcIdleRemaining() = %d, want %d", got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test -mod=vendor ./gateway/ -run TestCalcIdleRemaining -v
```

Expected: FAIL — `calcIdleRemaining` not defined.

- [ ] **Step 3: Add `calcIdleRemaining` function in `gateway/server.go`**

After the `evictStale` method (end of rate limiter section), add:

```go
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
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test -mod=vendor ./gateway/ -run TestCalcIdleRemaining -v
```

Expected: PASS.

- [ ] **Step 5: Add new fields to `statusContainerJSON` in `gateway/server.go`**

In `statusContainerJSON`, add after `LastRequest`:

```go
	IdleTimeoutSec   int64 `json:"idle_timeout_sec"`
	IdleRemainingSec int64 `json:"idle_remaining_sec"`
```

- [ ] **Step 6: Populate the new fields in `handleStatusAPI` in `gateway/server.go`**

In `handleStatusAPI`, inside the container loop after the `entry.LastRequest` block:

```go
		// Idle timeout countdown fields.
		now := time.Now()
		lastSeen, hasSeen := s.manager.GetLastSeen(c.Name)
		entry.IdleTimeoutSec = int64(c.IdleTimeout.Seconds())
		entry.IdleRemainingSec = calcIdleRemaining(c.IdleTimeout, lastSeen, hasSeen, now)
```

- [ ] **Step 7: Run full test suite**

```bash
go test -mod=vendor ./gateway/...
```

Expected: all tests pass.

- [ ] **Step 8: Add idle countdown UI to `status.html`**

In the `renderCard` function in `gateway/templates/status.html`, find the Metrics grid section (the `grid-cols-3` div containing Uptime, Last Req, Idle T/O).

After that grid div (after the closing `</div>` of the metrics grid, before the footer `<div class="mt-auto...">`), add:

```javascript
// Idle countdown — inserted dynamically by renderCard
```

Actually, modify the `renderCard` function in the JS. After the `// Metrics` block and before `// Footer`, add this JS string:

```javascript
// Idle countdown bar (only for running containers with idle_timeout_sec > 0)
const idleBar = (c.status === 'running' && c.idle_timeout_sec > 0)
    ? (function() {
        const remaining = c.idle_remaining_sec;
        if (remaining < 0) {
            return '<div class="mb-4 font-mono text-[10px] dark:text-slate-500 text-slate-400">⏱ idle stop: no activity yet</div>';
        }
        const total = c.idle_timeout_sec;
        const pct = Math.max(0, Math.min(100, Math.round((remaining / total) * 100)));
        const barColor = pct > 40 ? 'bg-status-running' : pct > 20 ? 'bg-status-starting' : 'bg-status-error';
        const mins = Math.floor(remaining / 60);
        const secs = remaining % 60;
        const label = mins > 0 ? mins + 'm ' + secs + 's' : secs + 's';
        return '<div class="mb-4">'
            + '<div class="flex justify-between font-mono text-[10px] dark:text-slate-500 text-slate-400 mb-1">'
            + '<span>⏱ idle stop in ' + label + '</span>'
            + '<span>' + pct + '%</span>'
            + '</div>'
            + '<div class="w-full h-1 dark:bg-border-dark bg-slate-200 rounded-full overflow-hidden">'
            + '<div class="h-full ' + barColor + ' rounded-full transition-all duration-1000" style="width:' + pct + '%"></div>'
            + '</div>'
            + '</div>';
    })()
    : '';
```

Then insert `+ idleBar` into the returned card HTML string, between the metrics grid closing tag and the footer div:

Find this in the return statement:
```javascript
                + '</div>'
                // Footer
                + '<div class="mt-auto border-t dark:border-border-dark border-border-light pt-3 flex justify-between items-center">'
```

Change to:
```javascript
                + '</div>'
                + idleBar
                // Footer
                + '<div class="mt-auto border-t dark:border-border-dark border-border-light pt-3 flex justify-between items-center">'
```

- [ ] **Step 9: Build to verify template embedded correctly**

```bash
go build -mod=vendor -o /dev/null .
```

Expected: no errors.

- [ ] **Step 10: Run full test suite**

```bash
go test -mod=vendor ./gateway/...
```

Expected: all tests pass.

- [ ] **Step 11: Commit**

```bash
git add gateway/server.go gateway/manager_test.go gateway/templates/status.html
git commit -m "feat: add idle countdown to status dashboard API and UI"
```

---

## Self-Review

**Spec coverage check:**

| Spec requirement | Task |
|---|---|
| `ScheduleStart`/`ScheduleStop` fields in ContainerConfig | Task 2 |
| `dag.schedule_start`/`dag.schedule_stop` Docker labels | Task 2 |
| Validation: malformed expressions rejected | Task 2 |
| Validation: same-minute conflict detected | Task 2 |
| `IsInScheduleWindow` algorithm | Task 3 |
| `validateScheduleCompatibility` | Task 3 |
| `ScheduleManager` with Sync | Task 4 |
| `TestScheduleCompatibility` | Task 3 |
| `TestIsInScheduleWindow` | Task 3 |
| `TestScheduleManagerSync` | Task 4 |
| `scheduled.html` template (503, red, nextStart) | Task 5 |
| Schedule gate in `handleRequest` | Task 6 |
| `serveScheduledPage` | Task 6 |
| `ScheduleManager` wired in main.go | Task 6 |
| `ReloadConfig` calls `Sync` | Task 6 |
| `idle_timeout_sec`/`idle_remaining_sec` in status API | Task 7 |
| `calcIdleRemaining` function | Task 7 |
| `TestCalcIdleRemaining` | Task 7 |
| Idle countdown bar in `status.html` | Task 7 |

All requirements covered. No placeholders. Type names consistent across tasks.
