package engine

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"poly-lighter.trade/bot/internal/exchange/lighter"
	"poly-lighter.trade/bot/internal/store"
	"poly-lighter.trade/bot/internal/strategy"
)

// midWindowTS is the current 5m window open, while still >2s before deadline so
// finalizePosition runs the careful flatness / exit_late path (not nextSoon).
func midWindowTS(t *testing.T) int64 {
	t.Helper()
	now := time.Now().UTC().Unix()
	base := now - (now % 300)
	if now-base >= 290 {
		t.Skip("too close to 5m boundary for settle flatness tests")
	}
	return base
}

func TestPlanSettleClose(t *testing.T) {
	flatYes := accountFlat{Known: true, Flat: true}
	flatNo := accountFlat{Known: true, Flat: false}
	unknown := accountFlat{}

	cases := []struct {
		name     string
		reason   string
		flat     accountFlat
		tape     bool
		wantMode settleCloseMode
		wantMkt  bool
		wantCx   bool
	}{
		{"remote_flat", "stop", flatYes, false, settleCloseRemoteFlat, false, true},
		{"residual_open", "stop", flatNo, false, settleCloseResidual, true, true},
		{"unknown_no_tape", "stop", unknown, false, settleCloseKeepStop, true, false},
		{"unknown_with_tape", "stop", unknown, true, settleCloseRemoteFlat, false, true},
		{"open_beats_tape", "stop", flatNo, true, settleCloseResidual, true, true},
		{"window_unknown", "", unknown, false, settleCloseWindow, true, true},
		{"window_flat", "", flatYes, false, settleCloseRemoteFlat, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := planSettleClose(tc.reason, tc.flat, tc.tape)
			if p.Mode != tc.wantMode || p.MarketClose != tc.wantMkt || p.CancelAll != tc.wantCx {
				t.Fatalf("got mode=%d mkt=%v cx=%v want mode=%d mkt=%v cx=%v",
					p.Mode, p.MarketClose, p.CancelAll, tc.wantMode, tc.wantMkt, tc.wantCx)
			}
		})
	}
}

func TestFinalizeExitLateOnlyWhenConfirmedOpen(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	ts := midWindowTS(t)
	ctx := context.Background()
	_ = st.InsertWindow(ctx, store.Window{WindowTS: ts, Side: strategy.SideLong, State: StateInPosition})

	cfg := testConfig()
	e := New(cfg, st, nil, nil, nil, nil, nil, nil, nil)
	e.skipWaitNext = true
	e.global.LateStreak = 0
	e.pxProbe = func(context.Context) (float64, error) { return 60_000, nil }

	pos := &livePos{
		WindowTS: ts, Side: strategy.SideLong, Qty: 0.001, Notional: 60,
		EntryPx: 60_400, EntryTS: float64(ts + 1), EquityBefore: 100,
		StopPx: 60_325, ArmedAt: time.Now().Add(-30 * time.Second), SeenAcctOpen: true,
	}

	// Network error on every account read must NOT count as exit_late.
	e.flatProbe = func(context.Context) (bool, lighter.Position, error) {
		return false, lighter.Position{}, errors.New("dns timeout")
	}
	if err := e.finalizePosition(ctx, pos, "stop", 60_290, 0); err != nil {
		t.Fatal(err)
	}
	w, err := st.GetWindow(ctx, ts)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(w.SkipReason, "exit_late") {
		t.Fatalf("skip_reason=%q must not include exit_late on unknown flatness", w.SkipReason)
	}
	if e.global.LateStreak != 0 {
		t.Fatalf("late_streak=%d want 0", e.global.LateStreak)
	}
	if w.ExitReason != "sl" {
		t.Fatalf("exit_reason=%q want sl", w.ExitReason)
	}
}

func TestFinalizeRemoteStopNoMarketWhenFlat(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	ts := midWindowTS(t)
	ctx := context.Background()
	_ = st.InsertWindow(ctx, store.Window{WindowTS: ts, Side: strategy.SideShort, State: StateInPosition})

	cfg := testConfig()
	e := New(cfg, st, nil, nil, nil, nil, nil, nil, nil)
	e.skipWaitNext = true
	closes := 0
	e.pxProbe = func(context.Context) (float64, error) {
		closes++
		return 60_594, nil
	}
	e.flatProbe = func(context.Context) (bool, lighter.Position, error) {
		return true, lighter.Position{}, nil
	}

	pos := &livePos{
		WindowTS: ts, Side: strategy.SideShort, Qty: 0.001, Notional: 60,
		EntryPx: 60_515, EntryTS: float64(ts + 1), EquityBefore: 100,
		StopPx: 60_590, StopClientIdx: 42, ArmedAt: time.Now().Add(-30 * time.Second),
		SeenAcctOpen: true,
	}
	// Dry-run market close calls btcPx. Remote-flat plan must not call it for close;
	// only the px<=0 fallback would. We pass px so closes should stay 0.
	if err := e.finalizePosition(ctx, pos, "stop", 60_606.5, 0); err != nil {
		t.Fatal(err)
	}
	if closes != 0 {
		t.Fatalf("btcPx calls=%d; remote-flat settle must not market-close", closes)
	}
	w, _ := st.GetWindow(ctx, ts)
	if w.ExitReason != "sl" {
		t.Fatalf("exit_reason=%q", w.ExitReason)
	}
	if w.LighterExitPx.Float64 != 60_606.5 {
		t.Fatalf("exit_px=%v want tape/passed stop fill", w.LighterExitPx.Float64)
	}
	if strings.Contains(w.SkipReason, "exit_late") {
		t.Fatalf("unexpected exit_late in %q", w.SkipReason)
	}
}

func TestFinalizeConfirmedOpenMarksExitLate(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	ts := midWindowTS(t)
	ctx := context.Background()
	_ = st.InsertWindow(ctx, store.Window{WindowTS: ts, Side: strategy.SideLong, State: StateInPosition})

	cfg := testConfig()
	e := New(cfg, st, nil, nil, nil, nil, nil, nil, nil)
	e.skipWaitNext = true
	e.pxProbe = func(context.Context) (float64, error) { return 60_000, nil }
	e.flatProbe = func(context.Context) (bool, lighter.Position, error) {
		return false, lighter.Position{Size: 0.001, Sign: 1}, nil
	}

	pos := &livePos{
		WindowTS: ts, Side: strategy.SideLong, Qty: 0.001, Notional: 60,
		EntryPx: 60_400, EntryTS: float64(ts + 1), EquityBefore: 100,
		ArmedAt: time.Now().Add(-time.Minute), SeenAcctOpen: true,
	}
	if err := e.finalizePosition(ctx, pos, "", 0, 0); err != nil {
		t.Fatal(err)
	}
	w, _ := st.GetWindow(ctx, ts)
	if !strings.Contains(w.SkipReason, "exit_late") {
		t.Fatalf("skip_reason=%q want exit_late", w.SkipReason)
	}
	if e.global.LateStreak != 1 {
		t.Fatalf("late_streak=%d want 1", e.global.LateStreak)
	}
}

func TestResolveStopWatchdogRemoteFlat(t *testing.T) {
	cfg := testConfig()
	e := New(cfg, nil, nil, nil, nil, nil, nil, nil, nil)
	e.pxProbe = func(context.Context) (float64, error) { return 60_300, nil }
	e.flatProbe = func(context.Context) (bool, lighter.Position, error) {
		return true, lighter.Position{}, nil
	}
	pos := &livePos{
		WindowTS: time.Now().UTC().Unix() - (time.Now().UTC().Unix() % 300),
		Side:     strategy.SideLong,
		StopPx:   60_325,
		ArmedAt:  time.Now().Add(-10 * time.Second),
	}
	reason, px, _, ok := e.resolveStopWatchdog(context.Background(), pos)
	if !ok {
		t.Fatal("expected watchdog settle")
	}
	if reason != "stop" {
		t.Fatalf("reason=%q", reason)
	}
	if px != 60_300 {
		t.Fatalf("px=%v", px)
	}
}

func TestResolveStopWatchdogGrace(t *testing.T) {
	cfg := testConfig()
	e := New(cfg, nil, nil, nil, nil, nil, nil, nil, nil)
	e.pxProbe = func(context.Context) (float64, error) { return 60_300, nil }
	pos := &livePos{
		Side: strategy.SideLong, StopPx: 60_325, ArmedAt: time.Now(),
	}
	if _, _, _, ok := e.resolveStopWatchdog(context.Background(), pos); ok {
		t.Fatal("watchdog must respect post-entry grace")
	}
}
