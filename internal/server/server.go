// Package server is the ExternalDNS webhook HTTP API.
package server

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/mockshox/external-dns-webhook-directadmin/internal/directadmin"
)

const mediaType = "application/external.dns.webhook+json;version=1"

// maxBody matches ExternalDNS's default webhook body limit (32 MiB).
const maxBody = 32 << 20

// New returns the webhook handler ExternalDNS calls.
func New(client *directadmin.Client, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	s := &handler{client: client, log: logger}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", health)
	mux.HandleFunc("GET /health", health)
	mux.HandleFunc("GET /{$}", s.negotiate)
	mux.HandleFunc("GET /records", s.records)
	mux.HandleFunc("POST /records", s.apply)
	mux.HandleFunc("POST /applychanges", s.apply)
	mux.HandleFunc("POST /adjustendpoints", s.adjust)
	return mux
}

func health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

type handler struct {
	client *directadmin.Client
	log    *slog.Logger
}

func (h *handler) negotiate(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"include": h.client.Zones()})
}

func (h *handler) records(w http.ResponseWriter, r *http.Request) {
	records, err := h.client.Records(r.Context())
	if err != nil {
		h.fail(w, "list records", err)
		return
	}
	writeJSON(w, http.StatusOK, records)
}

func (h *handler) apply(w http.ResponseWriter, r *http.Request) {
	raw, err := readBody(r)
	if err != nil {
		http.Error(w, err.Error(), statusFor(err))
		return
	}
	changes, err := directadmin.DecodeChanges(raw)
	if err != nil {
		http.Error(w, "invalid changes", http.StatusBadRequest)
		return
	}
	if err := h.client.ApplyChanges(r.Context(), changes); err != nil {
		h.fail(w, "apply changes", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) adjust(w http.ResponseWriter, r *http.Request) {
	raw, err := readBody(r)
	if err != nil {
		http.Error(w, err.Error(), statusFor(err))
		return
	}
	adjusted, err := directadmin.AdjustEndpoints(raw)
	if err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", mediaType)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(adjusted)
}

func (h *handler) fail(w http.ResponseWriter, what string, err error) {
	h.log.Error(what, "err", err)
	http.Error(w, what, http.StatusInternalServerError)
}

func readBody(r *http.Request) ([]byte, error) {
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxBody {
		return nil, errBodyTooLarge
	}
	if len(body) == 0 {
		return []byte("{}"), nil
	}
	return body, nil
}

var errBodyTooLarge = errors.New("request body too large")

func statusFor(err error) int {
	if errors.Is(err, errBodyTooLarge) {
		return http.StatusRequestEntityTooLarge
	}
	return http.StatusBadRequest
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		http.Error(w, "encode", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", mediaType)
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
