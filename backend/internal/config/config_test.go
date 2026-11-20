package config

import (
	"testing"
)

func TestEventSlug(t *testing.T) {
	c := &Config{PMSeriesSlug: "btc-updown-5m"}
	if c.EventSlug(1795090500) != "btc-updown-5m-1795090500" {
		t.Fatal(c.EventSlug(1795090500))
	}
}

func TestStrategyProfileDefaultsAllowed(t *testing.T) {
	t.Setenv("DASHBOARD_PASSWORD", "x")
	t.Setenv("LIGHTER_API_KEY_INDEX", "4")
	t.Setenv("LIGHTER_NETWORK", "mainnet")
	t.Setenv("LIGHTER_LEVERAGE", "1")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Leverage != 1 {
		t.Fatalf("got %v", cfg.Leverage)
	}
}

func TestStrategyProfileRejectsLeverageOverride(t *testing.T) {
	t.Setenv("DASHBOARD_PASSWORD", "x")
	t.Setenv("LIGHTER_API_KEY_INDEX", "4")
	t.Setenv("LIGHTER_NETWORK", "mainnet")
	t.Setenv("LIGHTER_LEVERAGE", "3")
	if _, err := Load(); err == nil {
		t.Fatal("expected reject")
	}
}
