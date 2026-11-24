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

	workCtx := ctx
	if nextSoon {
		// One budget for a stop-tape read and the reduce. A hung call must not
		// start decide(next) after T+2. Window-close does not read the tape here.
		var cancel context.CancelFunc
		workCtx, cancel = context.WithDeadline(ctx, boundaryCloseDeadline(e.now(), deadline, e.Cfg.EntryMaxLag))
		defer cancel()
	}

	// Prefer exchange tape over last/mark for stop / remote-close paths.
	var fromTape bool
	px, fee, reason, fromTape = e.resolveExitTape(workCtx, pos, reason, px, fee, nextSoon)

	flat := accountFlat{}
	if !nextSoon {
		flat = e.pollAccountFlat(ctx, 400*time.Millisecond)
	}

	plan := planSettleClose(reason, flat, fromTape)
	fillWait := 800 * time.Millisecond
	if nextSoon {
		fillWait = 0
	}

	cancelledStop := false
	if plan.CancelAll && e.Signer != nil {
		if !closeBudgetLeft(workCtx, 700*time.Millisecond) {
			// Cancelling the stop and then missing the IOC leaves the position naked.
			e.Log.Warn("settle: leaving the stop, no time to cancel and close", "reason", reason)
			plan.CancelAll = false
			plan.MarketClose = false
		} else if err := e.Signer.CancelAll(workCtx); err != nil {
			e.Log.Warn("cancel orders during settle", "err", err, "reason", reason, "mode", plan.Mode)
		} else {
			cancelledStop = true
			if e.Store != nil {
				if err := e.Store.CancelOpenStops(ctx, pos.WindowTS); err != nil {
					e.Log.Warn("mark stops canceled", "err", err, "window_ts", pos.WindowTS)
				}
			}
		}
	}
	if plan.MarketClose {
		if plan.Mode == settleCloseResidual {
			e.Log.Warn("account still open after detected close, flattening", "reason", reason)
		} else if plan.Mode == settleCloseKeepStop {
			e.Log.Warn("flatten without cancel: account flatness unknown after stop", "reason", reason)
		}
		cpx, cfee, err := e.sendMarketClose(workCtx, pos, fillWait)
		if err != nil {
			e.alert(ctx, "error", "flatten", err.Error())
			if cancelledStop && lighter.IsPreConnectFail(err) {
				e.flagFlatten()
			}
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
				// One reduce at a time. A live close is not a reason to send another.
				if e.closeInFlight() {
					time.Sleep(150 * time.Millisecond)
					continue
				}
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
	// A missing tape fill is not the entry. Leave price and PnL empty until refineExitAsync.
	exitConfirmed := px > 0
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
	readyPx := 0.0
	if pos.CloseMark > 0 && (pos.EntryPx <= 0 || math.Abs(pos.CloseMark-pos.EntryPx) >= 1e-6) {
		readyPx = pos.CloseMark
	}
	if readyPx <= 0 && exitConfirmed {
		readyPx = px
	}
	e.markReadyAfterClose(readyPx, eq, exitConfirmed)
	ret := 0.0
	pnl := 0.0
	fees := pos.Fees + fee
	if exitConfirmed && pos.EntryPx > 0 {
		ret = strategy.Sign(pos.Side) * (px/pos.EntryPx - 1)
		pnl = pos.Notional*ret - fees
	}
	w, err := e.Store.GetWindow(ctx, pos.WindowTS)
	if err != nil {
		w = store.Window{WindowTS: pos.WindowTS, Side: pos.Side}
	}
	w.EquityAfter = sql.NullFloat64{Float64: eq, Valid: true}
	w.FeesUSD = sql.NullFloat64{Float64: fees, Valid: true}
	if exitConfirmed {
		w.LighterExitTS = sql.NullFloat64{Float64: e.unix(), Valid: true}
		w.LighterExitPx = sql.NullFloat64{Float64: px, Valid: true}
		w.PnLUSD = sql.NullFloat64{Float64: pnl, Valid: true}
		w.RetBps = sql.NullFloat64{Float64: ret * 1e4, Valid: true}
		w.State = "closed"
	} else {
		w.LighterExitTS = sql.NullFloat64{}
		w.LighterExitPx = sql.NullFloat64{}
		w.PnLUSD = sql.NullFloat64{}
		w.RetBps = sql.NullFloat64{}
		w.State = "exit_pending"
	}
	if reason == "stop" {
		w.ExitReason = "sl"
		if pos.StopPx > 0 && px > 0 {
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
	if exitConfirmed {
		e.alert(ctx, "info", "exit", fmt.Sprintf("%s pnl=%.2f ret=%.2fbps eq=%.2f", pos.Side, pnl, ret*1e4, eq))
	} else {
		e.alert(ctx, "warn", "exit", fmt.Sprintf("%s exit pending tape eq=%.2f", pos.Side, eq))
	}
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

// boundaryCloseDeadline is the latest moment cancel+IOC may run when the next
// window is about to open. The remainder of EntryMaxLag stays for decide.
func boundaryCloseDeadline(now, nextOpen time.Time, entryMaxLag time.Duration) time.Time {
	reserve := 1100 * time.Millisecond
	// Only shrink the decide reserve when the entry window itself is shorter than ~1.3s.
	if entryMaxLag <= reserve+200*time.Millisecond {
		reserve = entryMaxLag / 2
	}
	until := nextOpen.Add(entryMaxLag - reserve)
	if !now.Before(until) {
		return now.Add(800 * time.Millisecond)
	}
	return until
}

// skipPreCloseTape is true when a trades read would only delay the reduce.
// On the boundary the exit fill does not exist yet; refineExitAsync books it.
func skipPreCloseTape(reason string, px float64, nextSoon bool) bool {
	remote := reason == "stop" || reason == "closed_away"
	if remote {
		return false
	}
	return px > 0 || nextSoon
}

func (e *Engine) resolveExitTape(ctx context.Context, pos *livePos, reason string, px, fee float64, nextSoon bool) (float64, float64, string, bool) {
	if skipPreCloseTape(reason, px, nextSoon) {
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

// closeBudgetLeft reports whether the context still has room for a cancel plus the IOC.
func closeBudgetLeft(ctx context.Context, min time.Duration) bool {
	deadline, ok := ctx.Deadline()
	if !ok {
		return true
	}
	return time.Until(deadline) >= min
}

func (e *Engine) sendMarketClose(ctx context.Context, pos *livePos, fillWait time.Duration) (px, fee float64, err error) {
	if e.marketCloseProbe != nil {
		return e.marketCloseProbe(pos)
	}
	if err := ctx.Err(); err != nil {
		return 0, 0, fmt.Errorf("%w: %v", lighter.ErrNotSubmitted, err)
	}
	if e.Cfg.DryRun || e.Signer == nil {
		px, err = e.btcPx(ctx)
		if err == nil && px > 0 && pos != nil {
			pos.CloseMark = px
		}
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
	if pos != nil {
		pos.CloseMark = ref
	}
	buy := pos.Side == strategy.SideShort // closing short = buy
	idx := e.nextClientIdx(ctx)
	res, err := e.Signer.MarketIOC(ctx, e.Cfg.Symbol, buy, pos.Qty, ref, e.Cfg.MarketSlippage, idx, true)
	txHash := hashOf(res)
	// The boundary deadline may already be spent on sendTx. The order row still has to land.
	e.insertOrder(ctx, pos.WindowTS, "exit", errString(err, "sent"), idx, txHash, pos.Qty, ref, nil)
	if err != nil {
		if !lighter.IsAmbiguousSubmit(err) {
			e.failPendingClose(pos, idx, txHash)
			return 0, 0, err
		}
		e.Log.Warn("exit send ambiguous; reconciling", "err", err, "client_idx", idx)
		e.alert(ctx, "warn", "exit_send_ambiguous", fmt.Sprintf("ack lost (%v); reconciling by client_idx=%d", err, idx))
	}
	pos.ExitClientIdx = idx
	pos.ExitHash = txHash
	e.notePendingClose(pos, idx, txHash)
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
		WantQty:      pos.Qty,
		FallbackPx:   ref,
		SizeDecimals: meta.SizeDecimals,
		Budget:       confirmBudget,
		UsePosition:  false,
	})
	cancel()
	if fill.Qty > 0 && fill.Px > 0 {
		e.clearPendingClose()
		if e.Store != nil {
			wctx, cancel := persistCtx(ctx)
			_ = e.Store.UpdateOrderFill(wctx, idx, "filled", fill.Px)
			cancel()
		}
		return fill.Px, fill.Fee, nil
	}
	// Close may already be done on the venue, but ref/FallbackPx is NOT a fill.
	// Returning 0 forces ensureExitPx / refineExitAsync to book from trades tape.
	if !boundaryFast {
		if flat, _, ferr := e.isFlat(ctx); ferr == nil && flat {
			e.clearPendingClose()
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
		if err := e.bookExitFill(ctx, store.UnconfirmedExit{
			WindowTS: windowTS, Side: side, EntryPx: entry, Notional: notional, Fees: entryFees, ClientIdx: clientIdx,
		}, px, fee); err != nil {
			e.Log.Warn("refine exit book", "err", err, "window_ts", windowTS, "client_idx", clientIdx)
			return
		}
		e.Log.Info("refine exit booked", "window_ts", windowTS, "px", px, "client_idx", clientIdx)
	}()
}

func (e *Engine) bookExitFill(ctx context.Context, row store.UnconfirmedExit, px, fee float64) error {
	if e.Store == nil || row.WindowTS == 0 || px <= 0 {
		return nil
	}
	w, err := e.Store.GetWindow(ctx, row.WindowTS)
	if err != nil {
		return err
	}
	entry := row.EntryPx
	if entry <= 0 && w.LighterEntryPx.Valid {
		entry = w.LighterEntryPx.Float64
	}
	if w.LighterExitPx.Valid && w.LighterExitPx.Float64 > 0 && entry > 0 &&
		math.Abs(w.LighterExitPx.Float64-entry) >= 1e-6 {
		if math.Abs(w.LighterExitPx.Float64-px) < 1e-6 && row.ClientIdx != 0 {
			_ = e.Store.UpdateOrderFill(ctx, row.ClientIdx, "filled", px)
		}
		return nil
	}
	fees := row.Fees + fee
	if w.FeesUSD.Valid && w.FeesUSD.Float64 > fees {
		fees = w.FeesUSD.Float64
	}
	notional := row.Notional
	if notional <= 0 && w.Notional.Valid {
		notional = w.Notional.Float64
	}
	ret := 0.0
	pnl := -fees
	if entry > 0 {
		ret = strategy.Sign(row.Side) * (px/entry - 1)
		pnl = notional*ret - fees
	}
	reason := w.ExitReason
	if reason == "" {
		reason = "window_close"
	}
	if err := e.Store.UpdateWindowExit(ctx, row.WindowTS, e.unix(), px, pnl, ret*1e4, fees, "closed", reason); err != nil {
		return err
	}
	if row.ClientIdx != 0 {
		if err := e.Store.UpdateOrderFill(ctx, row.ClientIdx, "filled", px); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) repairUnconfirmedExits(ctx context.Context) {
	if e.HTTP == nil || e.Store == nil || e.Cfg == nil {
		return
	}
	since := e.now().Add(-24 * time.Hour).Unix()
	rows, err := e.Store.ListUnconfirmedExits(ctx, since)
	if err != nil {
		e.Log.Warn("repair exits", "err", err)
		return
	}
	if len(rows) == 0 {
		return
	}
	meta, ok := e.Markets[e.Cfg.Symbol]
	if !ok {
		e.Log.Warn("repair exits: market missing", "symbol", e.Cfg.Symbol)
		return
	}
	fills, err := e.HTTP.AccountTrades(ctx, e.Cfg.AccountIndex, meta.MarketID, 100)
	if err != nil {
		e.Log.Warn("repair exits: trades", "err", err, "n", len(rows))
		return
	}
	for _, row := range rows {
		px, qty, fee, _ := lighter.VWAP(lighter.FillsMatching(fills, row.ClientIdx, row.TxHash))
		if qty <= 0 || px <= 0 {
			continue
		}
		if err := e.bookExitFill(ctx, row, px, fee); err != nil {
			e.Log.Warn("repair exit", "err", err, "window_ts", row.WindowTS)
			continue
		}
		e.Log.Info("repaired exit from tape", "window_ts", row.WindowTS, "px", px, "client_idx", row.ClientIdx)
	}
}

func (e *Engine) closeInFlight() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.pending != nil && !e.pending.Failed && e.pending.ClientIdx != 0
}

func (e *Engine) notePendingClose(pos *livePos, idx int64, hash string) {
	if pos == nil || idx == 0 {
		return
	}
	e.mu.Lock()
	e.pending = &pendingClose{WindowTS: pos.WindowTS, Side: pos.Side, ClientIdx: idx, TxHash: hash}
	e.mu.Unlock()
}

func (e *Engine) failPendingClose(pos *livePos, idx int64, hash string) {
	e.mu.Lock()
	e.pending = &pendingClose{WindowTS: pos.WindowTS, Side: pos.Side, ClientIdx: idx, TxHash: hash, Failed: true}
	e.mu.Unlock()
}

func (e *Engine) clearPendingClose() {
	e.mu.Lock()
	e.pending = nil
	e.mu.Unlock()
}

func (e *Engine) failInFlight() {
	e.mu.Lock()
	if e.pending != nil {
		e.pending.Failed = true
	}
	e.mu.Unlock()
}

func (e *Engine) pendingWindow() int64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.pending == nil {
		return 0
	}
	return e.pending.WindowTS
}

func (e *Engine) flagFlatten() {
	e.mu.Lock()
	e.needsFlatten = true
	e.mu.Unlock()
}

// pendingFillBooked writes a tape or stream fill onto the original window.
func (e *Engine) pendingFillBooked(ctx context.Context) bool {
	e.mu.Lock()
	p := e.pending
	e.mu.Unlock()
	if p == nil || p.ClientIdx == 0 || e.HTTP == nil || e.Cfg == nil {
		return false
	}
	px, qty, fee, ok := 0.0, 0.0, 0.0, false
	if e.HTTP.Stream != nil {
		var terminal bool
		px, qty, fee, terminal, ok = e.HTTP.Stream.FillState(p.ClientIdx)
		if !terminal {
			ok = false
		}
	}
	if !ok || qty <= 0 || px <= 0 {
		meta, mok := e.Markets[e.Cfg.Symbol]
		if !mok {
			return false
		}
		cctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
		fills, err := e.HTTP.AccountTradesUrgent(cctx, e.Cfg.AccountIndex, meta.MarketID, 100)
		cancel()
		if err != nil {
			return false
		}
		px, qty, fee, _ = lighter.VWAP(lighter.FillsMatching(fills, p.ClientIdx, p.TxHash))
	}
	if qty <= 0 || px <= 0 {
		return false
	}
	if err := e.bookExitFill(ctx, store.UnconfirmedExit{
		WindowTS: p.WindowTS, Side: p.Side, ClientIdx: p.ClientIdx, TxHash: p.TxHash,
	}, px, fee); err != nil {
		e.Log.Warn("book in-flight exit", "err", err, "window_ts", p.WindowTS)
		return false
	}
	return true
}

// confirmFlatForEntry blocks a new entry while the previous reduce is still
// working or the account is not flat. It does not send another order.
func (e *Engine) confirmFlatForEntry(ctx context.Context, deadline time.Time) error {
	if e.Cfg.DryRun || (e.HTTP == nil && e.flatProbe == nil) {
		return nil
	}
	budgetEnd := deadline.Add(-50 * time.Millisecond)
	for {
		flat, _, err := e.isFlat(ctx)
		if err == nil && flat {
			e.clearPendingClose()
			e.mu.Lock()
			e.readyOK = e.readyEq > 0 && e.readyPx > 0
			e.mu.Unlock()
			return nil
		}
		if err == nil && !flat {
			if e.closeInFlight() {
				return fmt.Errorf("previous close still open")
			}
			return fmt.Errorf("account not flat")
		}
		if !time.Now().Before(budgetEnd) {
			if err != nil {
				return fmt.Errorf("flatness unknown: %w", err)
			}
			return fmt.Errorf("account not flat")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(80 * time.Millisecond):
		}
	}
}

func (e *Engine) ensureFlat(ctx context.Context, why string) error {
	if why == "prewarm" || why == "skip" {
		e.mu.Lock()
		live := e.pos
		e.mu.Unlock()
		if live != nil {
			return nil
		}
		if e.closeInFlight() {
			flat, _, err := e.isFlat(ctx)
			if err == nil && flat {
				e.clearPendingClose()
				return nil
			}
			if err != nil {
				e.Log.Warn("flatten deferred; close in flight", "why", why, "err", err)
				return nil
			}
			// The account is still open. One tape read, then one new reduce.
			if e.pendingFillBooked(ctx) {
				e.clearPendingClose()
				return nil
			}
			e.failInFlight()
		}
	}
	if e.Cfg.DryRun || (e.Signer == nil && e.marketCloseProbe == nil) {
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
	origin := e.pendingWindow()
	if origin == 0 {
		origin = clock.WindowOf(e.now())
	}
	live := &livePos{WindowTS: origin, Qty: pos.Size, Side: strategy.SideLong}
	if pos.Sign < 0 {
		live.Side = strategy.SideShort
	}
	for i := 0; i < 15; i++ {
		if live.Qty <= 0 {
			break
		}
		if !e.closeInFlight() {
			_, _, err := e.sendMarketClose(ctx, live, 0)
			if err != nil {
				e.Log.Warn("flatten retry", "err", err)
			}
		}
		time.Sleep(200 * time.Millisecond)
		flat, cur, ferr := e.isFlat(ctx)
		if ferr != nil {
			continue
		}
		if flat {
			e.clearPendingClose()
			if e.Signer != nil {
				if err := e.Signer.CancelAll(ctx); err != nil {
					e.Log.Warn("cancel residual orders after flatten", "why", why, "err", err)
				}
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
