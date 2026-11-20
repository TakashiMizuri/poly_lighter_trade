package store

import (
	"context"
	"path/filepath"
	"testing"
)

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
