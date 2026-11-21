package engine

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strings"
	"time"

	"poly-lighter.trade/bot/internal/clock"
	"poly-lighter.trade/bot/internal/exchange/lighter"
	"poly-lighter.trade/bot/internal/store"
	"poly-lighter.trade/bot/internal/strategy"
)

// accountFlat is three-valued: network errors are Unknown, never "still open".
type accountFlat struct {
	Known bool
	Flat  bool
	Pos   lighter.Position
}

func (e *Engine) settleWindow(ctx context.Context, pos *livePos, reason string, px, fee float64) error {
	if err := e.finalizePosition(ctx, pos, reason, px, fee); err != nil {
		return err
	}
	if e.skipWaitNext {
		return nil
	}
	return e.waitNextOpen(ctx, pos.WindowTS+clock.WindowSec)
}

// finalizePosition closes/reconciles the live position and books the window.
// Invariant: exit_late / late_streak only when account is *confirmed* still open.
func (e *Engine) finalizePosition(ctx context.Context, pos *livePos, reason string, px, fee float64) error {
	e.setState(StateFlattening)
	late := false
	deadline := time.Unix(pos.WindowTS, 0).UTC().Add(time.Duration(clock.WindowSec) * time.Second)
	// Within 2s of (or past) the window boundary: prefer speed over extra account polls.
	nextSoon := !e.now().Before(deadline.Add(-2 * time.Second))

	// Prefer exchange tape over last/mark for stop / remote-close paths.
	var fromTape bool
	px, fee, reason, fromTape = e.resolveExitTape(ctx, pos, reason, px, fee, nextSoon)

	flat := accountFlat{}
	if !nextSoon {
		flat = e.pollAccountFlat(ctx, 400*time.Millisecond)
	}

	plan := planSettleClose(reason, flat, fromTape)
	fillWait := 800 * time.Millisecond
	if nextSoon {
		fillWait = 0
	}

	if plan.CancelAll && e.Signer != nil {
		if err := e.Signer.CancelAll(ctx); err != nil {
			e.Log.Warn("cancel orders during settle", "err", err, "reason", reason, "mode", plan.Mode)
		}
	}
	if plan.MarketClose {
		if plan.Mode == settleCloseResidual {
			e.Log.Warn("account still open after detected close, flattening", "reason", reason)
		} else if plan.Mode == settleCloseKeepStop {
			e.Log.Warn("flatten without cancel: account flatness unknown after stop", "reason", reason)
		}
		cpx, cfee, err := e.sendMarketClose(ctx, pos, fillWait)
		if err != nil {
			e.alert(ctx, "error", "flatten", err.Error())
		}
		if cpx > 0 {
			if plan.Mode == settleCloseResidual || plan.Mode == settleCloseKeepStop {
				px, fee = cpx, fee+cfee
			} else {
				px, fee = cpx, cfee
			}
		}
	}

	if !nextSoon {
		if plan.Mode != settleCloseRemoteFlat {
			retryUntil := e.now().Add(500 * time.Millisecond)
			for e.now().Before(retryUntil) {
				cur := e.pollAccountFlat(ctx, 0)
				if cur.Known && cur.Flat {
					flat = cur
					break
				}
				// Only hammer market closes when still open or flatness unknown.
				if !cur.Known || !cur.Flat {
					cpx, cfee, _ := e.sendMarketClose(ctx, pos, 0)
					if cpx > 0 {
						px = cpx
						fee += cfee
					}
				}
				time.Sleep(150 * time.Millisecond)
			}
		}
		final := e.pollAccountFlat(ctx, 250*time.Millisecond)
		switch {
		case final.Known && !final.Flat:
			late = true
			e.alert(ctx, "error", "exit_late", "не удалось закрыть к границе")
		case !final.Known:
			// DNS/timeout after a close attempt is not proof we missed the deadline.
			e.Log.Warn("exit flatness unconfirmed after settle")
			e.alert(ctx, "warn", "exit_flat_unknown", "позиция после выхода не подтверждена (сеть); late_streak не трогаем")
		}
	}

	if reason == "stop" && px > 0 {
		_ = e.Store.InsertOrder(ctx, pos.WindowTS, "stop_loss_fill", "filled", pos.StopClientIdx, "", pos.Qty, px, map[string]any{"stop_px": pos.StopPx, "sl_slip_bps": strategy.SLSlipBps(pos.EntryPx, px, pos.Side, e.Cfg.StopPricePct)})
	}
	// Never book the IOC ref / entry as a "fill" — resolve by exit client_idx first.
	// On the window boundary this must not block: next decide has only T+2s.
	px, fee = e.ensureExitPx(ctx, pos, px, fee, nextSoon)
	if px <= 0 && !nextSoon {
		if mark, err := e.btcPx(ctx); err == nil && mark > 0 {
			e.Log.Warn("exit px unconfirmed; booking mark", "mark", mark, "entry", pos.EntryPx)
			px = mark
		}
	}
	if px <= 0 {
		// Provisional: entry is better than 0 for ready-state; refineExitAsync fixes PnL.
		px = pos.EntryPx
	}
	eq := pos.EquityBefore
	if !nextSoon {
		if v, err := e.equity(ctx); err == nil && v > 0 {
			eq = v
		}
	} else {
		// Tiny budget only — a hung /api/v1/account must not skip the next window.
		ectx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		v, err := e.equity(ectx)
		cancel()
		if err == nil && v > 0 {
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
	if pos.ExitClientIdx != 0 {
		e.refineExitAsync(pos, pos.ExitClientIdx, pos.ExitHash)
	}
	e.shadowAsync(pos.WindowTS)
	return nil
}

// ensureExitPx replaces missing / suspicious exit prices (IOC ref often equals entry
// when btcPx timed out) with the trades-tape fill for ExitClientIdx.
// nextSoon: never wait — window_close must release the loop for decide(next) inside T+2.
func (e *Engine) ensureExitPx(ctx context.Context, pos *livePos, px, fee float64, nextSoon bool) (float64, float64) {
	if pos == nil || e.HTTP == nil || pos.ExitClientIdx == 0 {
		return px, fee
	}
	if nextSoon {
		return px, fee
	}
	suspicious := px <= 0 || (pos.EntryPx > 0 && math.Abs(px-pos.EntryPx) < 1e-6)
	if !suspicious {
		return px, fee
	}
	budget := 2 * time.Second
	meta := e.Markets[e.Cfg.Symbol]
	wctx, cancel := context.WithTimeout(context.Background(), budget)
	p, q, f, _ := e.HTTP.WaitFillFor(wctx, e.Cfg.AccountIndex, meta.MarketID, pos.ExitClientIdx, pos.ExitHash, 0, budget)
	cancel()
	if q > 0 && p > 0 {
		e.Log.Info("exit px resolved from tape", "px", p, "was", px, "client_idx", pos.ExitClientIdx)
		return p, fee + f
	}
	return px, fee
}

type settleCloseMode int

const (
	settleCloseRemoteFlat settleCloseMode = iota // confirmed flat: cancel residual only
	settleCloseResidual                          // stop/remote detected but still open: cancel + market
	settleCloseKeepStop                          // stop path, flatness unknown: market, leave SL live
	settleCloseWindow                            // normal window/halt exit: cancel stop + market
)

type settleClosePlan struct {
	Mode        settleCloseMode
	CancelAll   bool
	MarketClose bool
}

// planSettleClose decides cancel/close actions from reason + three-valued flatness.
// stopTape means we already matched a closing/stop fill on the trades tape — that
// is stronger evidence than a failed account read, so we must not market-close again.
func planSettleClose(reason string, flat accountFlat, stopTape bool) settleClosePlan {
	remote := reason == "stop" || reason == "closed_away"
	if flat.Known && flat.Flat {
		return settleClosePlan{Mode: settleCloseRemoteFlat, CancelAll: true, MarketClose: false}
	}
	if remote {
		if flat.Known && !flat.Flat {
			return settleClosePlan{Mode: settleCloseResidual, CancelAll: true, MarketClose: true}
		}
		if stopTape {
			return settleClosePlan{Mode: settleCloseRemoteFlat, CancelAll: true, MarketClose: false}
		}
		// Unknown and no tape yet: do NOT CancelAll — that races a live exchange SL.
		return settleClosePlan{Mode: settleCloseKeepStop, CancelAll: false, MarketClose: true}
	}
	return settleClosePlan{Mode: settleCloseWindow, CancelAll: true, MarketClose: true}
}

func (e *Engine) resolveExitTape(ctx context.Context, pos *livePos, reason string, px, fee float64, nextSoon bool) (float64, float64, string, bool) {
	wantTape := reason == "stop" || reason == "closed_away" || px <= 0
	if !wantTape {
		return px, fee, reason, false
	}
	if p, q, f, sl := e.closingTradeVWAP(ctx, pos); q > 0 {
		px, fee = p, f
		if sl && (reason == "" || reason == "stop" || reason == "closed_away") {
			reason = "stop"
		}
		return px, fee, reason, true
	}
	if nextSoon || e.HTTP == nil || (reason != "stop" && reason != "closed_away") {
		return px, fee, reason, false
	}
	// Native SL fill often lands in trades a beat after account goes flat.
	wait := 800 * time.Millisecond
	wctx, cancel := context.WithTimeout(context.Background(), wait)
	p, q, f, sl := e.waitClosingVWAP(wctx, pos)
	cancel()
	if q > 0 {
		px, fee = p, f
		if sl || reason == "stop" {
			reason = "stop"
		}
		return px, fee, reason, true
	}
	return px, fee, reason, false
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
	// fillWait<=0 means boundary/urgent close: at most one trades poll. Burning
	// 800ms here is what pushes decide(next) past T+2 after window_close.
	confirmBudget := fillWait
	boundaryFast := false
	if confirmBudget <= 0 {
		confirmBudget = time.Millisecond
		boundaryFast = true
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
		// Keep ExitClientIdx/Hash so refineExitAsync can still correct a bad VWAP.
		return fill.Px, fill.Fee, nil
	}
	// Close may already be done on the venue, but ref/FallbackPx is NOT a fill.
	// Returning 0 forces ensureExitPx / refineExitAsync to book from trades tape.
	if !boundaryFast {
		if flat, _, ferr := e.isFlat(ctx); ferr == nil && flat {
			e.Log.Warn("exit fill unconfirmed while flat; deferring px to tape", "client_idx", idx, "ref", ref)
			return 0, 0, nil
		}
	}
	if err != nil {
		return 0, 0, fmt.Errorf("exit unconfirmed after ambiguous/failed send: %w", err)
	}
	e.Log.Warn("exit fill unconfirmed; deferring px to tape", "client_idx", idx, "ref", ref, "boundary_fast", boundaryFast)
	return 0, 0, nil
}

func (e *Engine) refineExitAsync(pos *livePos, clientIdx int64, txHash string) {
	if e.HTTP == nil || pos == nil || e.Store == nil || clientIdx == 0 {
		return
	}
	windowTS := pos.WindowTS
	side := pos.Side
	entry := pos.EntryPx
	notional := pos.Notional
	entryFees := pos.Fees
	meta := e.Markets[e.Cfg.Symbol]
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		px, qty, fee, _ := e.HTTP.WaitFillFor(ctx, e.Cfg.AccountIndex, meta.MarketID, clientIdx, txHash, 0, 7*time.Second)
		if qty <= 0 || px <= 0 {
			e.Log.Warn("refine exit: fill still missing", "client_idx", clientIdx, "window_ts", windowTS)
			return
		}
		w, err := e.Store.GetWindow(ctx, windowTS)
		if err != nil {
			return
		}
		// Only overwrite when we improve on a missing/suspicious book (entry-as-exit).
		if w.LighterExitPx.Valid && w.LighterExitPx.Float64 > 0 && entry > 0 &&
			math.Abs(w.LighterExitPx.Float64-entry) >= 1e-6 &&
			math.Abs(w.LighterExitPx.Float64-px) < 1e-6 {
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
		e.Log.Info("refine exit booked", "window_ts", windowTS, "px", px, "client_idx", clientIdx)
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
	if e.flatProbe != nil {
		return e.flatProbe(ctx)
	}
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

// pollAccountFlat retries until flatness is known or budget expires.
// budget<=0 → single attempt.
func (e *Engine) pollAccountFlat(ctx context.Context, budget time.Duration) accountFlat {
	deadline := time.Now()
	if budget > 0 {
		deadline = deadline.Add(budget)
	}
	var last accountFlat
	for {
		flat, pos, err := e.isFlat(ctx)
		if err == nil {
			return accountFlat{Known: true, Flat: flat, Pos: pos}
		}
		last = accountFlat{}
		if budget <= 0 || !time.Now().Before(deadline) {
			return last
		}
		select {
		case <-ctx.Done():
			return last
		case <-time.After(80 * time.Millisecond):
		}
	}
}

func (e *Engine) detectExit(ctx context.Context, pos *livePos) (reason string, px, fee float64, ok bool) {
	if pos == nil {
		return "", 0, 0, false
	}
	// Brief grace: account/tape lag right after fill+stop placement.
	if !pos.ArmedAt.IsZero() && time.Since(pos.ArmedAt) < 2*time.Second {
		return "", 0, 0, false
	}
	fr := e.pollAccountFlat(ctx, 0)
	if !fr.Known {
		return "", 0, 0, false
	}
	if !fr.Flat {
		e.mu.Lock()
		if e.pos != nil {
			e.pos.SeenAcctOpen = true
		}
		e.mu.Unlock()
		return "", 0, 0, false
	}
	// Flat without ever observing open: only trust after enough age so a stale
	// post-entry flat reading cannot look like a remote close. If DNS hid the
	// whole open period, age≥5s still lets us settle a native SL.
	if !pos.SeenAcctOpen {
		if pos.ArmedAt.IsZero() || time.Since(pos.ArmedAt) < 5*time.Second {
			return "", 0, 0, false
		}
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

// resolveStopWatchdog is the local SLA backup when last/mark crosses stop.
// It must not CancelAll while flatness is unknown (that races a live exchange SL).
func (e *Engine) resolveStopWatchdog(ctx context.Context, pos *livePos) (reason string, px, fee float64, ok bool) {
	if pos == nil || pos.StopPx <= 0 {
		return "", 0, 0, false
	}
	if !pos.ArmedAt.IsZero() && time.Since(pos.ArmedAt) < 2*time.Second {
		return "", 0, 0, false
	}
	last, err := e.btcPx(ctx)
	if err != nil || !strategy.StopHit(last, pos.Side, pos.StopPx) {
		return "", 0, 0, false
	}
	e.Log.Warn("local stop watchdog hit", "px", last, "stop", pos.StopPx, "side", pos.Side)

	// Let a live native SL report flat before we send our own market close.
	fr := e.pollAccountFlat(ctx, 350*time.Millisecond)
	switch {
	case fr.Known && fr.Flat:
		px, fee = last, 0
		if e.HTTP != nil {
			wait := 800 * time.Millisecond
			exitAt := time.Unix(pos.WindowTS, 0).UTC().Add(time.Duration(clock.WindowSec)*time.Second - e.Cfg.ExitEarly)
			if remain := exitAt.Sub(e.now()); remain > 0 && remain < wait {
				wait = remain
			}
			wctx, cancel := context.WithTimeout(context.Background(), wait)
			p, q, f, sl := e.waitClosingVWAP(wctx, pos)
			cancel()
			if q > 0 {
				px, fee = p, f
				if sl || (pos.StopPx > 0 && strategy.StopLikeFill(p, pos.Side, pos.StopPx)) {
					return "stop", px, fee, true
				}
				return "closed_away", px, fee, true
			}
		}
		return "stop", px, fee, true
	case fr.Known && !fr.Flat:
		// True residual — settleWindow will CancelAll + market close.
		return "stop", last, 0, true
	default:
		// Unknown: still settle, but settleWindow keeps the exchange SL alive
		// (market close without CancelAll). Near-boundary ticks must not stall.
		e.Log.Warn("stop watchdog: account flatness unknown; market close without cancel")
		return "stop", last, 0, true
	}
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
