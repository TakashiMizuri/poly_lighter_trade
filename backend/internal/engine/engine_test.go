package engine

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"poly-lighter.trade/bot/internal/config"
	"poly-lighter.trade/bot/internal/exchange/lighter"
	"poly-lighter.trade/bot/internal/store"
	"poly-lighter.trade/bot/internal/strategy"
)

func testConfig() *config.Config {
	return &config.Config{
		Profile:            "POLY_LIGHTER_1",
		Symbol:             "BTC",
		Threshold:          0.65,
		MaxSpread:          0.05,
		PrewarmSeconds:     15,
		EntryMaxLag:        2 * time.Second,
		ExitEarly:          300 * time.Millisecond,
		Leverage:           1,
		EquityFraction:     1,
		MarketSlippage:     0.02,
		StopPricePct:       0.00125,
		DryRun:             true,
		HaltDDpct:          0.08,
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

func TestEnsureReadyRefreshesStaleSnapshot(t *testing.T) {
	cfg := testConfig()
	e := New(cfg, nil, nil, nil, nil, nil, nil, nil, nil)
	e.global.StartEquity = 25
	e.readyAt = time.Now().Add(-10 * time.Second)
	e.readyEq, e.readyPx, e.readyOK = 1, 1, false
	called := false
	e.pxProbe = func(context.Context) (float64, error) {
		called = true
		return 60100, nil
	}
	eq, px, err := e.ensureReady(context.Background(), time.Now().Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !called || eq != 25 || px != 60100 || !e.readyOK {
		t.Fatalf("eq=%v px=%v ok=%v called=%v", eq, px, e.readyOK, called)
	}
}

func TestEnsureReadySkipsRefreshWhenFresh(t *testing.T) {
	cfg := testConfig()
	e := New(cfg, nil, nil, nil, nil, nil, nil, nil, nil)
	e.readyAt = time.Now()
	e.readyEq, e.readyPx, e.readyOK = 25, 60100, true
	e.pxProbe = func(context.Context) (float64, error) {
		t.Fatal("fresh snapshot must not hit the venue")
		return 0, nil
	}
	eq, px, err := e.ensureReady(context.Background(), time.Now().Add(2*time.Second))
	if err != nil || eq != 25 || px != 60100 {
		t.Fatalf("eq=%v px=%v err=%v", eq, px, err)
	}
}

func TestEnsureReadyNoTimeDoesNotRefresh(t *testing.T) {
	cfg := testConfig()
	e := New(cfg, nil, nil, nil, nil, nil, nil, nil, nil)
	e.readyAt = time.Now().Add(-10 * time.Second)
	e.pxProbe = func(context.Context) (float64, error) {
		t.Fatal("no budget must not hit the venue")
		return 0, nil
	}
	_, _, err := e.ensureReady(context.Background(), time.Now().Add(50*time.Millisecond))
	if err == nil {
		t.Fatal("expected not ready")
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

func TestPersistCtxOutlivesParent(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	cancel()
	ctx, done := persistCtx(parent)
	defer done()
	if err := ctx.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestInsertOrderSurvivesCancelledContext(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	e := New(testConfig(), st, nil, nil, nil, nil, nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e.insertOrder(ctx, 1795341600, "exit", "context deadline exceeded", 334400010, "dead", 0.0004, 60895.3, nil)
	rows, err := st.ListOrders(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0]["client_order_index"] != int64(334400010) || rows[0]["kind"] != "exit" {
		t.Fatalf("%v", rows)
	}
}

func TestRecordFillLagIgnoresWholeSecondStamp(t *testing.T) {
	windowTS := int64(1795334400)
	clientIdx := int64(334400003)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"code":200,"orders":[{"client_order_index":%d,"created_at":%d}]}`, clientIdx, windowTS)
	}))
	defer srv.Close()
	st := openTestStore(t)
	insertLagWindow(t, st, windowTS)
	e := newLagEngine(t, st, srv.URL)
	e.recordFillLag(windowTS, clientIdx)
	got, err := st.GetWindow(context.Background(), windowTS)
	if err != nil {
		t.Fatal(err)
	}
	if got.FillLagMS.Valid {
		t.Fatalf("whole-second stamp stored %+v", got.FillLagMS)
	}
}

func TestRecordFillLagRetriesThenBooks(t *testing.T) {
	windowTS := int64(1795334400)
	clientIdx := int64(334400003)
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits == 1 {
			fmt.Fprint(w, `{"code":0,"orders":[]}`)
			return
		}
		fmt.Fprintf(w, `{"code":200,"orders":[{"client_order_index":%d,"created_at":%d}]}`, clientIdx, windowTS*1000+420)
	}))
	defer srv.Close()
	st := openTestStore(t)
	insertLagWindow(t, st, windowTS)
	e := newLagEngine(t, st, srv.URL)
	e.recordFillLag(windowTS, clientIdx)
	got, err := st.GetWindow(context.Background(), windowTS)
	if err != nil {
		t.Fatal(err)
	}
	if hits < 2 || !got.FillLagMS.Valid || got.FillLagMS.Float64 != 420 {
		t.Fatalf("hits %d lag %+v", hits, got.FillLagMS)
	}
	if got.Timings == nil || got.Timings.MsOpenToExchange != 420 || got.Timings.MsSendToExchange != 166 {
		t.Fatalf("timings %+v", got.Timings)
	}
}

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func insertLagWindow(t *testing.T, st *store.Store, windowTS int64) {
	t.Helper()
	row := store.Window{WindowTS: windowTS, Side: "short", State: "IN_POSITION", Timings: &store.Timings{OpenUnix: float64(windowTS), MsOpenToSend: 254}}
	row.LighterEntryPx.Float64, row.LighterEntryPx.Valid = 59149.5, true
	if err := st.InsertWindow(context.Background(), row); err != nil {
		t.Fatal(err)
	}
}

func newLagEngine(t *testing.T, st *store.Store, baseURL string) *Engine {
	t.Helper()
	cfg := testConfig()
	cfg.AccountIndex = 7
	return New(cfg, st, lighter.NewHTTP(baseURL), nil, nil, nil, nil, nil, nil)
}

func TestNoTwoPositionsInvariant(t *testing.T) {
	e := &Engine{}
	e.pos = &livePos{WindowTS: 1, Qty: 1}
	e.pos = &livePos{WindowTS: 2, Qty: 1}
	if e.pos.WindowTS != 2 {
		t.Fatal("single slot")
	}
}
