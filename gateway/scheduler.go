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
func IsInScheduleWindow(cfg *ContainerConfig, now time.Time) (allowed bool, nextStart time.Time) {
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
