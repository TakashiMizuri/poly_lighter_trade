package engine

import (
	"context"
	"database/sql"
	"time"

	"poly-lighter.trade/bot/internal/clock"
	"poly-lighter.trade/bot/internal/exchange/lighter"
	"poly-lighter.trade/bot/internal/store"
)

func (e *Engine) syncClock(ctx context.Context, n int) error {
	e.clockSyncMu.Lock()
	defer e.clockSyncMu.Unlock()
	s, err := clock.MeasureBinanceOffsetStable(ctx, nil, e.Cfg.BinanceURL, n)
	if err != nil {
		e.mu.Lock()
		e.offsetOK = false
		e.offsetAt = time.Time{}
		e.mu.Unlock()
		return err
	}
	e.mu.Lock()
	e.offset = s.Offset
	e.clockRTT = s.RTT
	e.offsetOK = true
	e.offsetAt = time.Now()
	e.mu.Unlock()
	e.Log.Info("clock", "offset", s.Offset, "rtt", s.RTT, "n", n)
	return nil
}

func (e *Engine) stampClock(w *store.Window) {
	e.mu.Lock()
	ok := e.offsetOK && !e.offsetAt.IsZero() && time.Since(e.offsetAt) <= 2*time.Minute
	off := e.offset
	rtt := e.clockRTT
	e.mu.Unlock()
	if !ok {
		return
	}
	w.ClockOffsetMS = sql.NullFloat64{Float64: off.Seconds() * 1000, Valid: true}
	w.ClockRTTMS = sql.NullFloat64{Float64: rtt.Seconds() * 1000, Valid: true}
}

// persistCtx keeps a database write alive after the trading context has hit its deadline.
func persistCtx(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(parent), time.Second)
}

func (e *Engine) insertOrder(ctx context.Context, windowTS int64, kind, status string, clientIdx int64, txHash string, qty, px float64, payload any) {
	if e == nil || e.Store == nil {
		return
	}
	wctx, cancel := persistCtx(ctx)
	defer cancel()
	if err := e.Store.InsertOrder(wctx, windowTS, kind, status, clientIdx, txHash, qty, px, payload); err != nil {
		e.Log.Warn("order persist", "kind", kind, "client_idx", clientIdx, "window_ts", windowTS, "err", err)
	}
}

func (e *Engine) saveWindow(ctx context.Context, w store.Window) {
	e.applyTrace(&w)
	tSQL := time.Now()
	if err := e.Store.InsertWindow(ctx, w); err != nil {
		_ = e.Store.UpdateWindow(ctx, w)
	}
	e.traceDur("sqlite", tSQL)
}

func (e *Engine) alert(ctx context.Context, level, kind, msg string) {
	e.Log.Info(msg, "level", level, "kind", kind)
	_ = e.Store.InsertEvent(ctx, level, kind, msg, nil)
	if e.Notify != nil {
		e.Notify.Alert(ctx, level, kind, msg)
	}
	if e.Flog != nil && (level == "error" || level == "warn") {
		e.Flog.Event(level, map[string]any{"kind": kind, "message": msg})
	}
}

func (e *Engine) flogWindow(kind string, w store.Window) {
	if e.Flog == nil {
		return
	}
	e.Flog.Event(kind, map[string]any{
		"window_ts": w.WindowTS, "decision_ts": w.DecisionTS, "quote_ts": w.QuoteTS,
		"p_up": w.PUp, "p_down": w.PDown, "spread": w.Spread, "quote_source": w.QuoteSource,
		"side": w.Side, "skip_reason": w.SkipReason, "exit_reason": w.ExitReason, "state": w.State,
		"entry_px": nullF(w.LighterEntryPx), "exit_px": nullF(w.LighterExitPx),
		"qty": nullF(w.Qty), "stop_px": nullF(w.StopPx), "fill_lag_ms": nullF(w.FillLagMS),
		"timings": w.Timings,
	})
}

func nullF(v sql.NullFloat64) any {
	if !v.Valid {
		return nil
	}
	return v.Float64
}

func errString(err error, ok string) string {
	if err != nil {
		return err.Error()
	}
	return ok
}

func hashOf(r *lighter.OrderResult) string {
	if r == nil {
		return ""
	}
	return r.TxHash
}

func joinReason(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	return a + "," + b
}

func SuppressRepeat(at map[string]time.Time, now time.Time, window time.Duration, level, kind, msg string) bool {
	if (level != "warn" && level != "error") || at == nil {
		return false
	}
	key := kind + "\n" + msg
	if t, ok := at[key]; ok && now.Sub(t) < window {
		return true
	}
	at[key] = now
	return false
}
