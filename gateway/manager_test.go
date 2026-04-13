package gateway

import (
	"sync"
	"testing"
	"time"
)

// ─── Start State Lifecycle ────────────────────────────────────────────────────

func TestStartStateLifecycle(t *testing.T) {
	m := NewContainerManager(nil) // no Docker client needed for state tests

	t.Run("unknown container returns unknown", func(t *testing.T) {
		status, errMsg := m.GetStartState("nonexistent")
		if status != "unknown" {
			t.Errorf("status = %q, want %q", status, "unknown")
		}
		if errMsg != "" {
			t.Errorf("errMsg = %q, want empty", errMsg)
		}
	})

	t.Run("InitStartState sets starting", func(t *testing.T) {
		m.InitStartState("c1")
		status, errMsg := m.GetStartState("c1")
		if status != "starting" {
			t.Errorf("status = %q, want %q", status, "starting")
		}
		if errMsg != "" {
			t.Errorf("errMsg = %q, want empty", errMsg)
		}
	})

	t.Run("setStartState to running", func(t *testing.T) {
		m.setStartState("c1", statusRunning, "")
		status, errMsg := m.GetStartState("c1")
		if status != "running" {
			t.Errorf("status = %q, want %q", status, "running")
		}
		if errMsg != "" {
			t.Errorf("errMsg = %q, want empty", errMsg)
		}
	})

	t.Run("setStartState to failed with error", func(t *testing.T) {
		m.setStartState("c1", statusFailed, "container crashed")
		status, errMsg := m.GetStartState("c1")
		if status != "failed" {
			t.Errorf("status = %q, want %q", status, "failed")
		}
		if errMsg != "container crashed" {
			t.Errorf("errMsg = %q, want %q", errMsg, "container crashed")
		}
	})
}

// ─── RecordActivity & GetLastSeen ─────────────────────────────────────────────

func TestRecordActivity(t *testing.T) {
	m := NewContainerManager(nil)

	t.Run("unseen container returns false", func(t *testing.T) {
		_, ok := m.GetLastSeen("never-seen")
		if ok {
			t.Error("expected ok=false for unseen container")
		}
	})

	t.Run("recording activity makes it visible", func(t *testing.T) {
		before := time.Now()
		m.RecordActivity("my-app")
		after := time.Now()

		ts, ok := m.GetLastSeen("my-app")
		if !ok {
			t.Fatal("expected ok=true after RecordActivity")
		}
		if ts.Before(before) || ts.After(after) {
			t.Errorf("timestamp %v not in range [%v, %v]", ts, before, after)
		}
	})

	t.Run("subsequent activity updates timestamp", func(t *testing.T) {
		m.RecordActivity("my-app")
		first, _ := m.GetLastSeen("my-app")

		time.Sleep(10 * time.Millisecond)
		m.RecordActivity("my-app")
		second, _ := m.GetLastSeen("my-app")

		if !second.After(first) {
			t.Error("second timestamp should be after first")
		}
	})
}

// ─── getLock ──────────────────────────────────────────────────────────────────

func TestGetLock(t *testing.T) {
	m := NewContainerManager(nil)

	t.Run("same name returns same mutex", func(t *testing.T) {
		l1 := m.getLock("app")
		l2 := m.getLock("app")
		if l1 != l2 {
			t.Error("expected same mutex for same container name")
		}
	})

	t.Run("different names return different mutexes", func(t *testing.T) {
		l1 := m.getLock("app1")
		l2 := m.getLock("app2")
		if l1 == l2 {
			t.Error("expected different mutexes for different container names")
		}
	})

	t.Run("concurrent access is safe", func(t *testing.T) {
		var wg sync.WaitGroup
		for i := 0; i < 100; i++ {
			wg.Add(1)
			go func(name string) {
				defer wg.Done()
				_ = m.getLock(name)
			}("container-" + string(rune('a'+i%10)))
		}
		wg.Wait()
		// If we got here without a race detector panic, pass
	})
}

// ─── State management thread safety ──────────────────────────────────────────

func TestStartState_ConcurrentAccess(t *testing.T) {
	m := NewContainerManager(nil)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			m.InitStartState("c1")
		}()
		go func() {
			defer wg.Done()
			m.GetStartState("c1")
		}()
	}
	wg.Wait()
	// No race detector panic = pass
}

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

// ─── BuildReverseDeps ─────────────────────────────────────────────────────────

func TestBuildReverseDeps(t *testing.T) {
	t.Run("empty config returns empty map", func(t *testing.T) {
		got := BuildReverseDeps(nil)
		if len(got) != 0 {
			t.Errorf("expected empty map, got %v", got)
		}
	})

	t.Run("no dependencies returns empty map", func(t *testing.T) {
		cfgs := []ContainerConfig{
			{Name: "app", Host: "app.local"},
			{Name: "db"},
		}
		got := BuildReverseDeps(cfgs)
		if len(got) != 0 {
			t.Errorf("expected empty map, got %v", got)
		}
	})

	t.Run("linear chain A→B→C", func(t *testing.T) {
		cfgs := []ContainerConfig{
			{Name: "app", Host: "app.local", DependsOn: []string{"api"}},
			{Name: "api", DependsOn: []string{"db"}},
			{Name: "db"},
		}
		got := BuildReverseDeps(cfgs)
		if len(got["api"]) != 1 || got["api"][0] != "app" {
			t.Errorf("got[api] = %v, want [app]", got["api"])
		}
		if len(got["db"]) != 1 || got["db"][0] != "api" {
			t.Errorf("got[db] = %v, want [api]", got["db"])
		}
		if len(got["app"]) != 0 {
			t.Errorf("got[app] = %v, want []", got["app"])
		}
	})

	t.Run("diamond A→[B,C]→D: D has two dependents", func(t *testing.T) {
		cfgs := []ContainerConfig{
			{Name: "app", Host: "app.local", DependsOn: []string{"api", "worker"}},
			{Name: "api", DependsOn: []string{"db"}},
			{Name: "worker", DependsOn: []string{"db"}},
			{Name: "db"},
		}
		got := BuildReverseDeps(cfgs)
		dbDeps := got["db"]
		if len(dbDeps) != 2 {
			t.Fatalf("got[db] length = %d, want 2; got %v", len(dbDeps), dbDeps)
		}
		found := map[string]bool{"api": false, "worker": false}
		for _, d := range dbDeps {
			found[d] = true
		}
		if !found["api"] || !found["worker"] {
			t.Errorf("got[db] = %v, want [api worker] (any order)", dbDeps)
		}
	})

	t.Run("shared dep: A→D and B→D produces D:[A,B]", func(t *testing.T) {
		cfgs := []ContainerConfig{
			{Name: "app", Host: "app.local", DependsOn: []string{"db"}},
			{Name: "other", Host: "other.local", DependsOn: []string{"db"}},
			{Name: "db"},
		}
		got := BuildReverseDeps(cfgs)
		if len(got["db"]) != 2 {
			t.Errorf("got[db] length = %d, want 2; got %v", len(got["db"]), got["db"])
		}
	})
}

// ─── RecordActivityChain ──────────────────────────────────────────────────────

func TestRecordActivityChain(t *testing.T) {
	t.Run("single container no deps: only root updated", func(t *testing.T) {
		m := NewContainerManager(nil)
		cfgs := []ContainerConfig{
			{Name: "app", Host: "app.local"},
		}
		before := time.Now()
		m.RecordActivityChain([]string{"app"}, cfgs)
		after := time.Now()

		ts, ok := m.GetLastSeen("app")
		if !ok {
			t.Fatal("expected app in lastSeen")
		}
		if ts.Before(before) || ts.After(after) {
			t.Errorf("timestamp %v not in range [%v, %v]", ts, before, after)
		}
	})

	t.Run("linear chain A→B→C: all three updated", func(t *testing.T) {
		m := NewContainerManager(nil)
		cfgs := []ContainerConfig{
			{Name: "app", Host: "app.local", DependsOn: []string{"api"}},
			{Name: "api", DependsOn: []string{"db"}},
			{Name: "db"},
		}
		before := time.Now()
		m.RecordActivityChain([]string{"app"}, cfgs)
		after := time.Now()

		for _, name := range []string{"app", "api", "db"} {
			ts, ok := m.GetLastSeen(name)
			if !ok {
				t.Fatalf("expected %q in lastSeen", name)
			}
			if ts.Before(before) || ts.After(after) {
				t.Errorf("%q timestamp %v not in range [%v, %v]", name, ts, before, after)
			}
		}
	})

	t.Run("multiple roots: union of deps updated", func(t *testing.T) {
		m := NewContainerManager(nil)
		cfgs := []ContainerConfig{
			{Name: "app", Host: "app.local", DependsOn: []string{"db"}},
			{Name: "other", Host: "other.local", DependsOn: []string{"redis"}},
			{Name: "db"},
			{Name: "redis"},
		}
		before := time.Now()
		m.RecordActivityChain([]string{"app", "other"}, cfgs)
		after := time.Now()

		for _, name := range []string{"app", "db", "other", "redis"} {
			ts, ok := m.GetLastSeen(name)
			if !ok {
				t.Fatalf("expected %q in lastSeen", name)
			}
			if ts.Before(before) || ts.After(after) {
				t.Errorf("%q timestamp %v not in range", name, ts)
			}
		}
	})

	t.Run("unknown root: falls back to RecordActivity, no panic", func(t *testing.T) {
		m := NewContainerManager(nil)
		cfgs := []ContainerConfig{
			{Name: "other", Host: "other.local"},
		}
		m.RecordActivityChain([]string{"ghost"}, cfgs)

		ts, ok := m.GetLastSeen("ghost")
		if !ok {
			t.Fatal("expected ghost in lastSeen via fallback RecordActivity")
		}
		if ts.IsZero() {
			t.Error("expected non-zero timestamp")
		}
	})

	t.Run("shared dep updated once (deduplication)", func(t *testing.T) {
		m := NewContainerManager(nil)
		cfgs := []ContainerConfig{
			{Name: "app", Host: "app.local", DependsOn: []string{"db"}},
			{Name: "other", Host: "other.local", DependsOn: []string{"db"}},
			{Name: "db"},
		}
		m.RecordActivityChain([]string{"app", "other"}, cfgs)

		_, ok := m.GetLastSeen("db")
		if !ok {
			t.Fatal("expected db in lastSeen")
		}
	})
}
