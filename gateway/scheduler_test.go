package gateway

import (
	"testing"
	"time"
)


// ─── cronExpr ────────────────────────────────────────────────────────────────

func TestCronExpr(t *testing.T) {
	tests := []struct {
		expr   string
		tzName string
		want   string
	}{
		{"0 8 * * *", "", "0 8 * * *"},
		{"0 8 * * *", "Europe/Rome", "CRON_TZ=Europe/Rome 0 8 * * *"},
		{"", "Europe/Rome", ""},
	}
	for _, tt := range tests {
		got := cronExpr(tt.expr, tt.tzName)
		if got != tt.want {
			t.Errorf("cronExpr(%q, %q) = %q, want %q", tt.expr, tt.tzName, got, tt.want)
		}
	}
}

// ─── cronExprFromLoc ─────────────────────────────────────────────────────────

func TestCronExprFromLoc(t *testing.T) {
	rome, err := time.LoadLocation("Europe/Rome")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		expr string
		loc  *time.Location
		want string
	}{
		{"0 8 * * *", nil, "0 8 * * *"},
		{"0 8 * * *", time.Local, "0 8 * * *"},
		{"0 8 * * *", rome, "CRON_TZ=Europe/Rome 0 8 * * *"},
		{"", rome, ""},
	}
	for _, tt := range tests {
		got := cronExprFromLoc(tt.expr, tt.loc)
		if got != tt.want {
			t.Errorf("cronExprFromLoc(%q, %v) = %q, want %q", tt.expr, tt.loc, got, tt.want)
		}
	}
}

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
			err := validateScheduleCompatibility(tt.start, tt.stop, time.Local)
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
			allowed, nextStart := IsInScheduleWindow(&tt.cfg, tt.now, time.Local)
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

func TestIsInScheduleWindowWithTimezone(t *testing.T) {
	// 10:10 Rome time (CEST = UTC+2) = 08:10 UTC
	// stop:  "0 8 * * *" Rome  → 08:00 Rome = 06:00 UTC — fired 2h10m ago UTC
	// start: "0 11 * * *" Rome → 11:00 Rome = 09:00 UTC — not yet fired
	rome, err := time.LoadLocation("Europe/Rome")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 4, 9, 10, 10, 0, 0, rome) // 10:10 Rome

	cfg := ContainerConfig{
		ScheduleStart: "0 11 * * *",
		ScheduleStop:  "0 8 * * *",
	}

	t.Run("outside window with Rome timezone", func(t *testing.T) {
		allowed, nextStart := IsInScheduleWindow(&cfg, now, rome)
		if allowed {
			t.Error("expected blocked outside window, got allowed")
		}
		if nextStart.IsZero() {
			t.Error("expected non-zero nextStart")
		}
	})

	t.Run("inside window with Rome timezone (12:00 Rome)", func(t *testing.T) {
		noon := time.Date(2026, 4, 9, 12, 0, 0, 0, rome)
		allowed, _ := IsInScheduleWindow(&cfg, noon, rome)
		if !allowed {
			t.Error("expected allowed inside window at noon Rome time")
		}
	})
}

// ─── ScheduleManager ─────────────────────────────────────────────────────────

func TestScheduleManagerSync(t *testing.T) {
	sm := NewScheduleManager(nil, nil)
	// Do NOT call Start — we only test entry registration, not execution.

	t.Run("initial sync registers correct entries", func(t *testing.T) {
		containers := []ContainerConfig{
			{Name: "app", ScheduleStart: "0 8 * * *", ScheduleStop: "0 20 * * *", StartTimeout: 60 * time.Second},
			{Name: "db", ScheduleStart: "0 7 * * *", StartTimeout: 30 * time.Second},
			{Name: "cache"}, // no schedule
		}
		sm.Sync(containers, time.Local)

		// app has start+stop = 2 entries; db has start only = 1; cache = 0
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
		sm.Sync(updated, time.Local)

		if got := len(sm.cron.Entries()); got != 2 {
			t.Errorf("after re-sync expected 2 cron entries, got %d", got)
		}
		if _, ok := sm.entries["db"]; ok {
			t.Error("expected 'db' entry removed after re-sync")
		}
	})

	t.Run("sync with nil removes all entries", func(t *testing.T) {
		sm.Sync(nil, time.Local)

		if got := len(sm.cron.Entries()); got != 0 {
			t.Errorf("after empty sync expected 0 cron entries, got %d", got)
		}
		if len(sm.entries) != 0 {
			t.Errorf("after empty sync expected empty entries map, got %d keys", len(sm.entries))
		}
	})
}

func TestScheduleManagerSyncWithTimezone(t *testing.T) {
	sm := NewScheduleManager(nil, nil)

	rome, err := time.LoadLocation("Europe/Rome")
	if err != nil {
		t.Fatal(err)
	}
	containers := []ContainerConfig{
		{Name: "app", ScheduleStart: "0 8 * * *", ScheduleStop: "0 20 * * *", StartTimeout: 60 * time.Second},
	}
	sm.Sync(containers, rome)

	entries := sm.cron.Entries()
	if len(entries) != 2 {
		t.Fatalf("expected 2 cron entries, got %d", len(entries))
	}
	// Verify the start entry fires at 08:00 Rome time by checking Next()
	ref := time.Date(2026, 4, 9, 7, 59, 0, 0, rome) // just before 08:00 Rome
	next := entries[0].Schedule.Next(ref)
	want := time.Date(2026, 4, 9, 8, 0, 0, 0, rome)
	if !next.Equal(want) {
		t.Errorf("next firing = %v, want %v", next, want)
	}
}
