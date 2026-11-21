package engine

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"time"

	"poly-lighter.trade/bot/internal/exchange/lighter"
	"poly-lighter.trade/bot/internal/store"
	"poly-lighter.trade/bot/internal/strategy"
)

func (e *Engine) arm(ctx context.Context, ts int64) error {
	if e.PM == nil {
		return fmt.Errorf("polymarket client is nil")
	}
	e.mu.Lock()
	holding := e.pos != nil
	e.mu.Unlock()
	if !holding {
		e.setState(StateArming)
	}
	slug := e.Cfg.EventSlug(ts)
	mkt, err := e.PM.Resolve(ctx, slug)
	if err != nil {
		e.mu.Lock()
		e.armedFor = 0
		e.mu.Unlock()
		return err
	}
	e.mu.Lock()
	e.armedFor = ts
	e.armedMarket = mkt
	e.mu.Unlock()
	e.PM.Arm(ts, mkt)
	warmCh := make(chan time.Duration, 1)
	go func() {
		wctx, cancel := context.WithTimeout(ctx, 1200*time.Millisecond)
		defer cancel()
		started := time.Now()
		e.PM.WarmREST(wctx, mkt)
		warmCh <- time.Since(started)
	}()
	if e.Signer != nil && !e.Cfg.DryRun {
		nctx, cancel := context.WithTimeout(ctx, 700*time.Millisecond)
		tNonce := time.Now()
		if err := e.Signer.PrepareNonce(nctx); err != nil {
			e.Log.Warn("prewarm nonce", "err", err, "ts", ts)
		}
		e.traceDur("nonce", tNonce)
		cancel()
	}
	select {
	case elapsed := <-warmCh:
		e.traceDurElapsed("warm_book", elapsed)
	case <-ctx.Done():
	}
	e.Log.Info("armed", "ts", ts, "slug", slug)
	return nil
}

func (e *Engine) decide(ctx context.Context, ts int64) error {
	e.beginTrace(ts)
	e.stampLateStart()
	if e.isHalted() {
		e.setState(StateHalt)
	} else {
		e.setState(StateDecide)
	}
	e.Log.Info("decide", "ts", ts, "elapsed_ms", e.now().Sub(time.Unix(ts, 0).UTC()).Milliseconds())
	if e.consumedTS == ts {
		return e.sleepToExit(ctx, ts)
	}
	tHas := time.Now()
	exists, err := e.Store.HasWindow(ctx, ts)
	e.traceDur("has_window", tHas)
	if err != nil {
		return err
	}
	if exists {
		e.consumedTS = ts
		return e.sleepToExit(ctx, ts)
	}
	e.consumedTS = ts

	w := store.Window{WindowTS: ts, Side: strategy.SideSkip, State: "decide"}
	e.stampClock(&w)

	if e.isHalted() {
		return e.finishSkip(ctx, w, "halt", "halted")
	}

	_, earliest, latest := e.entryElapsed(ts)
	if earliest < 0 {
		return e.finishSkip(ctx, w, "clock", "system UTC is still before window open")
	}
	if latest >= e.Cfg.EntryMaxLag {
		return e.finishSkip(ctx, w, "late", "past T+2s")
	}

	e.mu.Lock()
	mkt := e.armedMarket
	armed := e.armedFor == ts
	eq, px := e.readyEq, e.readyPx
	e.mu.Unlock()
	if !armed {
		tArm := time.Now()
		if err := e.arm(ctx, ts); err != nil {
			return e.finishSkip(ctx, w, "no_market", err.Error())
		}
		e.traceDur("arm", tArm)
		e.mu.Lock()
		mkt = e.armedMarket
		e.mu.Unlock()
	}
	if mkt.TokenUp == "" {
		return e.finishSkip(ctx, w, "no_market", "empty tokens")
	}

	deadline := e.entryDeadline(ts)
	qctx, qcancel := context.WithDeadline(ctx, deadline)
	tQ := time.Now()
	q, err := e.PM.SnapshotUp(qctx, mkt, e.now(), time.Second)
	qcancel()
	e.traceDur("quote", tQ)
	elapsed, _, _ := e.entryElapsed(ts)
	e.traceOpenTo("quote", time.Unix(ts, 0).Add(elapsed))
	if err != nil {
		if _, _, latest := e.entryElapsed(ts); latest >= e.Cfg.EntryMaxLag {
			return e.finishSkip(ctx, w, "late", "quote unavailable before T+2s: "+err.Error())
		}
		return e.finishSkip(ctx, w, "bad_quote", err.Error())
	}
	ok, why := strategy.QuoteOK(q, e.Cfg.MaxSpread, 1.0, e.now())
	if !ok {
		w.QuoteSource = q.Source
		w.PUp = q.MidUp()
		w.Spread = q.Spread()
		return e.finishSkip(ctx, w, why, why)
	}
	pUp := q.MidUp()
	pDown := 1 - pUp
	if q.HasDown && q.BidDown > 0 && q.AskDown > 0 {
		pDown = (q.BidDown + q.AskDown) / 2
	}
	side, skipReason, both := strategy.DecideSide(pUp, e.Cfg.Threshold)
	decisionTime := e.entryClock()
	w.DecisionTS = float64(decisionTime.UnixNano()) / 1e9
	quoteTime := e.correctedTime(q.Recv)
	w.QuoteTS = float64(quoteTime.UnixNano()) / 1e9
	w.PUp, w.PDown, w.Spread = pUp, pDown, q.Spread()
	w.QuoteSource = q.Source
	w.PMFav = sql.NullFloat64{Float64: math.Max(pUp, pDown), Valid: true}
	e.traceOpenTo("decide", e.correctedTime(time.Now()).UTC())
	e.mu.Lock()
	e.lastQuote = q
	e.mu.Unlock()
	e.Log.Info("quote", "ts", ts, "source", q.Source, "p_up", pUp, "p_down", pDown, "spread", q.Spread(), "side", side, "skip", skipReason, "bid_up", q.BidUp, "ask_up", q.AskUp)

	if both {
		e.alert(ctx, "error", "quote", fmt.Sprintf("обе стороны ≥ %.2f — skip", e.Cfg.Threshold))
		return e.finishSkip(ctx, w, "both_ge_th", "both >= threshold")
	}
	if side == strategy.SideSkip {
		return e.finishSkip(ctx, w, skipReason, skipReason)
	}
	w.EntryLagMS = sql.NullFloat64{Float64: (w.DecisionTS - float64(ts)) * 1000, Valid: true}

	if _, _, latest := e.entryElapsed(ts); latest >= e.Cfg.EntryMaxLag {
		return e.finishSkip(ctx, w, "late", "late after quote")
	}

	e.mu.Lock()
	readyOK, readyAt := e.readyOK, e.readyAt
	e.mu.Unlock()
	if !readyOK || eq <= 0 || px <= 0 || time.Since(readyAt) > 5*time.Second {
		return e.finishSkip(ctx, w, "ready", "equity/price/flat state is not fresh from prewarm")
	}
	meta := e.Markets[e.Cfg.Symbol]
	qty := strategy.FloorLot((eq*e.Cfg.EquityFraction*e.Cfg.Leverage)/px, meta.SizeDecimals)
	if meta.MinBaseAmount > 0 && qty < meta.MinBaseAmount {
		return e.finishSkip(ctx, w, "equity_too_small", fmt.Sprintf("qty=%v min=%v", qty, meta.MinBaseAmount))
	}
	if qty <= 0 {
		return e.finishSkip(ctx, w, "equity_too_small", "qty 0")
	}
	if qty*px < eq*e.Cfg.EquityFraction*0.95 {
		e.alert(ctx, "warn", "lot_rounding", fmt.Sprintf("notional=%.2f equity=%.2f", qty*px, eq))
	}

	clientIdx := e.nextClientIdx(ctx)
	buy := side == strategy.SideLong
	w.Side = side
	w.Qty = sql.NullFloat64{Float64: qty, Valid: true}
	w.Notional = sql.NullFloat64{Float64: qty * px, Valid: true}
	w.EquityBefore = sql.NullFloat64{Float64: eq, Valid: true}

	if e.Cfg.DryRun || e.Signer == nil {
		stopPx := strategy.StopPx(px, side, e.Cfg.StopPricePct)
		stopIdx := e.nextClientIdx(ctx)
		_ = e.Store.InsertOrder(ctx, ts, "entry", "dry_run", clientIdx, "", qty, px, map[string]any{"side": side})
		_ = e.Store.InsertOrder(ctx, ts, "stop_loss", "dry_run", stopIdx, "", qty, stopPx, map[string]any{"trigger_price": stopPx, "reduce_only": true, "paper": true})
		e.mu.Lock()
		e.pos = &livePos{
			WindowTS: ts, Side: side, Qty: qty, Notional: qty * px, EntryPx: px, EntryTS: e.unix(),
			EquityBefore: eq, ClientIdx: clientIdx, StopClientIdx: stopIdx, StopPx: stopPx, ArmedAt: time.Now(),
		}
		e.mu.Unlock()
		w.LighterEntryTS = sql.NullFloat64{Float64: e.unix(), Valid: true}
		w.LighterEntryPx = sql.NullFloat64{Float64: px, Valid: true}
		if stopPx > 0 {
			w.StopPx = sql.NullFloat64{Float64: stopPx, Valid: true}
		}
		w.State = StateInPosition
		e.saveWindow(ctx, w)
		e.setState(StateInPosition)
		e.flogWindow("entry", w)
		e.alert(ctx, "info", "entry", fmt.Sprintf("DRY %s qty=%.6f px=%.2f stop=%.2f p_up=%.3f", side, qty, px, stopPx, pUp))
		return nil
	}

	if _, _, latest := e.entryElapsed(ts); latest >= e.Cfg.EntryMaxLag {
		return e.finishSkip(ctx, w, "late", "deadline expired before order request")
	}
	// Do not fire a blind entry if Lighter is unreachable right now (DNS/dial).
	if err := e.venuePreflight(ctx, deadline); err != nil {
		e.mu.Lock()
		e.readyOK = false
		e.mu.Unlock()
		return e.finishSkip(ctx, w, "venue", err.Error())
	}
	sendCtx, sendCancel := context.WithDeadline(ctx, deadline)
	tSend := time.Now()
	e.traceOpenTo("send", e.correctedTime(tSend).UTC())
	res, err := e.Signer.MarketIOC(sendCtx, e.Cfg.Symbol, buy, qty, px, e.Cfg.MarketSlippage, clientIdx, false)
	sendCancel()
	e.traceDur("send", tSend)
	sendAckAt := e.correctedTime(time.Now()).UTC()
	e.traceOpenTo("send_ack", sendAckAt)
	w.State = "sent"
	e.saveWindow(ctx, w)
	txHash := hashOf(res)
	_ = e.Store.InsertOrder(ctx, ts, "entry", errString(err, "sent"), clientIdx, txHash, qty, px, nil)
	if err != nil {
		// DNS/dial failures never reached the venue → clean skip.
		// Timeout/reset after the POST may have left the process → reconcile.
		if !lighter.IsAmbiguousSubmit(err) {
			reason := "order_failed"
			if lighter.IsPreConnectFail(err) {
				reason = "venue"
			}
			return e.finishSkip(ctx, w, reason, err.Error())
		}
		e.Log.Warn("entry send ambiguous; reconciling", "err", err, "client_idx", clientIdx)
		e.alert(ctx, "warn", "entry_send_ambiguous", fmt.Sprintf("ack lost (%v); reconciling by client_idx=%d", err, clientIdx))
	}

	tFill := time.Now()
	fillPx, fillQty, fee, fillUnix, oversize := e.confirmLiveFill(ctx, meta, clientIdx, txHash, qty, px)
	e.traceDur("fill_wait", tFill)
	if oversize != nil {
		_ = e.ensureFlat(ctx, "unexpected_entry_size")
		e.halt(ctx, "unexpected_entry_size")
		return e.finishSkip(ctx, w, "unexpected_entry_size", oversize.Error())
	}
	if fillPx <= 0 {
		fillPx = px
	}
	if fillQty <= 0 {
		e.alert(ctx, "error", "entry", "ордер отправлен, филла нет — flatten")
		flattenErr := e.ensureFlat(ctx, "entry_unfilled")
		if flattenErr != nil {
			e.Log.Error("flatten after unconfirmed entry failed", "err", flattenErr)
			e.alert(ctx, "error", "entry_unconfirmed", "не подтверждённый вход; аварийное закрытие не подтверждено, HALT")
		} else if err := e.Signer.CancelAll(ctx); err != nil {
			// Only cancel after flat has been confirmed. If flatten failed, retain
			// any protective reduce-only order while the bot is halted.
			e.Log.Error("cancel orders after unconfirmed entry failed", "err", err)
			e.alert(ctx, "error", "entry_unconfirmed", "позиция проверена flat, но отмена остаточных ордеров не подтверждена; HALT")
		}
		w.SkipReason = "entry_unfilled_after_send"
		w.Side = strategy.SideSkip
		w.ExitReason = "skip"
		w.FillLagMS = sql.NullFloat64{}
		e.stampClock(&w)
		e.bumpLateStreak(ctx, true)
		// A sent entry with no confirmed fill is an ambiguous exchange state.
		// Stop all future entries even if the immediate position query said flat.
		e.halt(ctx, "entry_unconfirmed")
		e.saveWindow(ctx, w)
		e.flogWindow("skip", w)
		return e.sleepToExit(ctx, ts)
	}
	e.bumpLateStreak(ctx, false)
	usedQty := fillQty
	if usedQty > qty {
		usedQty = qty
	}
	fillTS := fillUnix
	var fillLag sql.NullFloat64
	stopPx := strategy.StopPx(fillPx, side, e.Cfg.StopPricePct)
	stopIdx := int64(0)
	stopHash := ""
	if stopPx > 0 {
		stopIdx = e.nextClientIdx(ctx)
		tStop := time.Now()
		sctx, scancel := context.WithTimeout(ctx, 1500*time.Millisecond)
		stopResult, stopErr := e.Signer.StopLoss(sctx, e.Cfg.Symbol, side == strategy.SideLong, usedQty, stopPx, e.Cfg.MarketSlippage, stopIdx)
		scancel()
		e.traceDur("stop", tStop)
		stopHash = hashOf(stopResult)
		if stopErr != nil && lighter.IsAmbiguousSubmit(stopErr) {
			e.Log.Warn("stop send ambiguous; reconciling", "err", stopErr, "client_idx", stopIdx)
			if e.reconcileStopPlaced(ctx, stopIdx) {
				stopErr = nil
				if stopHash == "" {
					stopHash = fmt.Sprintf("reconciled:%d", stopIdx)
				}
			}
		}
		status := errString(stopErr, "sent")
		_ = e.Store.InsertOrder(ctx, ts, "stop_loss", status, stopIdx, stopHash, usedQty, stopPx, map[string]any{"trigger_price": stopPx, "reduce_only": true})
		if stopErr != nil {
			e.alert(ctx, "error", "stop_missing", fmt.Sprintf("не удалось поставить стоп %.2f после fill %.2f: %v; закрываю позицию", stopPx, fillPx, stopErr))
			closeErr := e.ensureFlat(ctx, "stop_missing")
			if cancelErr := e.Signer.CancelAll(ctx); cancelErr != nil {
				e.Log.Warn("cancel orders after stop failure", "err", cancelErr)
			}
			e.halt(ctx, "stop_missing")
			if closeErr != nil {
				return fmt.Errorf("place stop: %w; emergency flatten: %v", stopErr, closeErr)
			}
			return e.finishSkip(ctx, w, "stop_missing", stopErr.Error())
		}
	}
	// Query exchange creation time only after the protective stop has been
	// submitted; telemetry must never delay risk protection.
	octx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	tOrd := time.Now()
	if o, err := e.HTTP.OrderByClient(octx, e.Cfg.AccountIndex, clientIdx); err == nil {
		if u := o.CreatedUnix(); u > 0 {
			fillLag = sql.NullFloat64{Float64: (u - float64(ts)) * 1000, Valid: true}
			fillTS = u
		}
	}
	e.traceDur("order_get", tOrd)
	cancel()
	if !fillLag.Valid {
		fillLag = sql.NullFloat64{Float64: float64(sendAckAt.Sub(time.Unix(ts, 0).UTC()) / time.Millisecond), Valid: true}
	}
	if fillLag.Valid && e.tr != nil {
		e.tr.t.MsOpenToExchange = fillLag.Float64
	}
	e.mu.Lock()
	e.pos = &livePos{
		WindowTS: ts, Side: side, Qty: usedQty, Notional: usedQty * fillPx,
		EntryPx: fillPx, EntryTS: fillTS, EquityBefore: eq, ClientIdx: clientIdx, Fees: fee,
		StopClientIdx: stopIdx, StopHash: stopHash, StopPx: stopPx,
		FillLagMS: fillLag.Float64, ArmedAt: time.Now(), EntryHash: txHash,
	}
	e.mu.Unlock()
	entryTS := fillTS
	if entryTS <= 0 {
		entryTS = e.unix()
	}
	w.LighterEntryTS = sql.NullFloat64{Float64: entryTS, Valid: true}
	w.LighterEntryPx = sql.NullFloat64{Float64: fillPx, Valid: true}
	w.Qty = sql.NullFloat64{Float64: usedQty, Valid: true}
	w.Notional = sql.NullFloat64{Float64: usedQty * fillPx, Valid: true}
	w.FeesUSD = sql.NullFloat64{Float64: fee, Valid: true}
	w.FillLagMS = fillLag
	w.State = StateInPosition
	if stopPx > 0 {
		w.StopPx = sql.NullFloat64{Float64: stopPx, Valid: true}
	}
	e.stampClock(&w)
	e.saveWindow(ctx, w)
	e.setState(StateInPosition)
	e.flogWindow("entry", w)
	lagTxt := "—"
	if w.FillLagMS.Valid {
		lagTxt = fmt.Sprintf("%.0fms", w.FillLagMS.Float64)
	}
	e.alert(ctx, "info", "entry", fmt.Sprintf("%s qty=%.6f px=%.2f p_up=%.3f lag=%s", side, usedQty, fillPx, pUp, lagTxt))
	return nil
}

// venuePreflight checks that Lighter answers before we submit an entry.
// Failures here skip the window without sending (reason venue).
func (e *Engine) venuePreflight(ctx context.Context, entryDeadline time.Time) error {
	if e.Cfg.DryRun || e.HTTP == nil {
		return nil
	}
	budget := 400 * time.Millisecond
	if remain := time.Until(entryDeadline); remain > 0 && remain < budget {
		budget = remain
	}
	if budget <= 0 {
		return fmt.Errorf("lighter preflight: no time left before entry deadline")
	}
	pctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	if _, err := e.HTTP.AccountUrgent(pctx, e.Cfg.AccountIndex); err != nil {
		return fmt.Errorf("lighter unreachable: %w", err)
	}
	return nil
}

func (e *Engine) finishSkip(ctx context.Context, w store.Window, reason, msg string) error {
	w.Side = strategy.SideSkip
	w.SkipReason = reason
	w.ExitReason = "skip"
	w.State = "skip"
	w.EntryLagMS = sql.NullFloat64{}
	w.FillLagMS = sql.NullFloat64{}
	e.stampClock(&w)
	if w.DecisionTS == 0 {
		w.DecisionTS = float64(e.entryClock().UnixNano()) / 1e9
	}
	e.saveWindow(ctx, w)
	e.mu.Lock()
	e.lastSkip = reason
	e.lastWindow = w.WindowTS
	e.mu.Unlock()
	e.flogWindow("skip", w)
	e.Log.Info("skip", "ts", w.WindowTS, "reason", reason, "msg", msg)
	_ = e.ensureFlat(ctx, "skip")
	return e.sleepToExit(ctx, w.WindowTS)
}

const entryFillConfirm = 5 * time.Second

func (e *Engine) confirmLiveFill(ctx context.Context, meta lighter.MarketMeta, clientIdx int64, txHash string, wantQty, fallbackPx float64) (px, qty, fee, unix float64, oversized error) {
	if e.HTTP == nil {
		return fallbackPx, 0, 0, 0, nil
	}
	res := e.HTTP.ConfirmFill(ctx, lighter.ConfirmFillOpts{
		AccountIndex: e.Cfg.AccountIndex,
		MarketID:     meta.MarketID,
		Symbol:       e.Cfg.Symbol,
		ClientIdx:    clientIdx,
		TxHash:       txHash,
		WantQty:      wantQty,
		FallbackPx:   fallbackPx,
		SizeDecimals: meta.SizeDecimals,
		Budget:       entryFillConfirm,
		UsePosition:  true,
	})
	if res.Qty <= 0 {
		e.Log.Warn("fill not confirmed", "client_idx", clientIdx, "hash", txHash, "want_qty", wantQty)
	}
	return res.Px, res.Qty, res.Fee, res.Unix, res.Oversize
}

func (e *Engine) reconcileStopPlaced(ctx context.Context, stopIdx int64) bool {
	if e.HTTP == nil || stopIdx == 0 {
		return false
	}
	deadline := time.Now().Add(1500 * time.Millisecond)
	for {
		octx, cancel := context.WithTimeout(ctx, 400*time.Millisecond)
		_, err := e.HTTP.OrderByClient(octx, e.Cfg.AccountIndex, stopIdx)
		cancel()
		if err == nil {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(100 * time.Millisecond):
		}
	}
}
