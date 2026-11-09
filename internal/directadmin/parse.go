package directadmin

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

func parseZone(body, zone string) ([]Endpoint, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return nil, fmt.Errorf("directadmin returned an empty zone dump for %s", zone)
	}
	if !strings.HasPrefix(body, "{") && !strings.HasPrefix(body, "[") {
		return nil, fmt.Errorf("directadmin returned a non-JSON zone dump for %s", zone)
	}
	var payload any
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		return nil, fmt.Errorf("directadmin zone %s: %w", zone, err)
	}
	if err := apiError(payload); err != nil {
		return nil, err
	}

	grouped := map[string]*Endpoint{}
	var order []string
	for _, rec := range iterRecords(payload) {
		rec.Type = strings.ToUpper(rec.Type)
		if !supported(rec.Type) {
			continue
		}
		value := canonicalTarget(rec.Type, stripQuotes(rec.Value))
		if value == "" {
			continue
		}
		name := fqdn(rec.Name, zone)
		key := rec.Type + " " + name
		ep := grouped[key]
		if ep == nil {
			ep = &Endpoint{DNSName: name, RecordType: rec.Type, RecordTTL: rec.TTL}
			grouped[key] = ep
			order = append(order, key)
		}
		if !contains(ep.Targets, value) {
			ep.Targets = append(ep.Targets, value)
		}
	}
	out := make([]Endpoint, 0, len(order))
	for _, key := range order {
		out = append(out, *grouped[key])
	}
	return out, nil
}

type rawRecord struct {
	Type  string
	Name  string
	Value string
	TTL   int64
}

func iterRecords(payload any) []rawRecord {
	switch body := payload.(type) {
	case []any:
		var out []rawRecord
		for _, item := range body {
			m, ok := item.(map[string]any)
			if !ok || m["type"] == nil || m["value"] == nil {
				continue
			}
			out = append(out, rawRecord{asString(m["type"]), asString(m["name"]), asString(m["value"]), ttlOf(m)})
		}
		return out
	case map[string]any:
		if err := apiError(body); err != nil && !hasRecords(body) {
			return nil
		}
		if records, ok := body["records"].([]any); ok {
			var out []rawRecord
			for _, item := range records {
				m, ok := item.(map[string]any)
				if !ok || m["value"] == nil {
					continue
				}
				rtype := m["type"]
				if rtype == nil {
					rtype = m["recordType"]
				}
				out = append(out, rawRecord{asString(rtype), asString(m["name"]), asString(m["value"]), ttlOf(m)})
			}
			return out
		}
		if dnsrecs := dnsRecs(body); dnsrecs != nil {
			return iterDNSRecs(dnsrecs)
		}
		return iterTypedMaps(body)
	default:
		return nil
	}
}

func iterDNSRecs(dnsrecs map[string]any) []rawRecord {
	var out []rawRecord
	for rtype, items := range dnsrecs {
		switch typed := items.(type) {
		case []any:
			for _, item := range typed {
				switch row := item.(type) {
				case map[string]any:
					out = append(out, rawRecord{rtype, asString(row["name"]), asString(row["value"]), ttlOf(row)})
				case string:
					out = append(out, rawRecord{rtype, row, "", 0})
				}
			}
		case map[string]any:
			for name, value := range typed {
				switch many := value.(type) {
				case []any:
					for _, v := range many {
						out = append(out, rawRecord{rtype, name, asString(v), 0})
					}
				default:
					out = append(out, rawRecord{rtype, name, asString(value), 0})
				}
			}
		}
	}
	return out
}

func iterTypedMaps(body map[string]any) []rawRecord {
	var out []rawRecord
	for _, rtype := range []string{"A", "AAAA", "CNAME", "TXT"} {
		switch items := body[rtype].(type) {
		case []any:
			for _, item := range items {
				m, ok := item.(map[string]any)
				if !ok {
					continue
				}
				out = append(out, rawRecord{rtype, asString(m["name"]), asString(m["value"]), ttlOf(m)})
			}
		case map[string]any:
			for name, value := range items {
				switch many := value.(type) {
				case []any:
					for _, v := range many {
						out = append(out, rawRecord{rtype, name, asString(v), 0})
					}
				default:
					out = append(out, rawRecord{rtype, name, asString(value), 0})
				}
			}
		}
	}
	return out
}

func dnsRecs(body map[string]any) map[string]any {
	if recs, ok := body["dnsrecs"].(map[string]any); ok {
		return recs
	}
	if recs, ok := body["dns_records"].(map[string]any); ok {
		return recs
	}
	return nil
}

func hasRecords(body map[string]any) bool {
	_, records := body["records"]
	return records || dnsRecs(body) != nil
}

func apiError(payload any) error {
	body, ok := payload.(map[string]any)
	if !ok {
		return nil
	}
	errVal, exists := body["error"]
	if !exists || benignError(errVal) {
		return nil
	}
	if _, ok := body["success"]; ok {
		return nil
	}
	if hasRecords(body) {
		return nil
	}
	msg := body["text"]
	if msg == nil {
		msg = body["message"]
	}
	if msg == nil {
		msg = payload
	}
	return fmt.Errorf("directadmin: %v", msg)
}

func benignError(value any) bool {
	if value == nil {
		return true
	}
	switch asString(value) {
	case "", "0", "none":
		return true
	default:
		return false
	}
}

func ttlOf(record map[string]any) int64 {
	switch typed := record["ttl"].(type) {
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		if err != nil || n < 0 {
			return 0
		}
		return n
	case float64:
		if typed < 0 {
			return 0
		}
		return int64(typed)
	default:
		return 0
	}
}

func asString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case nil:
		return ""
	case float64:
		return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%f", typed), "0"), ".")
	default:
		return fmt.Sprint(typed)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
