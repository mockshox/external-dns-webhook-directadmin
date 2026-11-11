package directadmin

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
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
	})
	if err != nil {
		t.Fatal(err)
	}
	err = client.ApplyChanges(context.Background(), ChangeSet{
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

func TestUnrecognizedZoneDumpIsAnError(t *testing.T) {
	cases := []string{
		`{"error":"0"}`,
		`{"error":"0","success":"ok"}`,
		`[{"foo":"bar"}]`,
		`{"records":[{"foo":"bar"}]}`,
	}
	for _, body := range cases {
		if _, err := parseZone(body, "example.com"); err == nil {
			t.Fatalf("%s parsed as an empty zone", body)
		}
	}
	empty := []string{
		`[]`,
		`{"records":[]}`,
		`{"A":{}}`,
		`{"dnsrecs":{}}`,
		`{"MX":{"@":"mail.example.com"}}`,
		`{"records":[{"type":"MX","name":"@","value":"mail.example.com"}]}`,
		`{"NS":{"@":"ns1.example.com"},"SOA":{"@":"ns1.example.com"}}`,
	}
	for _, body := range empty {
		records, err := parseZone(body, "example.com")
		if err != nil || len(records) != 0 {
			t.Fatalf("%s -> %#v %v", body, records, err)
		}
	}
}

func TestTXTAndAAAARoundTrip(t *testing.T) {
	records, err := parseZone(`{"TXT":{"www":"\"heritage=external-dns,external-dns/owner=mbono-public\""},"AAAA":{"www":"2001:0db8:0000:0000:0000:0000:0000:0001"}}`, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	foundTXT := false
	foundAAAA := false
	for _, rec := range records {
		if rec.RecordType == "TXT" && len(rec.Targets) == 1 && rec.Targets[0] == "heritage=external-dns,external-dns/owner=mbono-public" {
			foundTXT = true
		}
		if rec.RecordType == "AAAA" && len(rec.Targets) == 1 && rec.Targets[0] == "2001:db8::1" {
			foundAAAA = true
		}
	}
	if !foundTXT || !foundAAAA {
		t.Fatalf("%#v", records)
	}

	spaced, err := parseZone(`{"TXT":{"www":"\"v=spf1 a mx\""}}`, "example.com")
	if err != nil || len(spaced) != 1 || spaced[0].Targets[0] != "v=spf1 a mx" {
		t.Fatalf("%#v %v", spaced, err)
	}
}

func TestSameTargetEditsTTL(t *testing.T) {
	var raw string
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		raw = r.URL.RawQuery
		_, _ = io.WriteString(w, `{"error":"0"}`)
	}))
	defer srv.Close()
	client := NewClient(srv.URL, "user", "secret", []string{"example.com"}, false, 60, nil)
	err := client.ApplyChanges(context.Background(), ChangeSet{
		Delete: []Endpoint{{DNSName: "www.example.com", RecordType: "A", Targets: []string{"203.0.113.10"}, RecordTTL: 14400}},
		Create: []Endpoint{{DNSName: "www.example.com", RecordType: "A", Targets: []string{"203.0.113.10"}, RecordTTL: 60}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("calls %d", calls)
	}
	query, err := url.ParseQuery(raw)
	if err != nil {
		t.Fatal(err)
	}
	if query.Get("action") != "edit" || query.Get("ttl") != "60" || query.Get("name") != "www" || query.Get("value") != "203.0.113.10" {
		t.Fatalf("edit query %#v", query)
	}
	if query.Get("arecs0") != "name=www&value=203.0.113.10" {
		t.Fatalf("selector %q raw %s", query.Get("arecs0"), raw)
	}
	if strings.Contains(raw, "%253D") {
		t.Fatalf("selector encoded twice: %s", raw)
	}

	calls = 0
	err = client.ApplyChanges(context.Background(), ChangeSet{
		Delete: []Endpoint{{DNSName: "www.example.com", RecordType: "A", Targets: []string{"203.0.113.10"}, RecordTTL: 60}},
		Create: []Endpoint{{DNSName: "www.example.com", RecordType: "A", Targets: []string{"203.0.113.10"}, RecordTTL: 60}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("unchanged ttl was written %d times", calls)
	}

	unset := NewClient(srv.URL, "user", "secret", []string{"example.com"}, false, 0, nil)
	err = unset.ApplyChanges(context.Background(), ChangeSet{
		Delete: []Endpoint{{DNSName: "www.example.com", RecordType: "A", Targets: []string{"203.0.113.10"}}},
		Create: []Endpoint{{DNSName: "www.example.com", RecordType: "A", Targets: []string{"203.0.113.10"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("edit with no ttl was sent %d times", calls)
	}
}

func TestEditFallsBackToReplace(t *testing.T) {
	var actions []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		action := r.URL.Query().Get("action")
		actions = append(actions, action)
		if action == "edit" {
			_, _ = io.WriteString(w, `{"error":"1","text":"unknown action"}`)
			return
		}
		_, _ = io.WriteString(w, `{"error":"0"}`)
	}))
	defer srv.Close()
	client := NewClient(srv.URL, "user", "secret", []string{"example.com"}, false, 60, nil)
	err := client.ApplyChanges(context.Background(), ChangeSet{
		Delete: []Endpoint{{DNSName: "www.example.com", RecordType: "A", Targets: []string{"203.0.113.10"}, RecordTTL: 14400}},
		Create: []Endpoint{{DNSName: "www.example.com", RecordType: "A", Targets: []string{"203.0.113.10"}, RecordTTL: 60}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(actions, ",") != "edit,select,add" {
		t.Fatalf("actions %v", actions)
	}
}

func TestIdempotentAddAndDelete(t *testing.T) {
	phrases := []struct{ op, text string }{
		{"add", "Cannot Add Record - It Already Exists"},
		{"add", "duplicate"},
		{"delete", "It Does Not Exist"},
		{"delete", "doesn't exist"},
		{"delete", "no such record"},
		{"delete", "not found"},
		{"delete", "unable to find"},
	}
	for _, tc := range phrases {
		err := ignoreIdempotent(tc.op, &panelError{text: tc.text})
		if err != nil {
			t.Fatalf("%s %q: %v", tc.op, tc.text, err)
		}
	}
	if err := ignoreIdempotent("add", &panelError{text: "cannot add"}); err == nil {
		t.Fatal("real add failure was ignored")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("action") == "add" {
			_, _ = io.WriteString(w, `{"error":"1","text":"Cannot Add Record - It Already Exists"}`)
			return
		}
		_, _ = io.WriteString(w, `{"error":"1","text":"Cannot Delete Record - It Does Not Exist"}`)
	}))
	defer srv.Close()
	client := NewClient(srv.URL, "user", "secret", []string{"example.com"}, false, 60, nil)
	err := client.ApplyChanges(context.Background(), ChangeSet{
		Create: []Endpoint{{DNSName: "www.example.com", RecordType: "A", Targets: []string{"203.0.113.10"}}},
		Delete: []Endpoint{{DNSName: "gone.example.com", RecordType: "A", Targets: []string{"203.0.113.11"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRetryTransientAndContinueNames(t *testing.T) {
	var lists int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lists++
		if lists == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, `{"A":{}}`)
	}))
	defer srv.Close()
	client := NewClient(srv.URL, "user", "secret", []string{"example.com"}, false, 60, nil)
	if _, err := client.Records(context.Background()); err != nil {
		t.Fatal(err)
	}
	if lists != 2 {
		t.Fatalf("list attempts %d", lists)
	}

	var names []string
	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		names = append(names, name)
		if name == "bad" {
			_, _ = io.WriteString(w, `{"error":"1","text":"cannot add"}`)
			return
		}
		_, _ = io.WriteString(w, `{"error":"0"}`)
	}))
	defer panel.Close()
	client = NewClient(panel.URL, "user", "secret", []string{"example.com"}, false, 60, nil)
	err := client.ApplyChanges(context.Background(), ChangeSet{Create: []Endpoint{
		{DNSName: "bad.example.com", RecordType: "A", Targets: []string{"203.0.113.1"}},
		{DNSName: "good.example.com", RecordType: "A", Targets: []string{"203.0.113.2"}},
	}})
	if err == nil || !strings.Contains(err.Error(), "cannot add") {
		t.Fatalf("got %v", err)
	}
	if strings.Join(names, ",") != "bad,good" {
		t.Fatalf("names %v", names)
	}
}

func TestWildcardAAAAAndZoneCase(t *testing.T) {
	var query url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query()
		_, _ = io.WriteString(w, `{"error":"0"}`)
	}))
	defer srv.Close()
	client := NewClient(srv.URL, "user", "secret", []string{"Example.com"}, false, 60, nil)
	err := client.ApplyChanges(context.Background(), ChangeSet{Create: []Endpoint{{
		DNSName: "*.example.com", RecordType: "AAAA", Targets: []string{"2001:0db8::1"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if query.Get("domain") != "example.com" || query.Get("name") != "*" || query.Get("value") != "2001:db8::1" {
		t.Fatalf("query %#v", query)
	}
}

func TestInfoLogsOmitRecordValues(t *testing.T) {
	spy := &infoSpy{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"error":"0"}`)
	}))
	defer srv.Close()
	client := NewClient(srv.URL, "user", "secret", []string{"example.com"}, false, 60, slog.New(spy))
	err := client.ApplyChanges(context.Background(), ChangeSet{Create: []Endpoint{{
		DNSName: "www.example.com", RecordType: "TXT", Targets: []string{"heritage=external-dns,external-dns/owner=mbono-public"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range spy.keys() {
		if key == "value" || key == "body" {
			t.Fatalf("info log included %s", key)
		}
	}
}

type infoSpy struct {
	mu   sync.Mutex
	seen []string
}

func (s *infoSpy) Enabled(context.Context, slog.Level) bool { return true }

func (s *infoSpy) Handle(_ context.Context, r slog.Record) error {
	if r.Level != slog.LevelInfo {
		return nil
	}
	r.Attrs(func(a slog.Attr) bool {
		s.mu.Lock()
		s.seen = append(s.seen, a.Key)
		s.mu.Unlock()
		return true
	})
	return nil
}

func (s *infoSpy) WithAttrs([]slog.Attr) slog.Handler { return s }

func (s *infoSpy) WithGroup(string) slog.Handler { return s }

func TestOmittedTTLUsesDefault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"A":{"www":"203.0.113.10","mail":{"value":"203.0.113.11","ttl":14400}}}`)
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "user", "secret", []string{"example.com"}, false, 60, nil)
	records, err := client.Records(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for _, rec := range records {
		if len(rec.Targets) != 1 {
			t.Fatalf("%#v", rec)
		}
		got[rec.Targets[0]] = rec.RecordTTL
	}
	if got["203.0.113.10"] != 60 || got["203.0.113.11"] != 14400 {
		t.Fatalf("ttl %#v", got)
	}

	unset := NewClient(srv.URL, "user", "secret", []string{"example.com"}, false, 0, nil)
	records, err = unset.Records(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got = map[string]int64{}
	for _, rec := range records {
		got[rec.Targets[0]] = rec.RecordTTL
	}
	if got["203.0.113.10"] != 0 || got["203.0.113.11"] != 14400 {
		t.Fatalf("ttl with DA_TTL=0 %#v", got)
	}
}

func TestUnrecognizedMutationBodyIsAnError(t *testing.T) {
	bodies := []string{
		"<html><body>login</body></html>",
		"text=please+log+in",
		`{"message":"please log in"}`,
	}
	for _, body := range bodies {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, body)
		}))
		client := NewClient(srv.URL, "user", "secret", []string{"example.com"}, false, 60, nil)
		err := client.ApplyChanges(context.Background(), ChangeSet{Create: []Endpoint{{
			DNSName: "www.example.com", RecordType: "A", Targets: []string{"203.0.113.10"},
		}}})
		srv.Close()
		if err == nil || !strings.Contains(err.Error(), "unrecognized") {
			t.Fatalf("body %q got %v", body, err)
		}
	}
}

func TestRedirectIsNotFollowed(t *testing.T) {
	followed := false
	next := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		followed = true
		_, _ = io.WriteString(w, `{"error":"0"}`)
	}))
	defer next.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, next.URL+"/CMD_API_DNS_CONTROL", http.StatusFound)
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "user", "secret", []string{"example.com"}, false, 60, nil)
	err := client.ApplyChanges(context.Background(), ChangeSet{Create: []Endpoint{{
		DNSName: "www.example.com", RecordType: "A", Targets: []string{"203.0.113.10"},
	}}})
	if err == nil {
		t.Fatal("expected redirect to fail")
	}
	if followed {
		t.Fatal("redirect was followed")
	}
	if !PanelUnavailable(err) {
		t.Fatal("redirect should clear readiness")
	}
}

func TestRetryAfterIsHonored(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "10")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = io.WriteString(w, `{"A":{}}`)
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "user", "secret", []string{"example.com"}, false, 60, nil)
	var waited time.Duration
	client.sleep = func(ctx context.Context, d time.Duration) error {
		waited = d
		return nil
	}
	if _, err := client.Records(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls %d", calls)
	}
	if waited < 10*time.Second || waited > maxRetryWait {
		t.Fatalf("waited %s", waited)
	}
}

func (s *infoSpy) keys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.seen...)
}
