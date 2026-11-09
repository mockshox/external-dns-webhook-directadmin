// Package metrics serves Prometheus metrics and the chart probes.
package metrics

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Server is the port-8080 process probe. /healthz and /ready stay down until
// the webhook is listening, and /metrics exposes the default Prometheus registry.
type Server struct {
	log    *slog.Logger
	http   *http.Server
	mu     sync.Mutex
	health bool
	ready  bool
}

// New returns a server that answers not ready and not healthy.
func New(logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	s := &Server{log: logger}
	s.http = &http.Server{
		Handler:           s.handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      5 * time.Second,
	}
	return s
}

// SetHealthz reports whether the process should pass the liveness probe.
func (s *Server) SetHealthz(ok bool) {
	s.mu.Lock()
	s.health = ok
	s.mu.Unlock()
}

// SetReady reports whether the process should pass the readiness probe.
func (s *Server) SetReady(ok bool) {
	s.mu.Lock()
	s.ready = ok
	s.mu.Unlock()
}

// Serve accepts connections on ln.
func (s *Server) Serve(ln net.Listener) error {
	err := s.http.Serve(ln)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

// Shutdown stops accepting probe and metrics requests.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.http.Shutdown(ctx)
}

func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.healthz)
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /ready", s.readyz)
	mux.Handle("GET /metrics", promhttp.Handler())
	return mux
}

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	ok := s.health
	s.mu.Unlock()
	writeProbe(w, ok)
}

func (s *Server) readyz(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	ok := s.ready
	s.mu.Unlock()
	writeProbe(w, ok)
}

func writeProbe(w http.ResponseWriter, ok bool) {
	w.Header().Set("Content-Type", "text/plain")
	if ok {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "OK")
		return
	}
	w.WriteHeader(http.StatusServiceUnavailable)
	fmt.Fprintln(w, "DOWN")
}
