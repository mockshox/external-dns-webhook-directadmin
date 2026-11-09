// Package config reads the webhook process environment.
package config

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	defaultWebhook = "127.0.0.1:8888"
	defaultHealth  = "0.0.0.0:8080"
)

// Config is the process configuration.
type Config struct {
	URL            string
	Username       string
	LoginKey       string
	Zones          []string
	Insecure       bool
	TTL            int64
	WebhookAddress string
	HealthAddress  string
}

// Load reads configuration from getenv, which is usually os.Getenv.
func Load(getenv func(string) string) (Config, error) {
	cfg := Config{
		URL:            strings.TrimRight(strings.TrimSpace(getenv("DA_URL")), "/"),
		Username:       getenv("DA_USERNAME"),
		LoginKey:       getenv("DA_LOGIN_KEY"),
		Insecure:       truthy(getenv("DA_INSECURE")),
		WebhookAddress: valueOr(getenv("WEBHOOK_ADDRESS"), defaultWebhook),
		HealthAddress:  valueOr(getenv("HEALTH_ADDRESS"), defaultHealth),
	}
	zones := splitList(getenv("DA_DOMAINS"))
	if len(zones) == 0 {
		zones = splitList(getenv("DA_DOMAIN"))
	}
	filtered, err := restrictZones(zones, splitList(getenv("DOMAIN_FILTER")))
	if err != nil {
		return Config{}, err
	}
	cfg.Zones = filtered
	ttl, err := ttlSeconds(getenv("DA_TTL"))
	if err != nil {
		return Config{}, err
	}
	cfg.TTL = ttl

	var missing []string
	if cfg.URL == "" {
		missing = append(missing, "DA_URL")
	}
	if cfg.Username == "" {
		missing = append(missing, "DA_USERNAME")
	}
	if cfg.LoginKey == "" {
		missing = append(missing, "DA_LOGIN_KEY")
	}
	if len(cfg.Zones) == 0 {
		missing = append(missing, "DA_DOMAINS or DA_DOMAIN")
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("missing required configuration: %s", strings.Join(missing, ", "))
	}
	return cfg, nil
}

func restrictZones(zones, filter []string) ([]string, error) {
	if len(filter) == 0 {
		return zones, nil
	}
	allowed := map[string]struct{}{}
	for _, f := range filter {
		allowed[strings.ToLower(f)] = struct{}{}
	}
	var out []string
	for _, zone := range zones {
		if _, ok := allowed[strings.ToLower(zone)]; ok {
			out = append(out, zone)
		}
	}
	if len(zones) > 0 && len(out) == 0 {
		return nil, fmt.Errorf("DOMAIN_FILTER does not match any configured zone")
	}
	return out, nil
}

func splitList(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(strings.TrimSuffix(part, "."))
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func ttlSeconds(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 60, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("DA_TTL must be a non-negative number of seconds")
	}
	return n, nil
}

func valueOr(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

func truthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}
