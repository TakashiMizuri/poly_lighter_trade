package engine

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"poly-lighter.trade/bot/internal/clock"
	"poly-lighter.trade/bot/internal/exchange/lighter"
	"poly-lighter.trade/bot/internal/store"
	"poly-lighter.trade/bot/internal/strategy"
)

func (e *Engine) settleWindow(ctx context.Context, pos *livePos, reason string, px, fee float64) error {
	e.setState(StateFlattening)
	late := false
	deadline := time.Unix(pos.WindowTS, 0).UTC().Add(time.Duration(clock.WindowSec) * time.Second)
	nextSoon := !e.now().Before(deadline.Add(-2 * time.Second))
	alreadyFlat := false
	if !nextSoon {
		if flat, _, err := e.isFlat(ctx); err == nil && flat {
			alreadyFlat = true
		}
	}
	if px <= 0 && !nextSoon {
		if p, q, f, sl := e.closingTradeVWAP(ctx, pos); q > 0 {
			px, fee = p, f
			if reason == "" && sl {
				reason = "stop"
			}
		}
	}
	if reason == "stop" && px > 0 {
		_ = e.Store.InsertOrder(ctx, pos.WindowTS, "stop_loss_fill", "filled", pos.StopClientIdx, "", pos.Qty, px, map[string]any{"stop_px": pos.StopPx, "sl_slip_bps": strategy.SLSlipBps(pos.EntryPx, px, pos.Side, e.Cfg.StopPricePct)})
	}
	fillWait := 800 * time.Millisecond
	if nextSoon {
		fillWait = 0
	}
	if alreadyFlat {
		if e.Signer != nil {
			if err := e.Signer.CancelAll(ctx); err != nil {
				e.Log.Warn("cancel residual after remote close", "err", err)
			}
		}
	} else if reason == "stop" || reason == "closed_away" {
		e.Log.Warn("account still open after detected close, flattening", "reason", reason)
		cpx, cfee, err := e.sendMarketClose(ctx, pos, fillWait)
		if err != nil {
			e.alert(ctx, "error", "flatten", err.Error())
		}
		if cpx > 0 {
			px, fee = cpx, fee+cfee
		}
	} else {
		if pos.StopClientIdx > 0 && e.Signer != nil {
			if err := e.Signer.CancelAll(ctx); err != nil {
				e.Log.Warn("cancel stop before window close", "err", err)
			}
		}
		cpx, cfee, err := e.sendMarketClose(ctx, pos, fillWait)
		if err != nil {
			e.alert(ctx, "error", "flatten", err.Error())
		}
		if cpx > 0 {
			px, fee = cpx, cfee
		}
	}
	if !nextSoon {
		retryUntil := e.now().Add(500 * time.Millisecond)
		for !alreadyFlat && e.now().Before(retryUntil) {
			flat, _, err := e.isFlat(ctx)
			if err == nil && flat {
				break
			}
			late = e.now().After(deadline)
			cpx, cfee, _ := e.sendMarketClose(ctx, pos, 0)
			if cpx > 0 {
				px = cpx
				fee += cfee
			}
			time.Sleep(150 * time.Millisecond)
		}
		if flat, _, err := e.isFlat(ctx); err != nil || !flat {
			late = true
			e.alert(ctx, "error", "exit_late", "не удалось закрыть к границе")
		}
	}
	if px <= 0 {
		px, _ = e.btcPx(ctx)
	}
	eq := pos.EquityBefore
	if !nextSoon {
		if v, err := e.equity(ctx); err == nil && v > 0 {
			eq = v
		}
	}
	e.markReadyAfterClose(px, eq)
	sign := strategy.Sign(pos.Side)
	ret := 0.0
	if pos.EntryPx > 0 && px > 0 {
		ret = sign * (px/pos.EntryPx - 1)
	}
	pnl := pos.Notional * ret
	fees := pos.Fees + fee
	w, err := e.Store.GetWindow(ctx, pos.WindowTS)
	if err != nil {
		w = store.Window{WindowTS: pos.WindowTS, Side: pos.Side}
	}
	w.LighterExitTS = sql.NullFloat64{Float64: e.unix(), Valid: true}
	w.LighterExitPx = sql.NullFloat64{Float64: px, Valid: true}
	w.EquityAfter = sql.NullFloat64{Float64: eq, Valid: true}
	w.PnLUSD = sql.NullFloat64{Float64: pnl - fees, Valid: true}
	w.RetBps = sql.NullFloat64{Float64: ret * 1e4, Valid: true}
	w.FeesUSD = sql.NullFloat64{Float64: fees, Valid: true}
	w.State = "closed"
	if reason == "stop" {
		w.ExitReason = "sl"
		if pos.StopPx > 0 {
			w.StopPx = sql.NullFloat64{Float64: pos.StopPx, Valid: true}
			w.SLSlipBps = sql.NullFloat64{Float64: strategy.SLSlipBps(pos.EntryPx, px, pos.Side, e.Cfg.StopPricePct), Valid: true}
		}
	} else if reason == "closed_away" {
		w.ExitReason = "closed_away"
	} else if reason == "stop_missing" {
		w.ExitReason = "stop_missing"
	} else if strings.HasPrefix(reason, "halt") {
		w.ExitReason = "halt"
	} else {
		w.ExitReason = "window_close"
	}
	if pos.StopPx > 0 && !w.StopPx.Valid {
		w.StopPx = sql.NullFloat64{Float64: pos.StopPx, Valid: true}
	}
	e.stampClock(&w)
	if late {
		w.SkipReason = joinReason(w.SkipReason, "exit_late")
		e.bumpLateStreak(ctx, true)
		if e.global.LateStreak >= e.Cfg.HaltExitLateStreak {
			e.halt(ctx, "exit_late_streak")
		}
	} else {
		e.bumpLateStreak(ctx, false)
	}
	if reason != "" {
		w.SkipReason = joinReason(w.SkipReason, reason)
	}
	_ = e.Store.UpdateWindow(ctx, w)
	_ = e.Store.InsertEquity(ctx, time.Now().Unix(), "live", eq)
	e.applyPeak(ctx, eq)
	e.mu.Lock()
	e.pos = nil
	e.mu.Unlock()
	e.setState(StateIdle)
	e.flogWindow("exit", w)
	e.alert(ctx, "info", "exit", fmt.Sprintf("%s pnl=%.2f ret=%.2fbps eq=%.2f", pos.Side, pnl-fees, ret*1e4, eq))
	if pos.ExitClientIdx != 0 && pos.ExitHash != "" {
		e.refineExitAsync(pos, pos.ExitClientIdx, pos.ExitHash)
	}
	e.shadowAsync(pos.WindowTS)
	return e.waitNextOpen(ctx, pos.WindowTS+clock.WindowSec)
}

func (e *Engine) marketClose(ctx context.Context, pos *livePos) (px, fee float64, err error) {
	return e.sendMarketClose(ctx, pos, 800*time.Millisecond)
}

func (e *Engine) sendMarketClose(ctx context.Context, pos *livePos, fillWait time.Duration) (px, fee float64, err error) {
	if e.Cfg.DryRun || e.Signer == nil {
		px, err = e.btcPx(ctx)
		return px, 0, err
	}
	var ref float64
	if fillWait <= 0 {
		pctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		ref, err = e.btcPx(pctx)
		cancel()
		if err != nil || ref <= 0 {
			ref = pos.EntryPx
			err = nil
		}
		if ref <= 0 {
			return 0, 0, fmt.Errorf("no ref price for close")
		}
	} else {
		ref, err = e.btcPx(ctx)
		if err != nil {
			return 0, 0, err
		}
	}
	buy := pos.Side == strategy.SideShort // closing short = buy
	idx := e.nextClientIdx(ctx)
	res, err := e.Signer.MarketIOC(ctx, e.Cfg.Symbol, buy, pos.Qty, ref, e.Cfg.MarketSlippage, idx, true)
	txHash := hashOf(res)
	_ = e.Store.InsertOrder(ctx, pos.WindowTS, "exit", errString(err, "sent"), idx, txHash, pos.Qty, ref, nil)
	if err != nil {
		if !lighter.IsAmbiguousSubmit(err) {
			return 0, 0, err
		}
		e.Log.Warn("exit send ambiguous; reconciling", "err", err, "client_idx", idx)
		e.alert(ctx, "warn", "exit_send_ambiguous", fmt.Sprintf("ack lost (%v); reconciling by client_idx=%d", err, idx))
	}
	pos.ExitClientIdx = idx
	pos.ExitHash = txHash
	meta := e.Markets[e.Cfg.Symbol]
	confirmBudget := fillWait
	if confirmBudget <= 0 {
		confirmBudget = 800 * time.Millisecond
	}
	wctx, cancel := context.WithTimeout(context.Background(), confirmBudget)
	fill := e.HTTP.ConfirmFill(wctx, lighter.ConfirmFillOpts{
		AccountIndex: e.Cfg.AccountIndex,
		MarketID:     meta.MarketID,
		Symbol:       e.Cfg.Symbol,
		ClientIdx:    idx,
		TxHash:       txHash,
		FallbackPx:   ref,
		SizeDecimals: meta.SizeDecimals,
		Budget:       confirmBudget,
		UsePosition:  false,
	})
	cancel()
	if fill.Qty > 0 && fill.Px > 0 {
		pos.ExitHash = ""
		return fill.Px, fill.Fee, nil
	}
	// Ambiguous or slow fill: if account is already flat, treat close as done.
	if flat, _, ferr := e.isFlat(ctx); ferr == nil && flat {
		return ref, 0, nil
	}
	if err != nil {
		return 0, 0, fmt.Errorf("exit unconfirmed after ambiguous/failed send: %w", err)
	}
	if fillWait <= 0 {
		return ref, 0, nil
	}
	return ref, 0, nil
}

func (e *Engine) refineExitAsync(pos *livePos, clientIdx int64, txHash string) {
	if e.HTTP == nil || pos == nil || e.Store == nil {
		return
	}
	windowTS := pos.WindowTS
	side := pos.Side
	entry := pos.EntryPx
	notional := pos.Notional
	entryFees := pos.Fees
	meta := e.Markets[e.Cfg.Symbol]
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		px, qty, fee, _ := e.HTTP.WaitFillFor(ctx, e.Cfg.AccountIndex, meta.MarketID, clientIdx, txHash, 0, 3500*time.Millisecond)
		if qty <= 0 || px <= 0 {
			return
		}
		w, err := e.Store.GetWindow(ctx, windowTS)
		if err != nil {
			return
		}
		w.LighterExitPx = sql.NullFloat64{Float64: px, Valid: true}
		fees := entryFees + fee
		if w.FeesUSD.Valid && w.FeesUSD.Float64 > fees {
			fees = w.FeesUSD.Float64
		}
		w.FeesUSD = sql.NullFloat64{Float64: fees, Valid: true}
		if entry > 0 {
			ret := strategy.Sign(side) * (px/entry - 1)
			w.RetBps = sql.NullFloat64{Float64: ret * 1e4, Valid: true}
			w.PnLUSD = sql.NullFloat64{Float64: notional*ret - fees, Valid: true}
		}
		_ = e.Store.UpdateWindow(ctx, w)
	}()
}

func (e *Engine) ensureFlat(ctx context.Context, why string) error {
	if why == "prewarm" || why == "skip" {
		e.mu.Lock()
		live := e.pos
		e.mu.Unlock()
		if live != nil {
			return nil
		}
	}
	if e.Cfg.DryRun || e.Signer == nil {
		e.mu.Lock()
		e.pos = nil
		e.mu.Unlock()
		return nil
	}
	pos, err := e.waitOpenPosition(ctx)
	if err != nil {
		return err
	}
	if pos.Size <= 0 {
		if why == "boot" || why == "kill" {
			if err := e.Signer.CancelAll(ctx); err != nil {
				e.Log.Warn("cancel stale orders on boot", "err", err)
			}
		}
		return nil
	}
	e.Log.Warn("flatten", "why", why, "qty", pos.Size, "sign", pos.Sign)
	live := &livePos{WindowTS: clock.WindowOf(e.now()), Qty: pos.Size, Side: strategy.SideLong}
	if pos.Sign < 0 {
		live.Side = strategy.SideShort
	}
	for i := 0; i < 15; i++ {
		if live.Qty <= 0 {
			break
		}
		_, _, err := e.sendMarketClose(ctx, live, 0)
		if err != nil {
			e.Log.Warn("flatten retry", "err", err)
		}
		time.Sleep(200 * time.Millisecond)
		flat, cur, ferr := e.isFlat(ctx)
		if ferr != nil {
			continue
		}
		if flat {
			if err := e.Signer.CancelAll(ctx); err != nil {
				e.Log.Warn("cancel residual orders after flatten", "why", why, "err", err)
			}
			return nil
		}
		if cur.Size > 0 {
			live.Qty = cur.Size
			if cur.Sign < 0 {
				live.Side = strategy.SideShort
			} else {
				live.Side = strategy.SideLong
			}
		}
	}
	return fmt.Errorf("still not flat after flatten (%s)", why)
}

func (e *Engine) waitOpenPosition(ctx context.Context) (lighter.Position, error) {
	deadline := time.Now().Add(800 * time.Millisecond)
	var lastErr error
	for {
		flat, pos, err := e.isFlat(ctx)
		if err != nil {
			lastErr = err
		} else if !flat && pos.Size > 0 {
			return pos, nil
		} else {
			return lighter.Position{}, nil
		}
		if !time.Now().Before(deadline) {
			return lighter.Position{}, lastErr
		}
		select {
		case <-ctx.Done():
			if lastErr != nil {
				return lighter.Position{}, lastErr
			}
			return lighter.Position{}, ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
}

func (e *Engine) isFlat(ctx context.Context) (bool, lighter.Position, error) {
	if e.HTTP == nil {
		return true, lighter.Position{}, nil
	}
	acc, err := e.HTTP.AccountUrgent(ctx, e.Cfg.AccountIndex)
	if err != nil {
		return false, lighter.Position{}, err
	}
	meta := e.Markets[e.Cfg.Symbol]
	p, ok := acc.Position(e.Cfg.Symbol, meta.MarketID)
	if !ok || p.Size == 0 {
		return true, lighter.Position{}, nil
	}
	return false, p, nil
}

func (e *Engine) detectExit(ctx context.Context, pos *livePos) (reason string, px, fee float64, ok bool) {
	if pos == nil {
		return "", 0, 0, false
	}
	if !pos.ArmedAt.IsZero() && time.Since(pos.ArmedAt) < 5*time.Second {
		return "", 0, 0, false
	}
	flat, _, err := e.isFlat(ctx)
	if err != nil {
		return "", 0, 0, false
	}
	if !flat {
		e.mu.Lock()
		if e.pos != nil {
			e.pos.SeenAcctOpen = true
		}
		e.mu.Unlock()
		return "", 0, 0, false
	}
	if !pos.SeenAcctOpen {
		return "", 0, 0, false
	}
	// Trades REST often lags DNS/account. Do not book last/mark as the exit.
	wait := 2500 * time.Millisecond
	exitAt := time.Unix(pos.WindowTS, 0).UTC().Add(time.Duration(clock.WindowSec)*time.Second - e.Cfg.ExitEarly)
	if remain := exitAt.Sub(e.now()); remain > 0 && remain < wait {
		wait = remain
	}
	wctx, cancel := context.WithTimeout(context.Background(), wait)
	p, q, f, sl := e.waitClosingVWAP(wctx, pos)
	cancel()
	if q > 0 {
		px, fee = p, f
	} else {
		px, _ = e.btcPx(ctx)
	}
	if sl || (pos.StopPx > 0 && strategy.StopLikeFill(px, pos.Side, pos.StopPx)) {
		return "stop", px, fee, true
	}
	return "closed_away", px, fee, true
}

func (e *Engine) waitClosingVWAP(ctx context.Context, pos *livePos) (px, qty, fee float64, sl bool) {
	for {
		px, qty, fee, sl = e.closingTradeVWAP(ctx, pos)
		if qty > 0 && px > 0 {
			return px, qty, fee, sl
		}
		select {
		case <-ctx.Done():
			return 0, 0, 0, false
		case <-time.After(120 * time.Millisecond):
		}
	}
}

func (e *Engine) closingTradeVWAP(ctx context.Context, pos *livePos) (px, qty, fee float64, sl bool) {
	if e.HTTP == nil || pos == nil {
		return 0, 0, 0, false
	}
	meta := e.Markets[e.Cfg.Symbol]
	fills, err := e.HTTP.AccountTradesUrgent(ctx, e.Cfg.AccountIndex, meta.MarketID, 100)
	if err != nil {
		return 0, 0, 0, false
	}
	if pos.StopClientIdx != 0 || pos.StopHash != "" {
		if p, q, f, _ := lighter.VWAP(lighter.FillsMatching(fills, pos.StopClientIdx, pos.StopHash)); q > 0 {
			return p, q, f, true
		}
	}
	after := pos.EntryTS
	if after <= 0 && !pos.ArmedAt.IsZero() {
		after = float64(pos.ArmedAt.Unix())
	}
	p, q, f, _ := lighter.VWAP(lighter.FillsClosingPosition(fills, pos.ClientIdx, pos.EntryHash, pos.Qty, after))
	if q > 0 && pos.StopPx > 0 && strategy.StopLikeFill(p, pos.Side, pos.StopPx) {
		return p, q, f, true
	}
	return p, q, f, false
}
