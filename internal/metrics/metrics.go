// Package metrics serves Prometheus metrics and the chart probes.
package metrics

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Server is the port-8080 process probe. /healthz stays down until the
// process is listening. /ready stays down until a zone list or apply succeeds,
// and drops again when one fails. /metrics exposes process and DirectAdmin
// API metrics.
type Server struct {
	log        *slog.Logger
	http       *http.Server
	requests   *prometheus.CounterVec
	latency    *prometheus.HistogramVec
	last       *prometheus.GaugeVec
	seq        atomic.Uint64
	mu         sync.Mutex
	health     bool
	ready      bool
	readyToken uint64
}

// New returns a server that answers not ready and not healthy.
// version is the build version reported by webhook_build_info.
func New(logger *slog.Logger, version string) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	if version == "" {
		version = "dev"
	}
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	requests := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "directadmin_api_requests_total",
		Help: "DirectAdmin API calls by operation and result.",
	}, []string{"operation", "result"})
	latency := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "directadmin_api_request_duration_seconds",
		Help:    "DirectAdmin API call latency.",
		Buckets: prometheus.ExponentialBuckets(0.05, 2, 10),
	}, []string{"operation"})
	last := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "directadmin_last_success_timestamp_seconds",
		Help: "Unix time of the last successful list or apply.",
	}, []string{"operation"})
	info := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "webhook_build_info",
		Help: "Build information for the webhook.",
	}, []string{"version"})
	info.WithLabelValues(version).Set(1)
	reg.MustRegister(requests, latency, last, info)

	s := &Server{log: logger, requests: requests, latency: latency, last: last}
	s.http = &http.Server{
		Handler:           s.handler(reg),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      5 * time.Second,
	}
	return s
}

// Observe records one DirectAdmin HTTP call.
func (s *Server) Observe(operation, result string, seconds float64) {
	s.requests.WithLabelValues(operation, result).Inc()
	s.latency.WithLabelValues(operation).Observe(seconds)
}

// Begin marks the start of a list or apply.
func (s *Server) Begin() uint64 {
	return s.seq.Add(1)
}

// Finish records the outcome of the list or apply that Begin started.
// An older call does not replace the result of a newer one.
func (s *Server) Finish(operation string, token uint64, err error) {
	if err == nil {
		s.last.WithLabelValues(operation).Set(float64(time.Now().Unix()))
	}
	if operation != "list" && operation != "apply" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if token < s.readyToken {
		return
	}
	s.readyToken = token
	s.ready = err == nil
}

// SetHealthz reports whether the process should pass the liveness probe.
func (s *Server) SetHealthz(ok bool) {
	s.mu.Lock()
	s.health = ok
	s.mu.Unlock()
}

// SetReady reports whether the process should pass the readiness probe.
// Clearing readiness outranks an in-flight list or apply, so shutdown stays down.
func (s *Server) SetReady(ok bool) {
	s.mu.Lock()
	s.ready = ok
	if !ok {
		s.readyToken = ^uint64(0)
	}
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

func (s *Server) handler(reg *prometheus.Registry) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.healthz)
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /ready", s.readyz)
	mux.Handle("GET /metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
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
