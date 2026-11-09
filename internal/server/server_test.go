package server

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mockshox/external-dns-webhook-directadmin/internal/directadmin"
)

func TestNegotiateAndRecords(t *testing.T) {
	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"A":{"www":"203.0.113.10"}}`)
	}))
	defer panel.Close()
	client := directadmin.NewClient(panel.URL, "user", "secret", []string{"example.com"}, false, 60, slog.New(slog.DiscardHandler))
	srv := httptest.NewServer(New(client, slog.New(slog.DiscardHandler)))
	defer srv.Close()

	negotiate := get(t, srv.URL+"/")
	if negotiate.status != 200 || !strings.Contains(negotiate.body, `"include":["example.com"]`) {
		t.Fatalf("negotiate %d %s", negotiate.status, negotiate.body)
	}
	if negotiate.contentType != mediaType {
		t.Fatalf("content type %s", negotiate.contentType)
	}

	records := get(t, srv.URL+"/records")
	if records.status != 200 || !strings.Contains(records.body, `"dnsName":"www.example.com"`) {
		t.Fatalf("records %d %s", records.status, records.body)
	}

	health := httptest.NewServer(Health())
	defer health.Close()
	probe := get(t, health.URL+"/healthz")
	if probe.status != 200 || probe.body != "ok" {
		t.Fatalf("health %d %s", probe.status, probe.body)
	}
}

func TestAdjustEchoesAndApplyReturns204(t *testing.T) {
	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"error":"0"}`)
	}))
	defer panel.Close()
	client := directadmin.NewClient(panel.URL, "user", "secret", []string{"example.com"}, false, 60, slog.New(slog.DiscardHandler))
	srv := httptest.NewServer(New(client, slog.New(slog.DiscardHandler)))
	defer srv.Close()

	adjust, err := http.Post(srv.URL+"/adjustendpoints", mediaType, strings.NewReader(`[{"dnsName":"Alias.Example.com.","recordType":"CNAME","targets":["foo.other.com"],"recordTTL":0}]`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(adjust.Body)
	adjust.Body.Close()
	if adjust.StatusCode != 200 || !strings.Contains(string(body), `"dnsName":"alias.example.com"`) || !strings.Contains(string(body), `"foo.other.com."`) {
		t.Fatalf("adjust %d %s", adjust.StatusCode, body)
	}

	apply, err := http.Post(srv.URL+"/records", mediaType, strings.NewReader(`{"create":[{"dnsName":"www.example.com","recordType":"A","targets":["203.0.113.10"]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	apply.Body.Close()
	if apply.StatusCode != 204 {
		t.Fatalf("apply %d", apply.StatusCode)
	}
}

type response struct {
	status      int
	body        string
	contentType string
}

func get(t *testing.T, url string) response {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response{status: resp.StatusCode, body: string(body), contentType: resp.Header.Get("Content-Type")}
}
