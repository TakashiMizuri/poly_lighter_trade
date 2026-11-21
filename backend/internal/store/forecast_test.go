package store

import "testing"

func TestBuildForecastCompound(t *testing.T) {
	// 10 bps/trade, 20 trades over ~2 days → ~10 trades/day
	f := BuildForecast(1000, 20, 100, 12, 10, 0.25, 1_000_000, 1_000_000+2*86400)
	if !f.Ready {
		t.Fatal("expected ready")
	}
	if f.TradesPerDay < 9 || f.TradesPerDay > 11 {
		t.Fatalf("trades/day=%v", f.TradesPerDay)
	}
	if f.Equity1W <= 1000 {
		t.Fatalf("1w should grow with +mean: %v", f.Equity1W)
	}
	if f.Equity1Y <= f.Equity1M || f.Equity1M <= f.Equity1W {
		t.Fatalf("horizons not ordered: %v %v %v", f.Equity1W, f.Equity1M, f.Equity1Y)
	}
}

func TestBuildForecastNoTrades(t *testing.T) {
	f := BuildForecast(25, 0, 10, 0, 0, 0, 100, 400)
	if f.Ready {
		t.Fatal("not ready without trades")
	}
	if f.Confidence != "low" {
		t.Fatalf("confidence=%s", f.Confidence)
	}
	if f.Equity1W != 25 {
		t.Fatalf("equity=%v", f.Equity1W)
	}
}

func TestProjectEquityCaps(t *testing.T) {
	if projectEquity(100, -2, 10) <= 0 {
		t.Fatal("should floor mu")
	}
	huge := projectEquity(100, 1, 100) // capped to 5%/trade
	if huge > projectEquity(100, 0.05, 100)+1e-6 {
		t.Fatalf("cap failed %v", huge)
	}
}
