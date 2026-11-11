package metrics

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mockshox/external-dns-webhook-directadmin/internal/directadmin"
)

func TestProbesStayDownUntilReady(t *testing.T) {
	srv := New(slog.New(slog.DiscardHandler), "test")
	httpSrv := httptest.NewServer(srv.http.Handler)
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
	if !strings.Contains(metrics.body, `webhook_build_info{version="test"}`) {
		t.Fatalf("build info missing:\n%s", metrics.body)
	}
}

func TestReadyFollowsZoneList(t *testing.T) {
	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("action") == "add" {
			http.Error(w, "nope", http.StatusInternalServerError)
			return
		}
		_, _ = io.WriteString(w, `{"A":{}}`)
	}))
	defer panel.Close()

	probes := New(slog.New(slog.DiscardHandler), "test")
	probes.SetHealthz(true)
	client := directadmin.NewClient(panel.URL, "user", "secret", []string{"example.com"}, false, 60, slog.New(slog.DiscardHandler))
	client.UseObserver(probes)
	httpSrv := httptest.NewServer(probes.http.Handler)
	defer httpSrv.Close()

	if got := get(t, httpSrv.URL+"/healthz"); got.status != 200 {
		t.Fatalf("liveness %d", got.status)
	}
	if got := get(t, httpSrv.URL+"/ready"); got.status != http.StatusServiceUnavailable {
		t.Fatalf("ready before list %d", got.status)
	}
	if _, err := client.Records(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := get(t, httpSrv.URL+"/ready"); got.status != 200 {
		t.Fatalf("ready after list %d", got.status)
	}
	body := get(t, httpSrv.URL+"/metrics").body
	if !strings.Contains(body, "directadmin_api_requests_total") || !strings.Contains(body, "directadmin_last_success_timestamp_seconds") {
		t.Fatalf("metrics missing series:\n%s", body)
	}

	err := client.ApplyChanges(context.Background(), directadmin.ChangeSet{Create: []directadmin.Endpoint{{
		DNSName: "www.example.com", RecordType: "A", Targets: []string{"203.0.113.10"},
	}}})
	if err == nil {
		t.Fatal("expected apply failure")
	}
	if got := get(t, httpSrv.URL+"/ready"); got.status != http.StatusServiceUnavailable {
		t.Fatalf("ready after failed apply %d", got.status)
	}
	if got := get(t, httpSrv.URL+"/healthz"); got.status != 200 {
		t.Fatalf("liveness after failed apply %d", got.status)
	}
}

func TestReadyStaysUpWhenPanelRejectsOneName(t *testing.T) {
	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("action") == "add" {
			_, _ = io.WriteString(w, `{"error":"1","text":"cannot add"}`)
			return
		}
		_, _ = io.WriteString(w, `{"A":{}}`)
	}))
	defer panel.Close()

	probes := New(slog.New(slog.DiscardHandler), "test")
	probes.SetHealthz(true)
	client := directadmin.NewClient(panel.URL, "user", "secret", []string{"example.com"}, false, 60, slog.New(slog.DiscardHandler))
	client.UseObserver(probes)
	httpSrv := httptest.NewServer(probes.http.Handler)
	defer httpSrv.Close()

	if _, err := client.Records(context.Background()); err != nil {
		t.Fatal(err)
	}
	err := client.ApplyChanges(context.Background(), directadmin.ChangeSet{Create: []directadmin.Endpoint{{
		DNSName: "www.example.com", RecordType: "A", Targets: []string{"203.0.113.10"},
	}}})
	if err == nil {
		t.Fatal("expected apply failure")
	}
	if got := get(t, httpSrv.URL+"/ready"); got.status != http.StatusOK {
		t.Fatalf("ready after rejected record %d", got.status)
	}
	if got := get(t, httpSrv.URL+"/healthz"); got.status != http.StatusOK {
		t.Fatalf("liveness %d", got.status)
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
