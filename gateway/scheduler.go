package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
)

// cronExpr prepends a CRON_TZ=<tzName> prefix to expr when tzName is non-empty.
// robfig/cron v3 natively parses this prefix to set the schedule's timezone.
// Returns expr unchanged when tzName is empty or expr is empty.
func cronExpr(expr, tzName string) string {
	if tzName == "" || expr == "" {
		return expr
	}
	return "CRON_TZ=" + tzName + " " + expr
}

// cronExprFromLoc prepends CRON_TZ=<tz> to expr using the location's name.
// Returns expr unchanged when loc is nil, time.Local, or expr is empty.
// time.Local is skipped because "Local" is not a valid IANA name for CRON_TZ.
func cronExprFromLoc(expr string, loc *time.Location) string {
	if expr == "" || loc == nil || loc == time.Local {
		return expr
	}
	return "CRON_TZ=" + loc.String() + " " + expr
}

// validateScheduleCompatibility returns an error if the two cron expressions
// are malformed or would fire at the same minute within the next 7 days.
func validateScheduleCompatibility(startExpr, stopExpr string, loc *time.Location) error {
	var startSched, stopSched cron.Schedule
	var err error

	if startExpr != "" {
		startSched, err = cron.ParseStandard(cronExprFromLoc(startExpr, loc))
		if err != nil {
			return fmt.Errorf("schedule_start: invalid cron expression %q: %w", startExpr, err)
		}
	}
	if stopExpr != "" {
		stopSched, err = cron.ParseStandard(cronExprFromLoc(stopExpr, loc))
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
	for {
		t = startSched.Next(t)
		if t.IsZero() || t.After(window) {
			break
		}
		startMinutes[t.Truncate(time.Minute)] = true
	}

	t = now
	for {
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
func IsInScheduleWindow(cfg *ContainerConfig, now time.Time, loc *time.Location) (allowed bool, nextStart time.Time) {
	if cfg.ScheduleStart == "" || cfg.ScheduleStop == "" {
		return true, time.Time{}
	}

	startSched, err1 := cron.ParseStandard(cronExprFromLoc(cfg.ScheduleStart, loc))
	stopSched, err2 := cron.ParseStandard(cronExprFromLoc(cfg.ScheduleStop, loc))
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
// loc is the global timezone; individual containers may override it via ScheduleTimezone.
func (sm *ScheduleManager) Sync(containers []ContainerConfig, loc *time.Location) {
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

		// Determine effective timezone: per-container overrides global.
		effectiveLoc := loc
		if cfg.ScheduleTimezone != "" {
			if l, err := resolveLocation(cfg.ScheduleTimezone); err == nil {
				effectiveLoc = l
			}
		}

		var ids []cron.EntryID

		if cfg.ScheduleStart != "" {
			id, err := sm.cron.AddFunc(cronExprFromLoc(cfg.ScheduleStart, effectiveLoc), func() {
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
			id, err := sm.cron.AddFunc(cronExprFromLoc(cfg.ScheduleStop, effectiveLoc), func() {
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
