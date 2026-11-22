package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestSaveGlobalCAS(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()

	st0 := GlobalState{ClientOrderSeq: 10, Version: 1}
	ok, err := st.SaveGlobalCAS(ctx, st0, 0)
	if err != nil || !ok {
		t.Fatalf("initial cas ok=%v err=%v", ok, err)
	}
	st1 := GlobalState{ClientOrderSeq: 11, Version: 2}
	ok, err = st.SaveGlobalCAS(ctx, st1, 1)
	if err != nil || !ok {
		t.Fatalf("cas v1 ok=%v err=%v", ok, err)
	}
	stStale := GlobalState{ClientOrderSeq: 99, Version: 2}
	ok, err = st.SaveGlobalCAS(ctx, stStale, 1)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("stale cas should fail")
	}
	loaded, err := st.LoadGlobal(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ClientOrderSeq != 11 || loaded.Version != 2 {
		t.Fatalf("loaded %+v", loaded)
	}
}

func TestUniqueWindowTS(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	w := Window{WindowTS: 1000, Side: "skip", SkipReason: "below_th"}
	if err := st.InsertWindow(ctx, w); err != nil {
		t.Fatal(err)
	}
	if err := st.InsertWindow(ctx, w); err == nil {
		t.Fatal("expected unique violation")
	}
	ok, err := st.HasWindow(ctx, 1000)
	if err != nil || !ok {
		t.Fatalf("has %v %v", ok, err)
	}
}

func TestFillLagStatsOnlyTrades(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	skip := Window{WindowTS: 1000, Side: "skip", SkipReason: "below_th"}
	if err := st.InsertWindow(ctx, skip); err != nil {
		t.Fatal(err)
	}
	trade := Window{WindowTS: 1300, Side: "long"}
	trade.LighterEntryPx.Float64, trade.LighterEntryPx.Valid = 60000, true
	trade.FillLagMS.Float64, trade.FillLagMS.Valid = 350, true
	if err := st.InsertWindow(ctx, trade); err != nil {
		t.Fatal(err)
	}
	zero := Window{WindowTS: 1600, Side: "long"}
	zero.LighterEntryPx.Float64, zero.LighterEntryPx.Valid = 60000, true
	zero.FillLagMS.Float64, zero.FillLagMS.Valid = 0, true
	if err := st.InsertWindow(ctx, zero); err != nil {
		t.Fatal(err)
	}
	stt, err := st.ComputeStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stt.NTrades != 2 || stt.NFillLag != 1 {
		t.Fatalf("trades=%d nlag=%d", stt.NTrades, stt.NFillLag)
	}
	if stt.MeanFillLagMS < 349 || stt.MeanFillLagMS > 351 {
		t.Fatalf("mean %v", stt.MeanFillLagMS)
	}
	if stt.P50FillLagMS != 350 {
		t.Fatalf("p50 %v", stt.P50FillLagMS)
	}
	skip.EntryLagMS.Float64, skip.EntryLagMS.Valid = 7, true
	if err := st.UpdateWindow(ctx, skip); err != nil {
		t.Fatal(err)
	}
	listed, err := st.ListWindows(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range listed {
		if w.Side == "skip" && (w.FillLagMS.Valid || w.EntryLagMS.Valid) {
			t.Fatalf("skip must not expose lag: %+v", w)
		}
	}
}

func TestWindowTimingsRoundtrip(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	w := Window{WindowTS: 2000, Side: "skip", SkipReason: "below_th", Timings: &Timings{MsLateStart: 1812, MsQuote: 3, MsOpenToDecide: 1815}}
	if err := st.InsertWindow(ctx, w); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetWindow(ctx, 2000)
	if err != nil {
		t.Fatal(err)
	}
	if got.Timings == nil || got.Timings.MsLateStart != 1812 || got.Timings.MsQuote != 3 {
		t.Fatalf("timings %+v", got.Timings)
	}
	list, err := st.ListWindows(ctx, 5)
	if err != nil || len(list) != 1 || list[0].Timings == nil || list[0].Timings.MsOpenToDecide != 1815 {
		t.Fatalf("list timings")
	}
	if list[0].FillLagMS.Valid || list[0].EntryLagMS.Valid {
		t.Fatal("skip must hide fill/entry lag")
	}
}

func TestLiveShadowGap(t *testing.T) {
	gap, coef := LiveShadowGap(1.61, 1.72)
	if gap > -0.10 || gap < -0.12 {
		t.Fatalf("gap %v", gap)
	}
	if coef == nil || *coef < 0.93 || *coef > 0.94 {
		t.Fatalf("coef %v", coef)
	}
	_, coef = LiveShadowGap(1.0, 0)
	if coef != nil {
		t.Fatal("expected nil coef when shadow ~0")
	}
}

func TestShadowUpdateLeavesExit(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	w := Window{WindowTS: 3000, Side: "long", ExitReason: "window_close", State: "closed"}
	w.LighterEntryPx.Float64, w.LighterEntryPx.Valid = 60104.5, true
	w.LighterExitPx.Float64, w.LighterExitPx.Valid = 60078, true
	w.PnLUSD.Float64, w.PnLUSD.Valid = -0.011, true
	if err := st.InsertWindow(ctx, w); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateWindowShadow(ctx, 3000,
		sql.NullFloat64{Float64: 60133.3, Valid: true},
		sql.NullFloat64{Float64: -2.8, Valid: true}); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetWindow(ctx, 3000)
	if err != nil {
		t.Fatal(err)
	}
	if !got.LighterExitPx.Valid || got.LighterExitPx.Float64 != 60078 {
		t.Fatalf("exit %+v", got.LighterExitPx)
	}
	if !got.PnLUSD.Valid || got.PnLUSD.Float64 != -0.011 {
		t.Fatalf("pnl %+v", got.PnLUSD)
	}
	if !got.ShadowBinancePxT1.Valid || got.ShadowBinancePxT1.Float64 != 60133.3 {
		t.Fatalf("shadow %+v", got.ShadowBinancePxT1)
	}
	if !got.ShadowRetBps.Valid || got.ShadowRetBps.Float64 != -2.8 {
		t.Fatalf("shadow ret %+v", got.ShadowRetBps)
	}
}

func TestListUnconfirmedCopiedExit(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	w := Window{WindowTS: 4000, Side: "short", ExitReason: "window_close", State: "closed"}
	w.LighterEntryPx.Float64, w.LighterEntryPx.Valid = 60077.4, true
	w.LighterExitPx.Float64, w.LighterExitPx.Valid = 60077.4, true
	w.PnLUSD.Float64, w.PnLUSD.Valid = 0, true
	w.Notional.Float64, w.Notional.Valid = 24.63, true
	if err := st.InsertWindow(ctx, w); err != nil {
		t.Fatal(err)
	}
	if err := st.InsertOrder(ctx, 4000, "exit", "sent", 287000012, "e2b02e75", 0.00041, 60131.1, nil); err != nil {
		t.Fatal(err)
	}
	rows, err := st.ListUnconfirmedExits(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ClientIdx != 287000012 || rows[0].EntryPx != 60077.4 {
		t.Fatalf("%+v", rows)
	}
	if err := st.UpdateWindowExit(ctx, 4000, 1, 60141.4, -0.026, -10.6, 0, "closed", "window_close"); err != nil {
		t.Fatal(err)
	}
	rows, err = st.ListUnconfirmedExits(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("still unconfirmed %+v", rows)
	}
	got, err := st.GetWindow(ctx, 4000)
	if err != nil {
		t.Fatal(err)
	}
	if got.LighterExitPx.Float64 != 60141.4 || got.State != "closed" {
		t.Fatalf("%+v", got)
	}
}
