package directadmin

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const maxAttempts = 3

// Observer records DirectAdmin calls and whether a list or apply finished.
// Begin marks the start of a list or apply. Finish applies that outcome.
// A finish from an older call does not clear a newer result.
type Observer interface {
	Observe(operation, result string, seconds float64)
	Begin() uint64
	Finish(operation string, token uint64, err error)
}

// Client calls the DirectAdmin DNS API.
type Client struct {
	baseURL  string
	user     string
	key      string
	zones    []string
	ttl      int64
	http     *http.Client
	log      *slog.Logger
	observer Observer
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

// UseObserver records API calls and list/apply outcomes. nil clears it.
func (c *Client) UseObserver(observer Observer) {
	c.observer = observer
}

// Zones returns the configured DirectAdmin domains.
func (c *Client) Zones() []string {
	return append([]string(nil), c.zones...)
}

// Records lists A, AAAA, CNAME, and TXT records in every configured zone.
func (c *Client) Records(ctx context.Context) ([]Endpoint, error) {
	token := c.begin()
	var all []Endpoint
	for _, zone := range c.zones {
		body, err := c.do(ctx, url.Values{"domain": {zone}, "json": {"yes"}})
		if err != nil {
			c.finish("list", token, err)
			return nil, err
		}
		records, err := parseZone(body, zone)
		if err != nil {
			c.finish("list", token, err)
			return nil, err
		}
		all = append(all, records...)
	}
	if all == nil {
		all = []Endpoint{}
	}
	c.finish("list", token, nil)
	return all, nil
}

// ApplyChanges writes one name at a time. A new target is added before the old
// one is removed, so the name keeps answering while both values exist. The
// same target with a new TTL is edited in place. A name that is only going
// away is deleted. One name failing does not skip the remaining names.
func (c *Client) ApplyChanges(ctx context.Context, changes ChangeSet) error {
	token := c.begin()
	c.log.Info("apply changes", "create", len(changes.Create), "delete", len(changes.Delete))
	var errs []error
	for _, change := range planChanges(changes) {
		if err := c.applyName(ctx, change); err != nil {
			errs = append(errs, fmt.Errorf("%s %s: %w", change.recordType, change.dnsName, err))
		}
	}
	err := errors.Join(errs...)
	c.finish("apply", token, err)
	return err
}

type recordChange struct {
	recordType   string
	dnsName      string
	ttl          int64
	deleteTTL    int64
	hasDeleteTTL bool
	create       []string
	delete       []string
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
		recordType := strings.ToUpper(ep.RecordType)
		dnsName := strings.ToLower(strings.TrimSuffix(ep.DNSName, "."))
		targets := make([]string, 0, len(ep.Targets))
		for _, target := range ep.Targets {
			targets = append(targets, canonicalTarget(recordType, target))
		}
		key := recordType + " " + dnsName
		change := grouped[key]
		if change == nil {
			change = &recordChange{recordType: recordType, dnsName: dnsName}
			grouped[key] = change
			order = append(order, key)
		}
		if create {
			change.ttl = ep.RecordTTL
			change.create = append(change.create, targets...)
			return
		}
		change.hasDeleteTTL = true
		change.deleteTTL = ep.RecordTTL
		change.delete = append(change.delete, targets...)
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

func (c *Client) applyName(ctx context.Context, change recordChange) error {
	zone, ok := matchZone(change.dnsName, c.zones)
	if !ok {
		return fmt.Errorf("%s %s matches no configured zone", change.recordType, change.dnsName)
	}
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
		if err := c.write(ctx, "add", zone, change.recordType, change.dnsName, target, c.recordTTL(change.endpoint(target))); err != nil {
			return err
		}
	}
	for _, target := range change.delete {
		if _, same := inCreate[target]; !same {
			continue
		}
		if err := c.editTarget(ctx, zone, change, target); err != nil {
			return err
		}
	}
	for _, target := range change.delete {
		if _, same := inCreate[target]; same {
			continue
		}
		if err := c.write(ctx, "delete", zone, change.recordType, change.dnsName, target, 0); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) editTarget(ctx context.Context, zone string, change recordChange, target string) error {
	desired := c.recordTTL(change.endpoint(target))
	if desired == 0 || (change.hasDeleteTTL && desired == change.deleteTTL) {
		c.log.Debug("directadmin skip unchanged ttl", "type", change.recordType, "name", change.dnsName, "ttl", desired)
		return nil
	}
	err := c.write(ctx, "edit", zone, change.recordType, change.dnsName, target, desired)
	if err == nil || !editRejected(err) {
		return err
	}
	c.log.Info("directadmin edit rejected", "type", change.recordType, "name", change.dnsName, "zone", zone, "err", err)
	if err := c.write(ctx, "delete", zone, change.recordType, change.dnsName, target, 0); err != nil {
		return err
	}
	return c.write(ctx, "add", zone, change.recordType, change.dnsName, target, desired)
}

func (c *Client) recordTTL(ep Endpoint) int64 {
	if ep.RecordTTL > 0 {
		return ep.RecordTTL
	}
	return c.ttl
}

func (c *Client) write(ctx context.Context, op, zone, recordType, dnsName, value string, ttl int64) error {
	var (
		body string
		err  error
	)
	switch op {
	case "add":
		body, err = c.add(ctx, zone, recordType, dnsName, value, ttl)
	case "delete":
		body, err = c.delete(ctx, zone, recordType, dnsName, value)
	case "edit":
		body, err = c.edit(ctx, zone, recordType, dnsName, value, ttl)
	default:
		err = fmt.Errorf("unknown mutation %s", op)
	}
	if err == nil {
		err = mutationError(body)
	}
	if op != "edit" {
		err = ignoreIdempotent(op, err)
	}
	c.logMutation(op, zone, recordType, dnsName, value, body, ttl, err)
	return err
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
	return c.do(ctx, q)
}

func (c *Client) edit(ctx context.Context, zone, recordType, dnsName, value string, ttl int64) (string, error) {
	key, err := selectKey(recordType)
	if err != nil {
		return "", err
	}
	canonical := canonicalTarget(recordType, value)
	name := relative(dnsName, zone)
	// url.Values.Encode quotes the selector once. Quoting it beforehand makes
	// DirectAdmin look up a literal "%3D" and leave the record in place.
	q := url.Values{}
	q.Set("domain", zone)
	q.Set("action", "edit")
	q.Set("type", recordType)
	q.Set(key, "name="+name+"&value="+canonical)
	q.Set("name", name)
	q.Set("value", canonical)
	if ttl > 0 {
		q.Set("ttl", strconv.FormatInt(ttl, 10))
	}
	q.Set("json", "yes")
	return c.do(ctx, q)
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
	return c.do(ctx, q)
}

func (c *Client) do(ctx context.Context, query url.Values) (string, error) {
	op := operation(query)
	var last error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			if err := pause(ctx, time.Duration(attempt)*100*time.Millisecond); err != nil {
				return "", err
			}
		}
		start := time.Now()
		body, err := c.doOnce(ctx, query)
		c.observe(op, resultOf(err), time.Since(start).Seconds())
		if err == nil || !retryable(err) {
			return body, err
		}
		last = err
	}
	return "", last
}

func (c *Client) doOnce(ctx context.Context, query url.Values) (string, error) {
	endpoint := c.baseURL + "/CMD_API_DNS_CONTROL?" + query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(c.user, c.key)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", scrubTransport(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 300 {
		c.log.Debug("directadmin http error", "status", resp.StatusCode, "body", clip(string(body)))
		return "", &statusError{code: resp.StatusCode}
	}
	return string(body), nil
}

func operation(query url.Values) string {
	switch query.Get("action") {
	case "":
		return "list"
	case "select":
		return "delete"
	default:
		return query.Get("action")
	}
}

func resultOf(err error) string {
	if err == nil {
		return "ok"
	}
	return "error"
}

func retryable(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var status *statusError
	if errors.As(err, &status) {
		return status.code == http.StatusTooManyRequests || status.code >= 500
	}
	var panel *panelError
	if errors.As(err, &panel) {
		return false
	}
	return true
}

func pause(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func scrubTransport(err error) error {
	var uerr *url.Error
	if !errors.As(err, &uerr) {
		return err
	}
	loc := uerr.URL
	if u, perr := url.Parse(uerr.URL); perr == nil {
		u.RawQuery = ""
		u.User = nil
		loc = u.String()
	}
	return fmt.Errorf("%s %s: %w", uerr.Op, loc, uerr.Err)
}

type statusError struct {
	code int
}

func (e *statusError) Error() string {
	return fmt.Sprintf("directadmin http %d", e.code)
}

type panelError struct {
	text string
}

func (e *panelError) Error() string {
	return "directadmin: " + e.text
}

// idempotentPhrases are matched case-insensitively against the panel text.
// A new panel string is a one-line addition.
var idempotentPhrases = map[string][]string{
	"add": {
		"already exists",
		"duplicate",
	},
	"delete": {
		"does not exist",
		"doesn't exist",
		"no such",
		"not found",
		"unable to find",
	},
}

func ignoreIdempotent(op string, err error) error {
	if err == nil {
		return nil
	}
	var panel *panelError
	if !errors.As(err, &panel) {
		return err
	}
	text := strings.ToLower(panel.text)
	for _, phrase := range idempotentPhrases[op] {
		if strings.Contains(text, phrase) {
			return nil
		}
	}
	return err
}

func editRejected(err error) bool {
	var panel *panelError
	return errors.As(err, &panel)
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
		return &panelError{text: text}
	}
	return nil
}

func (c *Client) logMutation(op, zone, recordType, dnsName, value, body string, ttl int64, err error) {
	result := "ok"
	if err != nil {
		result = "error"
	}
	attrs := []any{"op", op, "type", recordType, "name", dnsName, "zone", zone, "result", result}
	if ttl > 0 {
		attrs = append(attrs, "ttl", ttl)
	}
	if err != nil {
		attrs = append(attrs, "err", err)
	}
	c.log.Info("directadmin", attrs...)
	c.log.Debug("directadmin detail", "op", op, "name", dnsName, "value", value, "body", clip(body))
}

func clip(body string) string {
	if len(body) > 200 {
		return body[:200]
	}
	return body
}

func (c *Client) observe(operation, result string, seconds float64) {
	if c.observer != nil {
		c.observer.Observe(operation, result, seconds)
	}
}

func (c *Client) begin() uint64 {
	if c.observer == nil {
		return 0
	}
	return c.observer.Begin()
}

func (c *Client) finish(operation string, token uint64, err error) {
	if c.observer != nil {
		c.observer.Finish(operation, token, err)
	}
}
