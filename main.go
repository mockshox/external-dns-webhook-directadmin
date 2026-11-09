package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mockshox/external-dns-webhook-directadmin/internal/config"
	"github.com/mockshox/external-dns-webhook-directadmin/internal/directadmin"
	"github.com/mockshox/external-dns-webhook-directadmin/internal/server"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		logger.Error("configuration", "err", err)
		os.Exit(1)
	}
	client := directadmin.NewClient(cfg.URL, cfg.Username, cfg.LoginKey, cfg.Zones, cfg.Insecure, logger)

	webhook := &http.Server{
		Addr:              cfg.WebhookAddress,
		Handler:           server.New(client, logger),
		ReadHeaderTimeout: 5 * time.Second,
	}
	health := &http.Server{
		Addr:              cfg.HealthAddress,
		Handler:           server.Health(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 2)
	go func() { errCh <- webhook.ListenAndServe() }()
	go func() { errCh <- health.ListenAndServe() }()
	logger.Info("listening", "webhook", cfg.WebhookAddress, "health", cfg.HealthAddress, "zones", cfg.Zones)

	select {
	case <-ctx.Done():
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server stopped", "err", err)
			os.Exit(1)
		}
	}

	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = webhook.Shutdown(shutdown)
	_ = health.Shutdown(shutdown)
}
