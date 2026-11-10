package directadmin

import (
	"fmt"
	"net/netip"
	"strings"
)

// Endpoint is one ExternalDNS record. Multiple values share Targets.
type Endpoint struct {
	DNSName    string   `json:"dnsName"`
	Targets    []string `json:"targets"`
	RecordType string   `json:"recordType"`
	RecordTTL  int64    `json:"recordTTL"`
}

var typeSelect = map[string]string{
	"A":     "arecs",
	"AAAA":  "aaaarecs",
	"CNAME": "cnamerecs",
	"TXT":   "txtrecs",
}

func supported(recordType string) bool {
	_, ok := typeSelect[recordType]
	return ok
}

func selectKey(recordType string) (string, error) {
	prefix, ok := typeSelect[recordType]
	if !ok {
		return "", fmt.Errorf("unsupported record type %q", recordType)
	}
	// DirectAdmin selects the record with <type>recs0=name=...&value=...
	return prefix + "0", nil
}

// matchZone returns the longest configured zone that owns name.
func matchZone(name string, zones []string) (string, bool) {
	name = strings.TrimSuffix(strings.ToLower(name), ".")
	var best string
	for _, zone := range zones {
		z := strings.TrimSuffix(strings.ToLower(zone), ".")
		if name == z || strings.HasSuffix(name, "."+z) {
			if len(z) > len(best) {
				best = z
			}
		}
	}
	return best, best != ""
}

func fqdn(name, zone string) string {
	name = strings.TrimSuffix(name, ".")
	var out string
	switch {
	case name == "" || name == "@" || strings.EqualFold(name, zone):
		out = zone
	case strings.HasSuffix(strings.ToLower(name), "."+strings.ToLower(zone)):
		out = name
	default:
		out = name + "." + zone
	}
	return strings.ToLower(out)
}

// canonicalTarget normalizes a value before it is compared or written.
// CNAME targets that already contain a dot become absolute: DirectAdmin treats
// a CNAME without a trailing dot as relative to the zone, so "foo.other.com"
// would be stored as foo.other.com.<zone>. A single label is left unchanged.
// AAAA targets use the compressed textual form so the list and the plan match.
func canonicalTarget(recordType, value string) string {
	value = strings.TrimSpace(value)
	switch strings.ToUpper(recordType) {
	case "AAAA":
		addr, err := netip.ParseAddr(value)
		if err != nil {
			return value
		}
		return addr.String()
	case "CNAME":
		if value == "" || strings.HasSuffix(value, ".") || !strings.Contains(value, ".") {
			return value
		}
		return value + "."
	default:
		return value
	}
}

func relative(dnsName, zone string) string {
	name := strings.TrimSuffix(strings.ToLower(dnsName), ".")
	zone = strings.TrimSuffix(strings.ToLower(zone), ".")
	if name == zone || name == "@" {
		return "@"
	}
	suffix := "." + zone
	if strings.HasSuffix(name, suffix) {
		return strings.TrimSuffix(name, suffix)
	}
	return name
}

func stripQuotes(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 {
		if q := value[0]; (q == '"' || q == '\'') && value[len(value)-1] == q {
			return value[1 : len(value)-1]
		}
	}
	return value
}
