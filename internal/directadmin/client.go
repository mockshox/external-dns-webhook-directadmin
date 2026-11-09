package directadmin

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Client calls the DirectAdmin DNS API.
type Client struct {
	baseURL string
	user    string
	key     string
	zones   []string
	ttl     int64
	http    *http.Client
	log     *slog.Logger
}

// NewClient builds a client. zones are the DirectAdmin domain names to manage.
// ttl is the record TTL, in seconds, used when ExternalDNS omits recordTTL.
// Zero leaves the panel default in place.
func NewClient(baseURL, user, key string, zones []string, insecure bool, ttl int64, logger *slog.Logger) *Client {
	if logger == nil {
		logger = slog.Default()
	}
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		base = &http.Transport{}
	}
	transport := base.Clone()
	if insecure {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // explicit DA_INSECURE opt-in
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		user:    user,
		key:     key,
		zones:   append([]string(nil), zones...),
		ttl:     ttl,
		http:    &http.Client{Timeout: 30 * time.Second, Transport: transport},
		log:     logger,
	}
}

// Zones returns the configured DirectAdmin domains.
func (c *Client) Zones() []string {
	return append([]string(nil), c.zones...)
}

// Records lists A, AAAA, CNAME, and TXT records in every configured zone.
func (c *Client) Records(ctx context.Context) ([]Endpoint, error) {
	var all []Endpoint
	for _, zone := range c.zones {
		body, err := c.do(ctx, url.Values{"domain": {zone}, "json": {"yes"}})
		if err != nil {
			return nil, err
		}
		records, err := parseZone(body, zone)
		if err != nil {
			return nil, err
		}
		all = append(all, records...)
	}
	if all == nil {
		all = []Endpoint{}
	}
	return all, nil
}

// ApplyChanges writes one name at a time. A replacement is added before the
// old target is removed, so the name keeps answering while DirectAdmin has
// both values. A name that is only going away is deleted. Replacing the same
// target (a TTL change) deletes that one value and adds it back.
func (c *Client) ApplyChanges(ctx context.Context, changes ChangeSet) error {
	c.log.Info("apply changes", "create", len(changes.Create), "delete", len(changes.Delete))
	for _, change := range planChanges(changes) {
		inDelete := map[string]struct{}{}
		for _, target := range change.delete {
			inDelete[target] = struct{}{}
		}
		inCreate := map[string]struct{}{}
		for _, target := range change.create {
			inCreate[target] = struct{}{}
		}
		for _, target := range change.create {
			if _, same := inDelete[target]; same {
				continue
			}
			if err := c.mutate(ctx, change.endpoint(target), true); err != nil {
				return err
			}
		}
		for _, target := range change.delete {
			if _, same := inCreate[target]; !same {
				continue
			}
			if err := c.mutate(ctx, change.endpoint(target), false); err != nil {
				return err
			}
			if err := c.mutate(ctx, change.endpoint(target), true); err != nil {
				return err
			}
		}
		for _, target := range change.delete {
			if _, same := inCreate[target]; same {
				continue
			}
			if err := c.mutate(ctx, change.endpoint(target), false); err != nil {
				return err
			}
		}
	}
	return nil
}

type recordChange struct {
	recordType string
	dnsName    string
	ttl        int64
	create     []string
	delete     []string
}

func (c recordChange) endpoint(target string) Endpoint {
	return Endpoint{
		DNSName:    c.dnsName,
		RecordType: c.recordType,
		RecordTTL:  c.ttl,
		Targets:    []string{target},
	}
}

func planChanges(changes ChangeSet) []recordChange {
	order := []string{}
	grouped := map[string]*recordChange{}
	note := func(ep Endpoint, create bool) {
		if ep.RecordType == "" || len(ep.Targets) == 0 {
			return
		}
		key := ep.RecordType + " " + ep.DNSName
		change := grouped[key]
		if change == nil {
			change = &recordChange{recordType: ep.RecordType, dnsName: ep.DNSName}
			grouped[key] = change
			order = append(order, key)
		}
		if create {
			change.ttl = ep.RecordTTL
			change.create = append(change.create, ep.Targets...)
			return
		}
		change.delete = append(change.delete, ep.Targets...)
	}
	for _, ep := range changes.Create {
		note(ep, true)
	}
	for _, ep := range changes.Delete {
		note(ep, false)
	}
	out := make([]recordChange, 0, len(order))
	for _, key := range order {
		out = append(out, *grouped[key])
	}
	return out
}

func (c *Client) mutate(ctx context.Context, ep Endpoint, create bool) error {
	if ep.RecordType == "" || len(ep.Targets) == 0 {
		return nil
	}
	zone, ok := matchZone(ep.DNSName, c.zones)
	if !ok {
		return fmt.Errorf("%s %s matches no configured zone", ep.RecordType, ep.DNSName)
	}
	for _, target := range ep.Targets {
		var (
			body string
			err  error
		)
		if create {
			body, err = c.add(ctx, zone, ep.RecordType, ep.DNSName, target, c.recordTTL(ep))
		} else {
			body, err = c.delete(ctx, zone, ep.RecordType, ep.DNSName, target)
		}
		if err != nil {
			return err
		}
		if err := mutationError(body); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) recordTTL(ep Endpoint) int64 {
	if ep.RecordTTL > 0 {
		return ep.RecordTTL
	}
	return c.ttl
}

func (c *Client) add(ctx context.Context, zone, recordType, dnsName, value string, ttl int64) (string, error) {
	if !supported(recordType) {
		return "", fmt.Errorf("unsupported record type %q", recordType)
	}
	q := url.Values{}
	q.Set("domain", zone)
	q.Set("action", "add")
	q.Set("type", recordType)
	q.Set("name", relative(dnsName, zone))
	q.Set("value", canonicalTarget(recordType, value))
	if ttl > 0 {
		q.Set("ttl", strconv.FormatInt(ttl, 10))
	}
	q.Set("json", "yes")
	body, err := c.do(ctx, q)
	if err != nil {
		return "", err
	}
	c.log.Info("directadmin add", "type", recordType, "name", dnsName, "value", value, "ttl", ttl, "body", clip(body))
	return body, nil
}

func (c *Client) delete(ctx context.Context, zone, recordType, dnsName, value string) (string, error) {
	key, err := selectKey(recordType)
	if err != nil {
		return "", err
	}
	// url.Values.Encode quotes this selector once. Quoting it beforehand makes
	// DirectAdmin look up a literal "%3D" and leave the record in place.
	q := url.Values{}
	q.Set("domain", zone)
	q.Set("action", "select")
	q.Set(key, "name="+relative(dnsName, zone)+"&value="+canonicalTarget(recordType, value))
	q.Set("json", "yes")
	body, err := c.do(ctx, q)
	if err != nil {
		return "", err
	}
	c.log.Info("directadmin delete", "type", recordType, "name", dnsName, "value", value, "body", clip(body))
	return body, nil
}

func (c *Client) do(ctx context.Context, query url.Values) (string, error) {
	endpoint := c.baseURL + "/CMD_API_DNS_CONTROL?" + query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(c.user, c.key)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("directadmin http %s: %s", resp.Status, clip(string(body)))
	}
	return string(body), nil
}

func mutationError(body string) error {
	body = strings.TrimSpace(body)
	if body == "" {
		return nil
	}
	if strings.HasPrefix(body, "{") || strings.HasPrefix(body, "[") {
		var payload any
		if err := jsonUnmarshal(body, &payload); err != nil {
			return fmt.Errorf("directadmin: %w", err)
		}
		return apiError(payload)
	}
	vals, err := url.ParseQuery(body)
	if err != nil {
		return nil
	}
	if e := vals.Get("error"); e != "" && e != "0" {
		text := vals.Get("text")
		if text == "" {
			text = body
		}
		return fmt.Errorf("directadmin: %s", text)
	}
	return nil
}

func clip(body string) string {
	if len(body) > 200 {
		return body[:200]
	}
	return body
}
