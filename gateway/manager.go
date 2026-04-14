package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"
)

// startStatus represents the lifecycle state of a container start attempt.
type startStatus string

const (
	statusStarting startStatus = "starting"
	statusRunning  startStatus = "running"
	statusFailed   startStatus = "failed"
)

// startState holds the current state of a container start attempt.
type startState struct {
	Status startStatus
	Err    string
}

// ContainerManager orchestrates container lifecycle: starting on demand,
// preventing concurrent starts, and auto-stopping idle containers.
type ContainerManager struct {
	client *DockerClient

	mu          sync.Mutex
	locks       map[string]*sync.Mutex
	lastSeen    map[string]time.Time
	startStates map[string]*startState
}

func NewContainerManager(client *DockerClient) *ContainerManager {
	return &ContainerManager{
		client:      client,
		locks:       make(map[string]*sync.Mutex),
		lastSeen:    make(map[string]time.Time),
		startStates: make(map[string]*startState),
	}
}

// getLock returns (or creates) a per-container mutex used to serialise starts.
func (m *ContainerManager) getLock(containerName string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.locks[containerName]; !ok {
		m.locks[containerName] = &sync.Mutex{}
	}
	return m.locks[containerName]
}

// setStartState updates the start state for a container (thread-safe).
func (m *ContainerManager) setStartState(name string, status startStatus, errMsg string) {
	m.mu.Lock()
	m.startStates[name] = &startState{Status: status, Err: errMsg}
	m.mu.Unlock()
}

// GetStartState returns the current start state for a container.
// It is used by the server's /_health endpoint.
func (m *ContainerManager) GetStartState(name string) (status string, errMsg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.startStates[name]
	if !ok {
		return "unknown", ""
	}
	return string(s.Status), s.Err
}

// InitStartState marks a container as "starting" before the async goroutine
// fires. This prevents the first /_health poll from returning "unknown".
func (m *ContainerManager) InitStartState(name string) {
	m.setStartState(name, statusStarting, "")
}

// RecordActivity records the current time as the last activity for a container.
// Call this on every successfully proxied request.
func (m *ContainerManager) RecordActivity(containerName string) {
	m.mu.Lock()
	m.lastSeen[containerName] = time.Now()
	m.mu.Unlock()
}

// RecordActivityChain records activity for each root container and all of its
// transitive dependencies. Use this in place of RecordActivity at server call
// sites, where the full container config is available.
//
// If TopologicalSort fails for a root (e.g. container not yet in config during
// a discovery lag), RecordActivity is called for that root as a silent fallback.
func (m *ContainerManager) RecordActivityChain(roots []string, allContainers []ContainerConfig) {
	now := time.Now()
	toUpdate := make(map[string]struct{})

	for _, root := range roots {
		chain, err := TopologicalSort(root, allContainers)
		if err != nil {
			// Transient condition (discovery lag): fall back to recording the root only.
			m.RecordActivity(root)
			continue
		}
		for _, name := range chain {
			toUpdate[name] = struct{}{}
		}
	}

	if len(toUpdate) == 0 {
		return
	}

	m.mu.Lock()
	for name := range toUpdate {
		m.lastSeen[name] = now
	}
	m.mu.Unlock()
}

// GetLastSeen returns the last activity timestamp for a container.
// Used by the /_status endpoint to show "Last Request" time.
func (m *ContainerManager) GetLastSeen(containerName string) (time.Time, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.lastSeen[containerName]
	return t, ok
}

// BuildReverseDeps returns, for each container D, the list of containers that
// declare D in their DependsOn field (direct dependents only).
func BuildReverseDeps(cfgs []ContainerConfig) map[string][]string {
	rev := make(map[string][]string)
	for _, cfg := range cfgs {
		for _, dep := range cfg.DependsOn {
			rev[dep] = append(rev[dep], cfg.Name)
		}
	}
	return rev
}

// EnsureRunning checks whether a container is running and, if not, starts it.
// Flow: docker start → wait for "running" state → TCP probe → mark ready.
// Uses cfg.StartTimeout as the total budget for the entire sequence.
func (m *ContainerManager) EnsureRunning(ctx context.Context, cfg *ContainerConfig) error {
	// Check current Docker status
	mu := m.getLock(cfg.Name)
	mu.Lock()
	defer mu.Unlock()

	// Check if already running
	status, err := m.client.GetContainerStatus(ctx, cfg.Name)
	if err == nil && status == "running" {
		m.RecordActivity(cfg.Name)
		return nil
	}

	start := time.Now()
	m.setStartState(cfg.Name, statusStarting, "")

	// Ask Docker to start it
	if err := m.client.StartContainer(ctx, cfg.Name); err != nil {
		m.setStartState(cfg.Name, statusFailed, "docker start failed")
		RecordStart(cfg.Name, false, 0)
		return fmt.Errorf("failed to start container %q: %w", cfg.Name, err)
	}

	// Poll until readiness probe passes or context expires
	ip, err := m.client.GetContainerAddress(ctx, cfg.Name, cfg.Network)
	if err != nil {
		m.setStartState(cfg.Name, statusFailed, "cannot find container IP")
		RecordStart(cfg.Name, false, 0)
		return fmt.Errorf("failed to get IP for %q: %w", cfg.Name, err)
	}

	targetAddr := fmt.Sprintf("%s:%s", ip, cfg.TargetPort)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			m.setStartState(cfg.Name, statusFailed, "startup timeout exceeded")
			RecordStart(cfg.Name, false, 0)
			return fmt.Errorf("timeout waiting for %q (%s) to be reachable", cfg.Name, targetAddr)
		case <-ticker.C:
			// Ensure container didn't crash during boot
			status, _ := m.client.GetContainerStatus(ctx, cfg.Name)
			if status == "exited" || status == "dead" {
				m.setStartState(cfg.Name, statusFailed, "container crashed on boot (see docker logs)")
				RecordStart(cfg.Name, false, 0)
				return fmt.Errorf("container %q crashed during boot", cfg.Name)
			}

			// Readiness probe: HTTP if health_path is set, TCP otherwise
			var probeErr error
			if cfg.HealthPath != "" {
				probeErr = m.client.ProbeHTTP(ctx, ip, cfg.TargetPort, cfg.HealthPath)
			} else {
				conn, dialErr := net.DialTimeout("tcp", targetAddr, 500*time.Millisecond)
				if dialErr == nil {
					conn.Close()
				}
				probeErr = dialErr
			}
			if probeErr == nil {
				m.RecordActivity(cfg.Name)
				m.setStartState(cfg.Name, statusRunning, "")
				RecordStart(cfg.Name, true, time.Since(start).Seconds())
				return nil
			}
		}
	}
}

// EnsureDepsRunning starts all dependencies for a container in topological order.
// Each dependency is started sequentially and must pass its readiness probe
// before the next one begins. Fails fast if any dependency fails.
func (m *ContainerManager) EnsureDepsRunning(ctx context.Context, target string, allContainers []ContainerConfig) error {
	order, err := TopologicalSort(target, allContainers)
	if err != nil {
		return fmt.Errorf("dependency resolution failed for %q: %w", target, err)
	}

	// Build a lookup map for container configs.
	cfgMap := make(map[string]*ContainerConfig, len(allContainers))
	for i := range allContainers {
		cfgMap[allContainers[i].Name] = &allContainers[i]
	}

	// Start each dependency in order (target is last in the order).
	// Skip the target itself — it will be started by the caller.
	for _, name := range order {
		if name == target {
			continue
		}
		depCfg, ok := cfgMap[name]
		if !ok {
			return fmt.Errorf("dependency %q not found in container list", name)
		}
		slog.Info("starting dependency", "dependency", name, "for", target)
		m.InitStartState(name)
		if err := m.EnsureRunning(ctx, depCfg); err != nil {
			return fmt.Errorf("dependency %q failed to start: %w", name, err)
		}
	}
	return nil
}

// EnsureGroupRunning starts all group members and their dependencies,
// returning nil when every member is running and ready.
func (m *ContainerManager) EnsureGroupRunning(ctx context.Context, group *GroupConfig, allContainers []ContainerConfig) error {
	cfgMap := make(map[string]*ContainerConfig, len(allContainers))
	for i := range allContainers {
		cfgMap[allContainers[i].Name] = &allContainers[i]
	}

	// Start dependencies for each group member first.
	for _, memberName := range group.Containers {
		if err := m.EnsureDepsRunning(ctx, memberName, allContainers); err != nil {
			return fmt.Errorf("group %q: %w", group.Name, err)
		}
	}

	// Start all group members.
	for _, memberName := range group.Containers {
		memberCfg, ok := cfgMap[memberName]
		if !ok {
			return fmt.Errorf("group %q: member %q not found", group.Name, memberName)
		}
		m.InitStartState(memberName)
		if err := m.EnsureRunning(ctx, memberCfg); err != nil {
			return fmt.Errorf("group %q: member %q failed: %w", group.Name, memberName, err)
		}
	}
	return nil
}

// probeTCPReady probes ip:port until the app responds or ctx expires.
// This function is no longer used after the EnsureRunning refactor.
// func (m *ContainerManager) probeTCPReady(ctx context.Context, cfg *ContainerConfig) error {
// 	ip, err := m.client.GetContainerAddress(ctx, cfg.Name, cfg.Network)
// 	if err != nil {
// 		msg := fmt.Sprintf("cannot resolve container address: %v", err)
// 		m.setStartState(cfg.Name, statusFailed, msg)
// 		return fmt.Errorf("%s", msg)
// 	}
// 	if err := m.client.ProbeTCP(ctx, ip, cfg.TargetPort); err != nil {
// 		msg := fmt.Sprintf("app not responding on port %s: %v", cfg.TargetPort, err)
// 		m.setStartState(cfg.Name, statusFailed, msg)
// 		return fmt.Errorf("%s", msg)
// 	}
// 	m.setStartState(cfg.Name, statusRunning, "")
// 	return nil
// }

// topoMergeStop returns a topological ordering of the nodes in toStop,
// respecting only the DependsOn edges whose destination is also in toStop.
// Result order: [deepest dependency, ..., entry-point].
// No cycle detection needed: cycles are prevented at config load time.
func topoMergeStop(toStop map[string]struct{}, cfgs []ContainerConfig) []string {
	cfgMap := make(map[string]*ContainerConfig, len(cfgs))
	for i := range cfgs {
		cfgMap[cfgs[i].Name] = &cfgs[i]
	}

	visited := make(map[string]bool, len(toStop))
	order := make([]string, 0, len(toStop))

	var visit func(name string)
	visit = func(name string) {
		if visited[name] {
			return
		}
		visited[name] = true
		if cfg, ok := cfgMap[name]; ok {
			for _, dep := range cfg.DependsOn {
				if _, inSet := toStop[dep]; inSet {
					visit(dep)
				}
			}
		}
		order = append(order, name)
	}

	for name := range toStop {
		visit(name)
	}
	return order
}

// cascadeStop stops all containers in the dependency chains of the given
// idle entry-points. Stop order is reverse topological (entry-point first,
// deepest dep last). A dependency is skipped if any running container outside
// the shutdown set still depends on it.
func (m *ContainerManager) cascadeStop(ctx context.Context, idleEntryPoints []string, cfgs []ContainerConfig) {
	toStop := make(map[string]struct{})
	for _, ep := range idleEntryPoints {
		chain, err := TopologicalSort(ep, cfgs)
		if err != nil {
			slog.Warn("idle watcher: skipping entry-point (topo sort failed)",
				"container", ep, "error", err)
			continue
		}
		for _, name := range chain {
			toStop[name] = struct{}{}
		}
	}
	if len(toStop) == 0 {
		return
	}

	revDeps := BuildReverseDeps(cfgs)
	order := topoMergeStop(toStop, cfgs)

	for i := len(order) - 1; i >= 0; i-- {
		name := order[i]

		safe := true
		for _, dependent := range revDeps[name] {
			if _, willStop := toStop[dependent]; willStop {
				continue
			}
			depStatus, err := m.client.GetContainerStatus(ctx, dependent)
			if err == nil && depStatus == "running" {
				slog.Info("idle watcher: skipping dep (still needed)",
					"dep", name, "needed_by", dependent)
				safe = false
				break
			}
		}
		if !safe {
			continue
		}

		status, err := m.client.GetContainerStatus(ctx, name)
		if err != nil || status != "running" {
			continue
		}

		slog.Info("idle watcher: cascade stopping container",
			"container", name, "reason", "cascade_idle",
			"triggered_by", idleEntryPoints)
		if err := m.client.StopContainer(ctx, name); err != nil {
			slog.Error("idle watcher: cascade stop failed",
				"container", name, "error", err)
		} else {
			RecordIdleStop(name)
			m.setStartState(name, "unknown", "")
		}
	}
}

// StartIdleWatcher begins a background routine that periodically checks
// container activity. If a container's idle_timeout is reached, it shuts it down.
func (m *ContainerManager) StartIdleWatcher(ctx context.Context, configProvider func() []ContainerConfig) {
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.checkIdle(ctx, configProvider())
			}
		}
	}()
}

func (m *ContainerManager) checkIdle(ctx context.Context, cfgs []ContainerConfig) {
	m.mu.Lock()
	snapshot := make(map[string]time.Time, len(m.lastSeen))
	for k, v := range m.lastSeen {
		snapshot[k] = v
	}
	m.mu.Unlock()

	now := time.Now()
	var idleEntryPoints []string
	for _, cfg := range cfgs {
		// Only entry-points (Host != "") govern idle shutdown.
		// Pure deps (no Host) are stopped only as part of an entry-point's cascade.
		if cfg.Host == "" || cfg.IdleTimeout == 0 {
			continue
		}
		last, seen := snapshot[cfg.Name]
		if !seen {
			continue
		}
		if now.Sub(last) >= cfg.IdleTimeout {
			idleEntryPoints = append(idleEntryPoints, cfg.Name)
		}
	}

	if len(idleEntryPoints) > 0 {
		m.cascadeStop(ctx, idleEntryPoints, cfgs)
	}
}
