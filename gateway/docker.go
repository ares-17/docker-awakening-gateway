package gateway

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	dockernetwork "github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
)

// DockerClient handles interactions with the Docker daemon
type DockerClient struct {
	cli *client.Client
}

// NewDockerClient creates a new DockerClient instance
func NewDockerClient() (*DockerClient, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}
	return &DockerClient{cli: cli}, nil
}

// ContainerInfo holds lightweight container details for the status dashboard.
type ContainerInfo struct {
	Status     string
	Image      string
	StartedAt  time.Time
	FinishedAt time.Time
}

// GetContainerStatus returns the status of a container (e.g. "running", "exited")
func (d *DockerClient) GetContainerStatus(ctx context.Context, containerName string) (string, error) {
	info, err := d.cli.ContainerInspect(ctx, containerName)
	if err != nil {
		return "", err
	}
	return info.State.Status, nil
}

// InspectContainer returns lightweight container details for the status dashboard.
func (d *DockerClient) InspectContainer(ctx context.Context, containerName string) (*ContainerInfo, error) {
	info, err := d.cli.ContainerInspect(ctx, containerName)
	if err != nil {
		return nil, err
	}
	ci := &ContainerInfo{
		Status: info.State.Status,
		Image:  info.Config.Image,
	}
	if t, err := time.Parse(time.RFC3339Nano, info.State.StartedAt); err == nil {
		ci.StartedAt = t
	}
	if t, err := time.Parse(time.RFC3339Nano, info.State.FinishedAt); err == nil {
		ci.FinishedAt = t
	}
	return ci, nil
}

// DiscoverLabeledContainers lists all containers with the `gateway.enabled=true` label
// and parses their labels into ContainerConfig structs.
func (d *DockerClient) DiscoverLabeledContainers(ctx context.Context) ([]ContainerConfig, error) {
	args := filters.NewArgs()
	args.Add("label", "dag.enabled=true")

	opts := container.ListOptions{
		All:     true,
		Filters: args,
	}

	containers, err := d.cli.ContainerList(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to list labeled containers: %w", err)
	}

	var configs []ContainerConfig
	for _, c := range containers {
		if len(c.Names) == 0 {
			continue
		}
		
		cfg := ContainerConfig{
			Name: strings.TrimPrefix(c.Names[0], "/"),
		}

		if host, ok := c.Labels["dag.host"]; ok && host != "" {
			cfg.Host = host
		} else {
			slog.Warn("discovery: container missing required dag.host", "container", cfg.Name)
			continue
		}

		cfg.TargetPort = "80"
		if port, ok := c.Labels["dag.target_port"]; ok && port != "" {
			cfg.TargetPort = port
		}

		cfg.StartTimeout = 60 * time.Second
		if val, ok := c.Labels["dag.start_timeout"]; ok && val != "" {
			if parseDur, err := time.ParseDuration(val); err == nil {
				cfg.StartTimeout = parseDur
			} else {
				slog.Warn("discovery: invalid start_timeout", "value", val, "container", cfg.Name, "error", err)
			}
		}

		if val, ok := c.Labels["dag.idle_timeout"]; ok && val != "" {
			if parseDur, err := time.ParseDuration(val); err == nil {
				cfg.IdleTimeout = parseDur
			} else {
				slog.Warn("discovery: invalid idle_timeout", "value", val, "container", cfg.Name, "error", err)
			}
		}

		if val, ok := c.Labels["dag.network"]; ok {
			cfg.Network = val
		}

		cfg.RedirectPath = "/"
		if val, ok := c.Labels["dag.redirect_path"]; ok && val != "" {
			cfg.RedirectPath = val
		}

		cfg.Icon = "docker"
		if val, ok := c.Labels["dag.icon"]; ok && val != "" {
			cfg.Icon = val
		}

		if val, ok := c.Labels["dag.health_path"]; ok && val != "" {
			cfg.HealthPath = val
		}

		if val, ok := c.Labels["dag.depends_on"]; ok && val != "" {
			cfg.DependsOn = strings.Split(val, ",")
			// Trim whitespace from each dependency name
			for j := range cfg.DependsOn {
				cfg.DependsOn[j] = strings.TrimSpace(cfg.DependsOn[j])
			}
		}

		configs = append(configs, cfg)
	}

	return configs, nil
}

// GetContainerAddress returns the IP address of the container.
// If network is non-empty, it looks up that specific Docker network.
// Otherwise it returns the IP from the first available network.
func (d *DockerClient) GetContainerAddress(ctx context.Context, containerName, network string) (string, error) {
	info, err := d.cli.ContainerInspect(ctx, containerName)
	if err != nil {
		return "", err
	}

	nets := info.NetworkSettings.Networks
	if len(nets) == 0 {
		return "", fmt.Errorf("container %s has no network interfaces", containerName)
	}

	// Prefer the requested network if specified
	if network != "" {
		if n, ok := nets[network]; ok && n.IPAddress != "" {
			return n.IPAddress, nil
		}
		return "", fmt.Errorf("container %s is not on network %q (attached networks: %s)",
			containerName, network, joinNetworkNames(nets))
	}

	// Fallback: return the first non-empty IP
	for _, n := range nets {
		if n.IPAddress != "" {
			return n.IPAddress, nil
		}
	}
	return "", fmt.Errorf("could not find IP address for container %s", containerName)
}

// joinNetworkNames lists attached network names for error messages.
func joinNetworkNames(nets map[string]*dockernetwork.EndpointSettings) string {
	names := make([]string, 0, len(nets))
	for name := range nets {
		names = append(names, name)
	}
	return strings.Join(names, ", ")
}

// ProbeTCP attempts a TCP connection to ip:port, retrying every 300 ms until
// the connection succeeds or ctx is cancelled. Returns nil on success.
func (d *DockerClient) ProbeTCP(ctx context.Context, ip, port string) error {
	addr := net.JoinHostPort(ip, port)
	for {
		dialer := &net.Dialer{}
		conn, err := dialer.DialContext(ctx, "tcp", addr)
		if err == nil {
			conn.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("TCP probe timed out for %s: %w", addr, ctx.Err())
		case <-time.After(300 * time.Millisecond):
			// retry
		}
	}
}

// ProbeHTTP performs an HTTP GET to http://ip:port/path, retrying every 500 ms
// until a 2xx response is received or ctx is cancelled. Returns nil on success.
func (d *DockerClient) ProbeHTTP(ctx context.Context, ip, port, path string) error {
	probeURL := fmt.Sprintf("http://%s:%s%s", ip, port, path)
	httpClient := &http.Client{Timeout: 2 * time.Second}
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, probeURL, nil)
		if err != nil {
			return fmt.Errorf("HTTP probe request creation failed for %s: %w", probeURL, err)
		}
		resp, err := httpClient.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("HTTP probe timed out for %s: %w", probeURL, ctx.Err())
		case <-time.After(500 * time.Millisecond):
			// retry
		}
	}
}

// StartContainer starts a container by name.
func (d *DockerClient) StartContainer(ctx context.Context, containerName string) error {
	return d.cli.ContainerStart(ctx, containerName, container.StartOptions{})
}

// StopContainer stops a running container gracefully.
func (d *DockerClient) StopContainer(ctx context.Context, containerName string) error {
	return d.cli.ContainerStop(ctx, containerName, container.StopOptions{})
}

// GetContainerLogs returns the last n log lines from the container.
// Lines are sanitised: Docker's 8-byte stream header is stripped and the
// output is safe for rendering as plain text in the browser.
func (d *DockerClient) GetContainerLogs(ctx context.Context, containerName string, n int) ([]string, error) {
	tail := fmt.Sprintf("%d", n)
	opts := container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Tail:       tail,
		Timestamps: false,
	}
	rc, err := d.cli.ContainerLogs(ctx, containerName, opts)
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	raw, err := io.ReadAll(rc)
	if err != nil {
		return nil, err
	}

	text := stripDockerLogHeaders(raw)

	var lines []string
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimRight(l, "\r")
		if l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines, nil
}

// stripDockerLogHeaders removes the 8-byte multiplexing header Docker prepends
// to each log frame: [stream_type(1), 0, 0, 0, size(4)] + payload.
func stripDockerLogHeaders(b []byte) string {
	var buf bytes.Buffer
	for len(b) >= 8 {
		size := int(b[4])<<24 | int(b[5])<<16 | int(b[6])<<8 | int(b[7])
		b = b[8:]
		if size > len(b) {
			size = len(b)
		}
		buf.Write(b[:size])
		b = b[size:]
	}
	return buf.String()
}

// Close closes the Docker client connection
func (d *DockerClient) Close() error {
	return d.cli.Close()
}
