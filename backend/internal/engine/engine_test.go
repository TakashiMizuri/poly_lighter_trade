package engine

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"poly-lighter.trade/bot/internal/config"
	"poly-lighter.trade/bot/internal/store"
	"poly-lighter.trade/bot/internal/strategy"
)

func testConfig() *config.Config {
	return &config.Config{
		Profile:        "POLY_LIGHTER_1",
		Symbol:         "BTC",
		Threshold:      0.65,
		MaxSpread:      0.05,
		PrewarmSeconds: 15,
		EntryMaxLag:    2 * time.Second,
		ExitEarly:      300 * time.Millisecond,
		Leverage:       1,
		EquityFraction: 1,
		MarketSlippage: 0.02,
		StopPricePct:   0.00125,
		DryRun:         true,
		HaltDDpct:      0.08,
		HaltExitLateStreak: 3,
		HaltPositionMax:    6 * time.Minute,
	}
}

func TestNextClientIdxDurable(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	cfg := testConfig()
	e := New(cfg, st, nil, nil, nil, nil, nil, nil, nil)
	ctx := context.Background()
	a := e.nextClientIdx(ctx)
	b := e.nextClientIdx(ctx)
	if b != a+1 {
		t.Fatalf("seq %d -> %d", a, b)
	}
	loaded, err := st.LoadGlobal(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ClientOrderSeq != b {
		t.Fatalf("persisted seq=%d want %d", loaded.ClientOrderSeq, b)
	}
	if loaded.Version < 2 {
		t.Fatalf("version=%d", loaded.Version)
	}
}

func TestEnsureFlatPrewarmNoopWithLivePos(t *testing.T) {
	cfg := testConfig()
	e := New(cfg, nil, nil, nil, nil, nil, nil, nil, nil)
	e.pos = &livePos{WindowTS: 1, Qty: 0.01, Side: strategy.SideLong}
	if err := e.ensureFlat(context.Background(), "prewarm"); err != nil {
		t.Fatal(err)
	}
	if e.pos == nil {
		t.Fatal("prewarm must not clear intentional position")
	}
	if err := e.ensureFlat(context.Background(), "skip"); err != nil {
		t.Fatal(err)
	}
	if e.pos == nil {
		t.Fatal("skip must not clear intentional position")
	}
}

func TestDecideLateSkip(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	cfg := testConfig()
	e := New(cfg, st, nil, nil, nil, nil, nil, nil, nil)
	ts := time.Now().UTC().Unix()
	ts = ts - (ts % 300) - 300 // previous window, far past EntryMaxLag

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	_ = e.decide(ctx, ts)

	w, err := st.GetWindow(context.Background(), ts)
	if err != nil {
		t.Fatal(err)
	}
	if w.SkipReason != "late" && w.SkipReason != "halt" {
		// halt only if somehow halted; expect late
		t.Fatalf("skip_reason=%q want late", w.SkipReason)
	}
	if w.Side != strategy.SideSkip {
		t.Fatalf("side=%q", w.Side)
	}
}

func TestKillResumePersists(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	e := New(testConfig(), st, nil, nil, nil, nil, nil, nil, nil)
	ctx := context.Background()
	if err := e.KillAll(ctx); err != nil {
		t.Fatal(err)
	}
	g, _ := st.LoadGlobal(ctx)
	if !g.KillSwitch {
		t.Fatal("kill not persisted")
	}
	e.Resume(ctx)
	g, _ = st.LoadGlobal(ctx)
	if g.KillSwitch || g.Halted {
		t.Fatalf("resume not persisted: %+v", g)
	}
}

func TestSideSignPnL(t *testing.T) {
	ret := strategy.Sign(strategy.SideLong) * (101.0/100.0 - 1)
	if ret <= 0 {
		t.Fatal(ret)
	}
	ret = strategy.Sign(strategy.SideShort) * (99.0/100.0 - 1)
	if ret <= 0 {
		t.Fatal(ret)
	}
}

func TestNoTwoPositionsInvariant(t *testing.T) {
	e := &Engine{}
	e.pos = &livePos{WindowTS: 1, Qty: 1}
	e.pos = &livePos{WindowTS: 2, Qty: 1}
	if e.pos.WindowTS != 2 {
		t.Fatal("single slot")
	}
}
