package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"docker-gateway/gateway"
)

func main() {
	// Configure structured JSON logging as the global default.
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	// Root context — cancelled on SIGTERM / SIGINT for graceful shutdown.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Load YAML configuration (path from CONFIG_PATH env, default /etc/gateway/config.yaml)
	cfg, err := gateway.LoadConfig()
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	// Initialize Docker client
	dockerClient, err := gateway.NewDockerClient()
	if err != nil {
		slog.Error("failed to initialize Docker client", "error", err)
		os.Exit(1)
	}
	defer dockerClient.Close()

	// Initialize Container Manager
	manager := gateway.NewContainerManager(dockerClient)

	// Initialize Cron Scheduler
	scheduler := gateway.NewScheduleManager(dockerClient, manager)

	// Initialize and start the HTTP server
	server, err := gateway.NewServer(manager, scheduler, cfg)
	if err != nil {
		slog.Error("failed to initialize server", "error", err)
		os.Exit(1)
	}

	// Initialize Auto-Discovery
	discoveryManager := gateway.NewDiscoveryManager(dockerClient, cfg, server.ReloadConfig)
	discoveryManager.Start(ctx, cfg.Gateway.DiscoveryInterval)
	slog.Info("discovery started", "interval", cfg.Gateway.DiscoveryInterval)

	// Start scheduler and register initial jobs.
	scheduler.Start(ctx)
	scheduler.Sync(cfg.Containers, cfg.Gateway.ScheduleTimezone)
	slog.Info("scheduler started")

	// Start idle-watcher goroutine with a callback to get the latest config
	manager.StartIdleWatcher(ctx, func() []gateway.ContainerConfig {
		return server.GetConfig().Containers
	})

	// Signal handling: SIGHUP → hot-reload config, SIGTERM/SIGINT → graceful shutdown.
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		for sig := range sigChan {
			switch sig {
			case syscall.SIGHUP:
				slog.Info("received SIGHUP, reloading static configuration")
				newCfg, err := gateway.LoadConfig()
				if err != nil {
					slog.Error("hot-reload failed", "error", err)
					continue
				}
				discoveryManager.UpdateStaticConfig(newCfg)
				slog.Info("static configuration reloaded and discovery pass triggered")
			case syscall.SIGTERM, syscall.SIGINT:
				slog.Info("received shutdown signal, initiating graceful shutdown", "signal", sig.String())
				cancel()
				return
			}
		}
	}()

	if err := server.Start(ctx); err != nil {
		slog.Error("server error", "error", err)
		os.Exit(1)
	}
}
