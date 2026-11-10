package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mockshox/external-dns-webhook-directadmin/internal/config"
	"github.com/mockshox/external-dns-webhook-directadmin/internal/directadmin"
	"github.com/mockshox/external-dns-webhook-directadmin/internal/metrics"
	"github.com/mockshox/external-dns-webhook-directadmin/internal/server"
)

// version is set with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	bootstrap := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		bootstrap.Error("configuration", "err", err)
		os.Exit(1)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
	client := directadmin.NewClient(cfg.URL, cfg.Username, cfg.LoginKey, cfg.Zones, cfg.Insecure, cfg.TTL, logger)
	probes := metrics.New(logger, version)
	client.UseObserver(probes)

	webhook := &http.Server{
		Handler:           server.New(client, logger),
		ReadHeaderTimeout: 5 * time.Second,
	}

	metricsLn, err := net.Listen("tcp", cfg.MetricsAddress)
	if err != nil {
		logger.Error("metrics listen", "addr", cfg.MetricsAddress, "err", err)
		os.Exit(1)
	}
	webhookLn, err := net.Listen("tcp", cfg.WebhookAddress)
	if err != nil {
		logger.Error("webhook listen", "addr", cfg.WebhookAddress, "err", err)
		os.Exit(1)
	}
	probes.SetHealthz(true)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 2)
	go func() { errCh <- probes.Serve(metricsLn) }()
	go func() { errCh <- webhook.Serve(webhookLn) }()
	go watchPanel(ctx, client, logger)
	logger.Info("listening", "version", version, "webhook", cfg.WebhookAddress, "metrics", cfg.MetricsAddress, "zones", cfg.Zones)

	select {
	case <-ctx.Done():
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server stopped", "err", err)
			probes.SetReady(false)
			probes.SetHealthz(false)
			os.Exit(1)
		}
	}

	probes.SetReady(false)
	probes.SetHealthz(false)
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = webhook.Shutdown(shutdown)
	_ = probes.Shutdown(shutdown)
}

// watchPanel lists the zones once, retrying until the panel answers, then
// stops. Later list and apply calls keep readiness current.
func watchPanel(ctx context.Context, client *directadmin.Client, logger *slog.Logger) {
	backoff := time.Second
	for {
		_, err := client.Records(ctx)
		if err == nil {
			logger.Info("directadmin zone list succeeded")
			return
		}
		logger.Error("directadmin zone list", "err", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}
