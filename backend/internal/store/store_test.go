package store

import (
	"context"
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
	stt, err := st.ComputeStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stt.NTrades != 1 || stt.NFillLag != 1 {
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
