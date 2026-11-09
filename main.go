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

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		logger.Error("configuration", "err", err)
		os.Exit(1)
	}
	client := directadmin.NewClient(cfg.URL, cfg.Username, cfg.LoginKey, cfg.Zones, cfg.Insecure, cfg.TTL, logger)

	webhook := &http.Server{
		Handler:           server.New(client, logger),
		ReadHeaderTimeout: 5 * time.Second,
	}
	probes := metrics.New(logger)

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
	probes.SetReady(true)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 2)
	go func() { errCh <- probes.Serve(metricsLn) }()
	go func() { errCh <- webhook.Serve(webhookLn) }()
	logger.Info("listening", "webhook", cfg.WebhookAddress, "metrics", cfg.MetricsAddress, "zones", cfg.Zones)

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
