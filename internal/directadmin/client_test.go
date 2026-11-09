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

	client := NewClient(srv.URL, "user", "secret", []string{"example.com"}, false, 60, nil)
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
	client := NewClient(srv.URL, "user", "secret", []string{"example.com"}, false, 60, nil)

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
	if strings.Join(actions, ",") != "add,add,select,select" {
		t.Fatalf("actions %v", actions)
	}

	legacy, err := DecodeChanges([]byte(`{"Create":[{"dnsName":"www.example.com","recordType":"A","targets":["203.0.113.5"]}]}`))
	if err != nil || len(legacy.Create) != 1 {
		t.Fatalf("legacy %#v %v", legacy, err)
	}
}

func TestReplacementAddsBeforeDelete(t *testing.T) {
	var steps []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		name := q.Get("name")
		if name == "" {
			name = q.Get("arecs0")
		}
		steps = append(steps, q.Get("action")+":"+name)
		_, _ = io.WriteString(w, `{"error":"0"}`)
	}))
	defer srv.Close()
	client := NewClient(srv.URL, "user", "secret", []string{"example.com"}, false, 60, nil)
	err := client.ApplyChanges(context.Background(), ChangeSet{
		Delete: []Endpoint{
			{DNSName: "gone.example.com", RecordType: "A", Targets: []string{"203.0.113.1"}},
			{DNSName: "www.example.com", RecordType: "A", Targets: []string{"203.0.113.3"}},
		},
		Create: []Endpoint{
			{DNSName: "www.example.com", RecordType: "A", Targets: []string{"203.0.113.4"}, RecordTTL: 60},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(steps, ",")
	want := "add:www,select:name=www&value=203.0.113.3,select:name=gone&value=203.0.113.1"
	if got != want {
		t.Fatalf("order %s", got)
	}
}

func TestApexCreateListAndDelete(t *testing.T) {
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.RawQuery)
		if r.URL.Query().Get("action") == "" {
			_, _ = io.WriteString(w, `{"A":{"@":"203.0.113.10"},"CNAME":{"www":"foo.other.com"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"error":"0"}`)
	}))
	defer srv.Close()
	client := NewClient(srv.URL, "user", "secret", []string{"example.com"}, false, 60, nil)

	records, err := client.Records(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	foundApex := false
	foundCNAME := false
	for _, rec := range records {
		if rec.RecordType == "A" && rec.DNSName == "example.com" && len(rec.Targets) == 1 && rec.Targets[0] == "203.0.113.10" {
			foundApex = true
		}
		if rec.RecordType == "CNAME" && rec.Targets[0] == "foo.other.com." {
			foundCNAME = true
		}
	}
	if !foundApex || !foundCNAME {
		t.Fatalf("records %#v", records)
	}

	err = client.ApplyChanges(context.Background(), ChangeSet{
		Create: []Endpoint{{DNSName: "example.com", RecordType: "A", Targets: []string{"203.0.113.10"}}},
		Delete: []Endpoint{{DNSName: "example.com", RecordType: "A", Targets: []string{"203.0.113.10"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(queries, "\n")
	if !strings.Contains(joined, "name=%40") || !strings.Contains(joined, "action=add") || !strings.Contains(joined, "arecs0=name%3D%40%26value%3D203.0.113.10") {
		t.Fatalf("apex queries:\n%s", joined)
	}
}

func TestCNAMETargetIsAbsolute(t *testing.T) {
	var query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		_, _ = io.WriteString(w, `{"error":"0"}`)
	}))
	defer srv.Close()
	client := NewClient(srv.URL, "user", "secret", []string{"example.com"}, false, 60, nil)

	err := client.ApplyChanges(context.Background(), ChangeSet{Create: []Endpoint{{
		DNSName: "alias.example.com", RecordType: "CNAME", Targets: []string{"foo.other.com"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(query, "value=foo.other.com.") {
		t.Fatalf("CNAME was sent relative: %s", query)
	}

	err = client.ApplyChanges(context.Background(), ChangeSet{Create: []Endpoint{{
		DNSName: "alias.example.com", RecordType: "CNAME", Targets: []string{"www"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(query, "value=www.") {
		t.Fatalf("single-label CNAME should stay relative: %s", query)
	}
	if !strings.Contains(query, "value=www") {
		t.Fatalf("relative CNAME query: %s", query)
	}
}

func TestUnmatchedZoneIsRejected(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()
	client := NewClient(srv.URL, "user", "secret", []string{"example.com"}, false, 60, nil)
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
	client := NewClient(srv.URL, "user", "secret", []string{"example.com"}, false, 60, nil)
	err := client.ApplyChanges(context.Background(), ChangeSet{Create: []Endpoint{{
		DNSName: "www.example.com", RecordType: "A", Targets: []string{"203.0.113.10"},
	}}})
	if err == nil || !strings.Contains(err.Error(), "cannot add") {
		t.Fatalf("got %v", err)
	}
}

func TestAddUsesRequestedTTLOrDefault(t *testing.T) {
	var query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		_, _ = io.WriteString(w, `{"error":"0"}`)
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "user", "secret", []string{"example.com"}, false, 60, nil)
	err := client.ApplyChanges(context.Background(), ChangeSet{Create: []Endpoint{{
		DNSName: "www.example.com", RecordType: "A", Targets: []string{"203.0.113.10"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(query, "ttl=60") {
		t.Fatalf("default ttl missing: %s", query)
	}

	err = client.ApplyChanges(context.Background(), ChangeSet{Create: []Endpoint{{
		DNSName: "www.example.com", RecordType: "A", Targets: []string{"203.0.113.10"}, RecordTTL: 120,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(query, "ttl=120") {
		t.Fatalf("record ttl missing: %s", query)
	}

	unset := NewClient(srv.URL, "user", "secret", []string{"example.com"}, false, 0, nil)
	err = unset.ApplyChanges(context.Background(), ChangeSet{Create: []Endpoint{{
		DNSName: "www.example.com", RecordType: "A", Targets: []string{"203.0.113.10"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(query, "ttl=") {
		t.Fatalf("ttl sent without a default or record TTL: %s", query)
	}
}

func TestParseZoneKeepsRecordTTL(t *testing.T) {
	records, err := parseZone(`{"records":[{"type":"A","name":"www","value":"203.0.113.10","ttl":"14400"},{"type":"TXT","name":"www","value":"hello","ttl":60}]}`, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].RecordTTL != 14400 || records[1].RecordTTL != 60 {
		t.Fatalf("%#v", records)
	}
}
