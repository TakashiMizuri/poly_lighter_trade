package engine

import (
	"context"
	"database/sql"
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

func TestBoundaryCloseLeavesDecideBudget(t *testing.T) {
	open := time.Unix(1_795_273_500, 0).UTC()
	early := open.Add(-300 * time.Millisecond)
	got := boundaryCloseDeadline(early, open, 2*time.Second)
	want := open.Add(900 * time.Millisecond)
	if !got.Equal(want) {
		t.Fatalf("deadline %s want %s", got, want)
	}
	late := open.Add(1500 * time.Millisecond)
	got = boundaryCloseDeadline(late, open, 2*time.Second)
	if got.Sub(late) != 800*time.Millisecond {
		t.Fatalf("past-slot cap %s", got.Sub(late))
	}
}

func TestSkipPreCloseTapeOnBoundary(t *testing.T) {
	if !skipPreCloseTape("", 0, true) {
		t.Fatal("window close on the boundary must not read the tape first")
	}
	if skipPreCloseTape("stop", 0, true) {
		t.Fatal("stop on the boundary still resolves the tape")
	}
	if skipPreCloseTape("", 0, false) {
		t.Fatal("mid-window missing px still looks at the tape")
	}
	if !skipPreCloseTape("", 60_000, false) {
		t.Fatal("known px does not need a tape read")
	}
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

func TestEnsureExitPxSkipsWhenNotSuspicious(t *testing.T) {
	e := New(testConfig(), nil, nil, nil, nil, nil, nil, nil, nil)
	pos := &livePos{EntryPx: 60_000, ExitClientIdx: 1}
	px, fee := e.ensureExitPx(context.Background(), pos, 60_250, 0.01, false)
	if px != 60_250 || fee != 0.01 {
		t.Fatalf("got %v %v", px, fee)
	}
}

func TestEnsureExitPxNoHTTPLeavesSuspicious(t *testing.T) {
	e := New(testConfig(), nil, nil, nil, nil, nil, nil, nil, nil)
	pos := &livePos{EntryPx: 60_174.9, ExitClientIdx: 14}
	px, fee := e.ensureExitPx(context.Background(), pos, 60_174.9, 0, true)
	if px != 60_174.9 || fee != 0 {
		t.Fatalf("without HTTP must leave px alone, got %v %v", px, fee)
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

func TestFinalizeBoundaryDoesNotCopyEntry(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	now := time.Now().UTC().Unix()
	ts := now - (now % 300) - 300
	ctx := context.Background()
	_ = st.InsertWindow(ctx, store.Window{WindowTS: ts, Side: strategy.SideLong, State: StateInPosition})

	cfg := testConfig()
	e := New(cfg, st, nil, nil, nil, nil, nil, nil, nil)
	e.skipWaitNext = true
	e.readyPx = 60100
	e.readyEq = 25
	e.readyAt = time.Now().Add(-time.Minute)
	e.pxProbe = func(context.Context) (float64, error) {
		return 0, errors.New("no mark")
	}

	pos := &livePos{
		WindowTS: ts, Side: strategy.SideLong, Qty: 0.00041, Notional: 24.642845,
		EntryPx: 60104.5, EntryTS: float64(ts + 1), EquityBefore: 25.037493,
		ArmedAt: time.Now().Add(-time.Minute), SeenAcctOpen: true,
	}
	if err := e.finalizePosition(ctx, pos, "", 0, 0); err != nil {
		t.Fatal(err)
	}
	w, err := st.GetWindow(ctx, ts)
	if err != nil {
		t.Fatal(err)
	}
	if w.LighterExitPx.Valid || w.PnLUSD.Valid || w.RetBps.Valid {
		t.Fatalf("unconfirmed exit must stay empty: exit=%+v pnl=%+v ret=%+v", w.LighterExitPx, w.PnLUSD, w.RetBps)
	}
	if w.State != "exit_pending" || w.ExitReason != "window_close" {
		t.Fatalf("state=%q reason=%q", w.State, w.ExitReason)
	}
	if e.readyPx != 60100 {
		t.Fatalf("ready px %v must not become the entry", e.readyPx)
	}
	if e.readyOK {
		t.Fatal("readyOK must stay false until the close is confirmed")
	}
}

func TestEnsureFlatDefersWhenFlatnessUnknown(t *testing.T) {
	cfg := testConfig()
	cfg.DryRun = false
	e := New(cfg, nil, nil, nil, nil, nil, nil, nil, nil)
	e.pending = &pendingClose{WindowTS: 1, ClientIdx: 287000012, Side: strategy.SideShort}
	e.flatProbe = func(context.Context) (bool, lighter.Position, error) {
		return false, lighter.Position{}, errors.New("dns timeout")
	}
	called := 0
	e.marketCloseProbe = func(*livePos) (float64, float64, error) {
		called++
		return 0, 0, nil
	}
	if err := e.ensureFlat(context.Background(), "skip"); err != nil {
		t.Fatal(err)
	}
	if called != 0 {
		t.Fatalf("market closes=%d", called)
	}
	if !e.closeInFlight() {
		t.Fatal("unknown flatness must keep the in-flight close")
	}
}

func TestEnsureFlatResendsWhenInFlightStillOpen(t *testing.T) {
	cfg := testConfig()
	cfg.DryRun = false
	e := New(cfg, nil, nil, nil, nil, nil, nil, nil, nil)
	e.pending = &pendingClose{WindowTS: 1, ClientIdx: 287000012, Side: strategy.SideShort}
	called := 0
	e.flatProbe = func(context.Context) (bool, lighter.Position, error) {
		if called > 0 {
			return true, lighter.Position{}, nil
		}
		return false, lighter.Position{Size: 0.00041, Sign: -1}, nil
	}
	e.marketCloseProbe = func(p *livePos) (float64, float64, error) {
		called++
		if p.WindowTS != 1 {
			t.Fatalf("retry booked on window %d", p.WindowTS)
		}
		return 60100, 0, nil
	}
	if err := e.ensureFlat(context.Background(), "skip"); err != nil {
		t.Fatal(err)
	}
	if called != 1 {
		t.Fatalf("market closes=%d", called)
	}
}

func TestCloseBudgetLeavesTheStop(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(200*time.Millisecond))
	defer cancel()
	if closeBudgetLeft(ctx, 700*time.Millisecond) {
		t.Fatal("short deadline must not cancel the stop")
	}
	if !closeBudgetLeft(context.Background(), 700*time.Millisecond) {
		t.Fatal("no deadline has room to close")
	}
}

func TestConfirmFlatRejectsInFlightClose(t *testing.T) {
	cfg := testConfig()
	cfg.DryRun = false
	e := New(cfg, nil, nil, nil, nil, nil, nil, nil, nil)
	e.readyEq = 25
	e.readyPx = 60131.1
	e.readyAt = time.Now()
	e.pending = &pendingClose{WindowTS: 1, ClientIdx: 287000012, Side: strategy.SideShort}
	e.flatProbe = func(context.Context) (bool, lighter.Position, error) {
		return false, lighter.Position{Size: 0.00041, Sign: -1}, nil
	}
	err := e.confirmFlatForEntry(context.Background(), time.Now().Add(time.Second))
	if err == nil {
		t.Fatal("expected not flat")
	}
	e.flatProbe = func(context.Context) (bool, lighter.Position, error) {
		return true, lighter.Position{}, nil
	}
	if err := e.confirmFlatForEntry(context.Background(), time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if e.closeInFlight() {
		t.Fatal("flat account must clear the in-flight close")
	}
	if !e.readyOK {
		t.Fatal("readyOK")
	}
}

func TestBookExitFillSurvivesShadow(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	ts := int64(1795287000)
	w := store.Window{WindowTS: ts, Side: strategy.SideLong, ExitReason: "window_close", State: "closed"}
	w.LighterEntryPx.Float64, w.LighterEntryPx.Valid = 60104.5, true
	w.LighterExitPx.Float64, w.LighterExitPx.Valid = 60104.5, true
	w.PnLUSD.Float64, w.PnLUSD.Valid = 0, true
	w.Notional.Float64, w.Notional.Valid = 24.642845, true
	if err := st.InsertWindow(ctx, w); err != nil {
		t.Fatal(err)
	}
	if err := st.InsertOrder(ctx, ts, "exit", "sent", 287000009, "f40b4fed", 0.00041, 60079.2, nil); err != nil {
		t.Fatal(err)
	}
	e := New(testConfig(), st, nil, nil, nil, nil, nil, nil, nil)
	rows, err := st.ListUnconfirmedExits(ctx, ts)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	if err := e.bookExitFill(ctx, rows[0], 60078, 0); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateWindowShadow(ctx, ts,
		sql.NullFloat64{Float64: 60133.3, Valid: true},
		sql.NullFloat64{Float64: -2.82, Valid: true}); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetWindow(ctx, ts)
	if err != nil {
		t.Fatal(err)
	}
	if !got.LighterExitPx.Valid || got.LighterExitPx.Float64 != 60078 {
		t.Fatalf("exit %+v", got.LighterExitPx)
	}
	if !got.PnLUSD.Valid || got.PnLUSD.Float64 >= 0 {
		t.Fatalf("pnl %+v", got.PnLUSD)
	}
	if got.State != "closed" {
		t.Fatalf("state %q", got.State)
	}
	if !got.ShadowBinancePxT1.Valid || got.ShadowBinancePxT1.Float64 != 60133.3 {
		t.Fatalf("shadow %+v", got.ShadowBinancePxT1)
	}
	orders, err := st.ListOrders(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(orders) != 1 || orders[0]["status"] != "filled" {
		t.Fatalf("orders %+v", orders)
	}
}
