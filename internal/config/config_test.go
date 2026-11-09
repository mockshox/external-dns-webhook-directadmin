package config

import "testing"

func TestLoadZonesAndDefaults(t *testing.T) {
	env := map[string]string{
		"DA_URL":       "https://panel.example:2222/",
		"DA_USERNAME":  "user",
		"DA_LOGIN_KEY": "key",
		"DA_DOMAINS":   "example.com, example.org.",
	}
	cfg, err := Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.URL != "https://panel.example:2222" {
		t.Fatalf("url %q", cfg.URL)
	}
	if cfg.Insecure {
		t.Fatal("TLS verification is on unless DA_INSECURE is set")
	}
	if cfg.WebhookAddress != "127.0.0.1:8888" || cfg.HealthAddress != "0.0.0.0:8080" {
		t.Fatalf("addresses webhook=%s health=%s", cfg.WebhookAddress, cfg.HealthAddress)
	}
	if len(cfg.Zones) != 2 || cfg.Zones[0] != "example.com" || cfg.Zones[1] != "example.org" {
		t.Fatalf("zones %#v", cfg.Zones)
	}
	if cfg.TTL != 60 {
		t.Fatalf("default ttl %d", cfg.TTL)
	}
}

func TestLoadTTL(t *testing.T) {
	env := map[string]string{
		"DA_URL":       "https://panel.example:2222",
		"DA_USERNAME":  "user",
		"DA_LOGIN_KEY": "key",
		"DA_DOMAIN":    "example.com",
		"DA_TTL":       "120",
	}
	cfg, err := Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TTL != 120 {
		t.Fatalf("ttl %d", cfg.TTL)
	}
	env["DA_TTL"] = "nope"
	if _, err := Load(func(k string) string { return env[k] }); err == nil {
		t.Fatal("expected invalid DA_TTL")
	}
}

func TestLoadDomainFallbackAndFilter(t *testing.T) {
	env := map[string]string{
		"DA_URL":        "https://panel.example:2222",
		"DA_USERNAME":   "user",
		"DA_LOGIN_KEY":  "key",
		"DA_DOMAIN":     "example.com",
		"DOMAIN_FILTER": "other.com",
	}
	if _, err := Load(func(k string) string { return env[k] }); err == nil {
		t.Fatal("expected filter mismatch")
	}
	env["DOMAIN_FILTER"] = "example.com"
	cfg, err := Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Zones) != 1 || cfg.Zones[0] != "example.com" {
		t.Fatalf("zones %#v", cfg.Zones)
	}
}
