package directadmin

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDeleteSelectorIsEncodedOnce(t *testing.T) {
	var query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, key, ok := r.BasicAuth()
		if !ok || user != "user" || key != "secret" {
			t.Errorf("auth user=%s ok=%v", user, ok)
		}
		query = r.URL.RawQuery
		_, _ = io.WriteString(w, `{"error":"0"}`)
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "user", "secret", []string{"example.com"}, false, nil)
	err := client.ApplyChanges(context.Background(), ChangeSet{Delete: []Endpoint{{
		DNSName:    "www.example.com",
		RecordType: "TXT",
		Targets:    []string{"hello=world"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(query, "name%253D") || strings.Contains(query, "%253D") {
		t.Fatalf("selector was encoded twice: %s", query)
	}
	if !strings.Contains(query, "txtrecs0=name%3Dwww%26value%3Dhello%3Dworld") {
		t.Fatalf("selector query: %s", query)
	}
	if !strings.Contains(query, "action=select") || !strings.Contains(query, "domain=example.com") {
		t.Fatalf("query: %s", query)
	}
}

func TestLongestZoneSuffix(t *testing.T) {
	zones := []string{"com", "example.com", "example.org"}
	got, ok := matchZone("a.example.com", zones)
	if !ok || got != "example.com" {
		t.Fatalf("a.example.com -> %q ok=%v", got, ok)
	}
	got, ok = matchZone("b.example.org", zones)
	if !ok || got != "example.org" {
		t.Fatalf("b.example.org -> %q ok=%v", got, ok)
	}
	if _, ok := matchZone("notexample.com", []string{"example.com"}); ok {
		t.Fatal("notexample.com matched example.com")
	}
}

func TestParseZoneShapes(t *testing.T) {
	cases := []string{
		`[{"type":"A","name":"www","value":"203.0.113.10"}]`,
		`{"records":[{"type":"A","name":"www","value":"203.0.113.10"}]}`,
		`{"dnsrecs":{"A":[{"name":"www","value":"203.0.113.10"}]}}`,
		`{"A":{"www":"203.0.113.10"}}`,
		`{"A":[{"name":"www","value":"\"203.0.113.10\""}],"error":"0"}`,
	}
	for _, body := range cases {
		records, err := parseZone(body, "example.com")
		if err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		if len(records) != 1 || records[0].DNSName != "www.example.com" || records[0].RecordType != "A" || records[0].Targets[0] != "203.0.113.10" {
			t.Fatalf("%s -> %#v", body, records)
		}
	}
}

func TestNonJSONZoneIsAnError(t *testing.T) {
	if _, err := parseZone("error=0&text=ok", "example.com"); err == nil {
		t.Fatal("non-JSON dump must fail the reconcile")
	}
}

func TestApplyUsesV021AndLegacyKeys(t *testing.T) {
	var actions []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actions = append(actions, r.URL.Query().Get("action"))
		_, _ = io.WriteString(w, `{"error":"0","success":"ok"}`)
	}))
	defer srv.Close()
	client := NewClient(srv.URL, "user", "secret", []string{"example.com"}, false, nil)

	changes, err := DecodeChanges([]byte(`{
		"delete":[{"dnsName":"old.example.com","recordType":"A","targets":["203.0.113.1"]}],
		"create":[{"dnsName":"new.example.com","recordType":"A","targets":["203.0.113.2"]}],
		"updateOld":[{"dnsName":"www.example.com","recordType":"A","targets":["203.0.113.3"]}],
		"updateNew":[{"dnsName":"www.example.com","recordType":"A","targets":["203.0.113.4"]}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := client.ApplyChanges(context.Background(), changes); err != nil {
		t.Fatal(err)
	}
	if strings.Join(actions, ",") != "select,select,add,add" {
		t.Fatalf("actions %v", actions)
	}

	legacy, err := DecodeChanges([]byte(`{"Create":[{"dnsName":"www.example.com","recordType":"A","targets":["203.0.113.5"]}]}`))
	if err != nil || len(legacy.Create) != 1 {
		t.Fatalf("legacy %#v %v", legacy, err)
	}
}

func TestUnmatchedZoneIsRejected(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()
	client := NewClient(srv.URL, "user", "secret", []string{"example.com"}, false, nil)
	err := client.ApplyChanges(context.Background(), ChangeSet{Create: []Endpoint{{
		DNSName: "www.other.org", RecordType: "A", Targets: []string{"203.0.113.9"},
	}}})
	if err == nil {
		t.Fatal("expected unmatched zone to fail")
	}
	if called {
		t.Fatal("unmatched name was sent to DirectAdmin")
	}
}

func TestMutationError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"error":"1","text":"cannot add"}`)
	}))
	defer srv.Close()
	client := NewClient(srv.URL, "user", "secret", []string{"example.com"}, false, nil)
	err := client.ApplyChanges(context.Background(), ChangeSet{Create: []Endpoint{{
		DNSName: "www.example.com", RecordType: "A", Targets: []string{"203.0.113.10"},
	}}})
	if err == nil || !strings.Contains(err.Error(), "cannot add") {
		t.Fatalf("got %v", err)
	}
}
