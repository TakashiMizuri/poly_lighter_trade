package config

import (
	"testing"
	"time"
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
	t.Setenv("DRY_RUN", "true")
	t.Setenv("LIGHTER_API_PRIVATE_KEY", "")
	t.Setenv("LIGHTER_ACCOUNT_INDEX", "0")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Leverage != 1 {
		t.Fatalf("got %v", cfg.Leverage)
	}
	if cfg.ExitEarly != 1500*time.Millisecond {
		t.Fatalf("exit early %s", cfg.ExitEarly)
	}
}

func TestExitEarlyRejectsOld300ms(t *testing.T) {
	t.Setenv("DASHBOARD_PASSWORD", "x")
	t.Setenv("LIGHTER_API_KEY_INDEX", "4")
	t.Setenv("LIGHTER_NETWORK", "mainnet")
	t.Setenv("LIGHTER_LEVERAGE", "1")
	t.Setenv("DRY_RUN", "true")
	t.Setenv("EXIT_EARLY_SECONDS", "0.30")
	if _, err := Load(); err == nil {
		t.Fatal("expected reject of 0.30")
	}
}

func TestStrategyProfileRejectsLeverageOverride(t *testing.T) {
	t.Setenv("DASHBOARD_PASSWORD", "x")
	t.Setenv("LIGHTER_API_KEY_INDEX", "4")
	t.Setenv("LIGHTER_NETWORK", "mainnet")
	t.Setenv("LIGHTER_LEVERAGE", "3")
	t.Setenv("DRY_RUN", "true")
	if _, err := Load(); err == nil {
		t.Fatal("expected reject")
	}
}

func TestLiveRequiresAccountAndKey(t *testing.T) {
	t.Setenv("DASHBOARD_PASSWORD", "x")
	t.Setenv("LIGHTER_API_KEY_INDEX", "4")
	t.Setenv("LIGHTER_NETWORK", "mainnet")
	t.Setenv("LIGHTER_LEVERAGE", "1")
	t.Setenv("DRY_RUN", "false")
	t.Setenv("LIGHTER_API_PRIVATE_KEY", "")
	t.Setenv("LIGHTER_ACCOUNT_INDEX", "0")
	if _, err := Load(); err == nil {
		t.Fatal("expected reject without key/account")
	}
}
