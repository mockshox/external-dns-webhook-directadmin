package metrics

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProbesStayDownUntilReady(t *testing.T) {
	srv := New(slog.New(slog.DiscardHandler))
	httpSrv := httptest.NewServer(srv.handler())
	defer httpSrv.Close()

	health := get(t, httpSrv.URL+"/healthz")
	ready := get(t, httpSrv.URL+"/ready")
	if health.status != http.StatusServiceUnavailable || ready.status != http.StatusServiceUnavailable {
		t.Fatalf("startup health=%d ready=%d", health.status, ready.status)
	}

	srv.SetHealthz(true)
	srv.SetReady(true)
	health = get(t, httpSrv.URL+"/healthz")
	ready = get(t, httpSrv.URL+"/ready")
	root := get(t, httpSrv.URL+"/")
	if health.status != 200 || ready.status != 200 || root.status != 200 {
		t.Fatalf("ready health=%d ready=%d root=%d", health.status, ready.status, root.status)
	}
	if health.body != "OK\n" {
		t.Fatalf("body %q", health.body)
	}

	metrics := get(t, httpSrv.URL+"/metrics")
	if metrics.status != 200 || len(metrics.body) == 0 {
		t.Fatalf("metrics %d %d bytes", metrics.status, len(metrics.body))
	}
}

func get(t *testing.T, url string) struct {
	status int
	body   string
} {
	t.Helper()
	res, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return struct {
		status int
		body   string
	}{res.StatusCode, string(body)}
}
