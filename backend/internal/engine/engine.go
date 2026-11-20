package engine

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"time"

	"poly-lighter.trade/bot/internal/binance"
	"poly-lighter.trade/bot/internal/clock"
	"poly-lighter.trade/bot/internal/config"
	"poly-lighter.trade/bot/internal/exchange/lighter"
	"poly-lighter.trade/bot/internal/flog"
	"poly-lighter.trade/bot/internal/notify"
	"poly-lighter.trade/bot/internal/polymarket"
	"poly-lighter.trade/bot/internal/store"
	"poly-lighter.trade/bot/internal/strategy"
)

const (
	StateIdle       = "IDLE"
	StateArming     = "ARMING"
	StateDecide     = "DECIDE"
	StateInPosition = "IN_POSITION"
	StateFlattening = "FLATTENING"
	StateHalt       = "HALT"
)

type livePos struct {
	WindowTS      int64
	Side          string
	Qty           float64
	Notional      float64
	EntryPx       float64
	EntryTS       float64
	EquityBefore  float64
	ClientIdx     int64
	StopClientIdx int64
	StopPx        float64
	Fees          float64
	FillLagMS     float64
}

type Engine struct {
	Cfg     *config.Config
	Store   *store.Store
	HTTP    *lighter.HTTPClient
	Signer  *lighter.Signer
	Markets map[string]lighter.MarketMeta
	PM      *polymarket.Client
	BN      *binance.Client
	Notify  notify.Notifier
	Flog    *flog.Logger
	Log     *slog.Logger

	mu          sync.Mutex
	state       string
	offset      time.Duration
	pos         *livePos
	lastQuote   strategy.Quote
	lastSkip    string
	lastWindow  int64
	armedFor    int64
	armedMarket polymarket.Market
	wsPM        string
	started     time.Time
	global      store.GlobalState
	bootWindow  int64
}

func New(cfg *config.Config, st *store.Store, httpClient *lighter.HTTPClient, signer *lighter.Signer, markets map[string]lighter.MarketMeta, pm *polymarket.Client, bn *binance.Client, n notify.Notifier, log *slog.Logger) *Engine {
	if n == nil {
		n = notify.Nop{}
	}
	if log == nil {
		log = slog.Default()
	}
	return &Engine{
		Cfg: cfg, Store: st, HTTP: httpClient, Signer: signer, Markets: markets,
		PM: pm, BN: bn, Notify: n, Log: log, state: StateIdle, started: time.Now(),
	}
}

func (e *Engine) now() time.Time {
	return time.Now().Add(e.offset)
}

func (e *Engine) unix() float64 {
	return float64(e.now().UnixNano()) / 1e9
}

func (e *Engine) Bootstrap(ctx context.Context) error {
	g, err := e.Store.LoadGlobal(ctx)
	if err != nil {
		return err
	}
	if e.Cfg.KillSwitch {
		g.KillSwitch = true
	}
	if g.GoLiveTS == 0 {
		g.GoLiveTS = time.Now().Unix()
	}
	e.global = g
	e.bootWindow = clock.WindowOf(e.now())
	if e.Signer != nil && !e.Cfg.DryRun && e.Cfg.Leverage >= 1 {
		if err := e.Signer.UpdateLeverage(e.Cfg.Symbol, e.Cfg.Leverage); err != nil {
			e.Log.Warn("update leverage", "err", err, "leverage", e.Cfg.Leverage)
			e.alert(ctx, "warn", "leverage", fmt.Sprintf("не выставили %gx на бирже: %v", e.Cfg.Leverage, err))
		} else {
			e.Log.Info("leverage set", "symbol", e.Cfg.Symbol, "leverage", e.Cfg.Leverage)
		}
	}
	if err := e.ensureFlat(ctx, "boot"); err != nil {
		e.Log.Error("boot flatten", "err", err)
		e.halt(ctx, "boot_flatten")
	}
	eq, _ := e.equity(ctx)
	if eq > 0 && g.StartEquity == 0 {
		g.StartEquity = eq
		g.PeakEquity = eq
		g.ShadowEquity = eq
	}
	if eq > g.PeakEquity {
		g.PeakEquity = eq
	}
	e.global = g
	_ = e.Store.SaveGlobal(ctx, g)
	_ = e.Store.InsertEquity(ctx, time.Now().Unix(), "live", eq)
	e.alert(ctx, "info", "boot", fmt.Sprintf("POLY_LIGHTER_1 онлайн · %s · lev=%.1fx · frac=%.2f · equity=%.2f", e.Cfg.Network, e.Cfg.Leverage, e.Cfg.EquityFraction, eq))
	return nil
}

func (e *Engine) Run(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		if err := e.tick(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			e.Log.Error("tick", "err", err)
			time.Sleep(200 * time.Millisecond)
		}
	}
}

func (e *Engine) tick(ctx context.Context) error {
	if e.isHalted() {
		e.setState(StateHalt)
		_ = e.ensureFlat(ctx, "halt")
		return clock.SleepUntil(ctx, time.Now().Add(time.Second), time.Now)
	}
	now := e.now()
	ts := clock.WindowOf(now)

	e.mu.Lock()
	pos := e.pos
	e.mu.Unlock()
	if pos != nil {
		// A native reduce-only stop may have closed the position while this
		// process was asleep. Detect that before waiting for the window close.
		if !e.Cfg.DryRun && e.Signer != nil {
			flat, _ := e.isFlat(ctx)
			if flat {
				return e.flattenWindow(ctx, pos, "stop")
			}
		}
		if now.Sub(time.Unix(0, int64(pos.EntryTS*1e9))) > e.Cfg.HaltPositionMax && pos.EntryTS > 0 {
			e.halt(ctx, "position_over_6m")
			return e.flattenWindow(ctx, pos, "halt_age")
		}
		exitAt := time.Unix(pos.WindowTS, 0).UTC().Add(300*time.Second - e.Cfg.ExitEarly)
		if now.After(exitAt) || now.Equal(exitAt) {
			return e.flattenWindow(ctx, pos, "")
		}
		if pos.StopPx > 0 {
			pollAt := e.now().Add(200 * time.Millisecond)
			if pollAt.After(exitAt) {
				pollAt = exitAt
			}
			return clock.SleepUntil(ctx, pollAt, e.now)
		}
		return clock.SleepUntil(ctx, exitAt, e.now)
	}

	// Restart mid-window: never enter the boot window.
	if ts == e.bootWindow && now.Unix() > ts {
		return e.waitNextOpen(ctx, ts+clock.WindowSec)
	}

	if now.Unix()-ts >= int64(e.Cfg.EntryMaxLag.Seconds()) {
		return e.waitNextOpen(ctx, ts+clock.WindowSec)
	}

	// Prewarm current if we arrived early (clock just crossed into last 15s of previous).
	if now.Unix() < ts {
		return e.waitNextOpen(ctx, ts)
	}

	return e.decide(ctx, ts)
}

func (e *Engine) waitNextOpen(ctx context.Context, next int64) error {
	prewarm := time.Unix(next, 0).UTC().Add(-time.Duration(e.Cfg.PrewarmSeconds * float64(time.Second)))
	if e.now().Before(prewarm) {
		e.setState(StateIdle)
		if err := clock.SleepUntil(ctx, prewarm, e.now); err != nil {
			return err
		}
	}
	if err := e.arm(ctx, next); err != nil {
		e.Log.Warn("arm", "err", err, "ts", next)
	}
	open := time.Unix(next, 0).UTC()
	if err := clock.SleepUntil(ctx, open, e.now); err != nil {
		return err
	}
	return e.decide(ctx, next)
}

func (e *Engine) arm(ctx context.Context, ts int64) error {
	e.setState(StateArming)
	if err := e.syncClock(ctx); err != nil {
		e.Log.Warn("clock", "err", err)
	}
	if absDur(e.offset) > e.Cfg.ClockAlertOffset {
		e.alert(ctx, "warn", "clock", fmt.Sprintf("offset=%s", e.offset))
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
	e.Log.Info("armed", "ts", ts, "slug", slug)
	return nil
}

func (e *Engine) syncClock(ctx context.Context) error {
	off, err := clock.MeasureBinanceOffset(ctx, nil, e.Cfg.BinanceURL)
	if err != nil {
		return err
	}
	e.mu.Lock()
	e.offset = off
	e.mu.Unlock()
	return nil
}

func (e *Engine) decide(ctx context.Context, ts int64) error {
	e.setState(StateDecide)
	exists, err := e.Store.HasWindow(ctx, ts)
	if err != nil {
		return err
	}
	if exists {
		e.Log.Info("window already recorded", "ts", ts)
		return e.sleepToExit(ctx, ts)
	}

	w := store.Window{WindowTS: ts, Side: strategy.SideSkip, State: "decide"}
	if err := e.Store.InsertWindow(ctx, w); err != nil {
		e.Log.Warn("unique window", "err", err)
		return e.sleepToExit(ctx, ts)
	}

	if absDur(e.offset) > e.Cfg.ClockSkipOffset {
		return e.finishSkip(ctx, w, "clock", "clock offset > 1s")
	}
	if e.isHalted() {
		return e.finishSkip(ctx, w, "halt", "halted")
	}

	now := e.now()
	if now.Unix()-ts >= int64(e.Cfg.EntryMaxLag.Seconds()) {
		return e.finishSkip(ctx, w, "late", "past T+2s")
	}

	e.mu.Lock()
	mkt := e.armedMarket
	armed := e.armedFor == ts
	e.mu.Unlock()
	if !armed {
		if err := e.arm(ctx, ts); err != nil {
			return e.finishSkip(ctx, w, "no_market", err.Error())
		}
		e.mu.Lock()
		mkt = e.armedMarket
		e.mu.Unlock()
	}
	if mkt.TokenUp == "" {
		return e.finishSkip(ctx, w, "no_market", "empty tokens")
	}

	q, err := e.PM.SnapshotUp(ctx, mkt, e.now(), time.Second)
	if err != nil {
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
	w.DecisionTS = e.unix()
	w.QuoteTS = float64(q.Recv.UnixNano()) / 1e9
	w.PUp, w.PDown, w.Spread = pUp, pDown, q.Spread()
	w.QuoteSource = q.Source
	w.PMFav = sql.NullFloat64{Float64: math.Max(pUp, pDown), Valid: true}
	w.EntryLagMS = sql.NullFloat64{Float64: (w.DecisionTS - float64(ts)) * 1000, Valid: true}
	e.mu.Lock()
	e.lastQuote = q
	e.mu.Unlock()

	if both {
		e.alert(ctx, "error", "quote", "обе стороны ≥ 0.65 — skip")
		return e.finishSkip(ctx, w, "both_ge_th", "both >= threshold")
	}
	if side == strategy.SideSkip {
		return e.finishSkip(ctx, w, skipReason, skipReason)
	}

	if e.now().Unix()-ts >= int64(e.Cfg.EntryMaxLag.Seconds()) {
		return e.finishSkip(ctx, w, "late", "late after quote")
	}

	if err := e.mustBeFlat(ctx); err != nil {
		return e.finishSkip(ctx, w, "flatten_busy", err.Error())
	}

	eq, err := e.equity(ctx)
	if err != nil {
		return e.finishSkip(ctx, w, "equity", err.Error())
	}
	px, err := e.btcPx(ctx)
	if err != nil || px <= 0 {
		return e.finishSkip(ctx, w, "no_px", fmt.Sprintf("%v", err))
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
		_ = e.Store.InsertOrder(ctx, ts, "entry", "dry_run", clientIdx, "", qty, px, map[string]any{"side": side})
		e.mu.Lock()
		e.pos = &livePos{WindowTS: ts, Side: side, Qty: qty, Notional: qty * px, EntryPx: px, EntryTS: e.unix(), EquityBefore: eq, ClientIdx: clientIdx}
		e.mu.Unlock()
		w.LighterEntryTS = sql.NullFloat64{Float64: e.unix(), Valid: true}
		w.LighterEntryPx = sql.NullFloat64{Float64: px, Valid: true}
		w.FillLagMS = sql.NullFloat64{Float64: (e.unix() - float64(ts)) * 1000, Valid: true}
		w.State = StateInPosition
		_ = e.Store.UpdateWindow(ctx, w)
		e.setState(StateInPosition)
		e.flogWindow("entry", w)
		e.alert(ctx, "info", "entry", fmt.Sprintf("DRY %s qty=%.6f px=%.2f p_up=%.3f", side, qty, px, pUp))
		return nil
	}

	res, err := e.Signer.MarketIOC(e.Cfg.Symbol, buy, qty, px, e.Cfg.MarketSlippage, clientIdx, false)
	sentTS := e.unix()
	_ = e.Store.InsertOrder(ctx, ts, "entry", errString(err, "sent"), clientIdx, hashOf(res), qty, px, nil)
	if err != nil {
		return e.finishSkip(ctx, w, "order_failed", err.Error())
	}
	if e.now().Unix()-ts >= int64(e.Cfg.EntryMaxLag.Seconds()) {
		// sent but past deadline — still honor fill if any; do not chase more
	}

	fillPx, fillQty, fee := e.HTTP.WaitFillFor(ctx, e.Cfg.AccountIndex, meta.MarketID, clientIdx, px, 300*time.Millisecond)
	if fillPx <= 0 {
		fillPx = px
	}
	if fillQty <= 0 {
		// unfilled after send
		w.SkipReason = "entry_unfilled_after_send"
		w.Side = strategy.SideSkip
		e.bumpLateStreak(ctx, true)
		e.alert(ctx, "error", "entry", "ордер отправлен, филла нет")
		_ = e.Store.UpdateWindow(ctx, w)
		e.flogWindow("skip", w)
		if e.global.LateStreak >= e.Cfg.HaltExitLateStreak {
			e.halt(ctx, "entry_unfilled_streak")
		}
		return e.sleepToExit(ctx, ts)
	}
	e.bumpLateStreak(ctx, false)
	usedQty := fillQty
	if usedQty > qty {
		usedQty = qty
	}
	fillTS := e.unix()
	stopPx := strategy.StopPx(fillPx, side, e.Cfg.StopPricePct)
	stopIdx := int64(0)
	if stopPx > 0 {
		stopIdx = e.nextClientIdx(ctx)
		stopResult, stopErr := e.Signer.StopLoss(e.Cfg.Symbol, side == strategy.SideLong, usedQty, stopPx, e.Cfg.MarketSlippage, stopIdx)
		status := errString(stopErr, "sent")
		_ = e.Store.InsertOrder(ctx, ts, "stop_loss", status, stopIdx, hashOf(stopResult), usedQty, stopPx, map[string]any{"trigger_price": stopPx, "reduce_only": true})
		if stopErr != nil {
			e.alert(ctx, "error", "stop_missing", fmt.Sprintf("не удалось поставить стоп %.2f после fill %.2f: %v; закрываю позицию", stopPx, fillPx, stopErr))
			closeErr := e.ensureFlat(ctx, "stop_missing")
			if cancelErr := e.Signer.CancelAll(); cancelErr != nil {
				e.Log.Warn("cancel orders after stop failure", "err", cancelErr)
			}
			e.halt(ctx, "stop_missing")
			if closeErr != nil {
				return fmt.Errorf("place stop: %w; emergency flatten: %v", stopErr, closeErr)
			}
			return e.finishSkip(ctx, w, "stop_missing", stopErr.Error())
		}
	}
	e.mu.Lock()
	e.pos = &livePos{
		WindowTS: ts, Side: side, Qty: usedQty, Notional: usedQty * fillPx,
		EntryPx: fillPx, EntryTS: fillTS, EquityBefore: eq, ClientIdx: clientIdx, Fees: fee,
		StopClientIdx: stopIdx, StopPx: stopPx,
		FillLagMS: (e.unix() - float64(ts)) * 1000,
	}
	e.mu.Unlock()
	w.LighterEntryTS = sql.NullFloat64{Float64: fillTS, Valid: true}
	w.LighterEntryPx = sql.NullFloat64{Float64: fillPx, Valid: true}
	w.Qty = sql.NullFloat64{Float64: usedQty, Valid: true}
	w.Notional = sql.NullFloat64{Float64: usedQty * fillPx, Valid: true}
	w.FeesUSD = sql.NullFloat64{Float64: fee, Valid: true}
	w.FillLagMS = sql.NullFloat64{Float64: (e.unix() - float64(ts)) * 1000, Valid: true}
	w.State = StateInPosition
	_ = e.Store.UpdateWindow(ctx, w)
	e.setState(StateInPosition)
	e.flogWindow("entry", w)
	e.alert(ctx, "info", "entry", fmt.Sprintf("%s qty=%.6f px=%.2f p_up=%.3f lag=%.0fms", side, usedQty, fillPx, pUp, w.FillLagMS.Float64))
	return nil
}

func (e *Engine) finishSkip(ctx context.Context, w store.Window, reason, msg string) error {
	w.Side = strategy.SideSkip
	w.SkipReason = reason
	w.State = "skip"
	if w.DecisionTS == 0 {
		w.DecisionTS = e.unix()
	}
	_ = e.Store.UpdateWindow(ctx, w)
	e.mu.Lock()
	e.lastSkip = reason
	e.lastWindow = w.WindowTS
	e.mu.Unlock()
	e.flogWindow("skip", w)
	e.Log.Info("skip", "ts", w.WindowTS, "reason", reason, "msg", msg)
	_ = e.ensureFlat(ctx, "skip")
	return e.sleepToExit(ctx, w.WindowTS)
}

func (e *Engine) sleepToExit(ctx context.Context, ts int64) error {
	exitAt := time.Unix(ts, 0).UTC().Add(300*time.Second - e.Cfg.ExitEarly)
	e.setState(StateIdle)
	if e.now().After(exitAt) {
		_ = e.fillShadow(ctx, ts)
		return nil
	}
	if err := clock.SleepUntil(ctx, exitAt, e.now); err != nil {
		return err
	}
	_ = e.ensureFlat(ctx, "boundary")
	_ = e.fillShadow(ctx, ts)
	return nil
}

func (e *Engine) flattenWindow(ctx context.Context, pos *livePos, reason string) error {
	e.setState(StateFlattening)
	late := false
	deadline := time.Unix(pos.WindowTS, 0).UTC().Add(300 * time.Second)
	px, fee := 0.0, 0.0
	if reason == "stop" {
		px, qty, stopFee := e.HTTP.WaitFillFor(ctx, e.Cfg.AccountIndex, e.Markets[e.Cfg.Symbol].MarketID, pos.StopClientIdx, 0, 600*time.Millisecond)
		fee = stopFee
		if px > 0 && qty > 0 {
			_ = e.Store.InsertOrder(ctx, pos.WindowTS, "stop_loss_fill", "filled", pos.StopClientIdx, "", qty, px, map[string]any{"stop_px": pos.StopPx, "sl_slip_bps": strategy.SLSlipBps(pos.EntryPx, px, pos.Side, e.Cfg.StopPricePct)})
		}
		if px <= 0 {
			px, _ = e.btcPx(ctx)
		}
	} else {
		if pos.StopClientIdx > 0 && e.Signer != nil {
			if err := e.Signer.CancelAll(); err != nil {
				e.Log.Warn("cancel stop before window close", "err", err)
			}
		}
		var err error
		px, fee, err = e.marketClose(ctx, pos)
		if err != nil {
			e.alert(ctx, "error", "flatten", err.Error())
		}
	}
	retryUntil := deadline.Add(2 * time.Second)
	if reason == "stop" {
		retryUntil = e.now().Add(2 * time.Second)
	}
	for e.now().Before(retryUntil) {
		flat, _ := e.isFlat(ctx)
		if flat {
			break
		}
		late = e.now().After(deadline)
		_, _, _ = e.marketClose(ctx, pos)
		time.Sleep(200 * time.Millisecond)
	}
	if flat, _ := e.isFlat(ctx); !flat {
		late = true
		e.alert(ctx, "error", "exit_late", "не удалось закрыть к границе")
	}
	if px <= 0 {
		px, _ = e.btcPx(ctx)
	}
	eq, _ := e.equity(ctx)
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
	_ = e.fillShadow(ctx, pos.WindowTS)
	return nil
}

func (e *Engine) marketClose(ctx context.Context, pos *livePos) (px, fee float64, err error) {
	if e.Cfg.DryRun || e.Signer == nil {
		px, err = e.btcPx(ctx)
		return px, 0, err
	}
	ref, err := e.btcPx(ctx)
	if err != nil {
		return 0, 0, err
	}
	buy := pos.Side == strategy.SideShort // closing short = buy
	idx := e.nextClientIdx(ctx)
	res, err := e.Signer.MarketIOC(e.Cfg.Symbol, buy, pos.Qty, ref, e.Cfg.MarketSlippage, idx, true)
	_ = e.Store.InsertOrder(ctx, pos.WindowTS, "exit", errString(err, "sent"), idx, hashOf(res), pos.Qty, ref, nil)
	if err != nil {
		return 0, 0, err
	}
	meta := e.Markets[e.Cfg.Symbol]
	fillPx, _, fillFee := e.HTTP.WaitFillFor(ctx, e.Cfg.AccountIndex, meta.MarketID, idx, ref, 800*time.Millisecond)
	if fillPx <= 0 {
		fillPx = ref
	}
	return fillPx, fillFee, nil
}

func (e *Engine) ensureFlat(ctx context.Context, why string) error {
	if e.Cfg.DryRun || e.Signer == nil {
		e.mu.Lock()
		e.pos = nil
		e.mu.Unlock()
		return nil
	}
	flat, pos := e.isFlat(ctx)
	if flat {
		// Clear stale conditional orders once on boot, including the case where
		// the previous process exited after the position was already closed.
		if why == "boot" || why == "kill" {
			if err := e.Signer.CancelAll(); err != nil {
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
		_, _, err := e.marketClose(ctx, live)
		if err != nil {
			e.Log.Warn("flatten retry", "err", err)
		}
		time.Sleep(200 * time.Millisecond)
		if ok, _ := e.isFlat(ctx); ok {
			if err := e.Signer.CancelAll(); err != nil {
				e.Log.Warn("cancel residual orders after flatten", "why", why, "err", err)
			}
			return nil
		}
	}
	return fmt.Errorf("still not flat after flatten (%s)", why)
}

func (e *Engine) mustBeFlat(ctx context.Context) error {
	if e.Cfg.DryRun {
		return nil
	}
	ok, _ := e.isFlat(ctx)
	if !ok {
		return fmt.Errorf("position not flat")
	}
	return nil
}

func (e *Engine) isFlat(ctx context.Context) (bool, lighter.Position) {
	if e.HTTP == nil {
		return true, lighter.Position{}
	}
	acc, err := e.HTTP.Account(ctx, e.Cfg.AccountIndex)
	if err != nil {
		return false, lighter.Position{}
	}
	meta := e.Markets[e.Cfg.Symbol]
	p, ok := acc.Position(e.Cfg.Symbol, meta.MarketID)
	if !ok || p.Size == 0 {
		return true, lighter.Position{}
	}
	return false, p
}

func (e *Engine) equity(ctx context.Context) (float64, error) {
	if e.HTTP == nil {
		if e.global.StartEquity > 0 {
			return e.global.StartEquity, nil
		}
		return 1000, nil
	}
	acc, err := e.HTTP.Account(ctx, e.Cfg.AccountIndex)
	if err != nil {
		return 0, err
	}
	return acc.Equity(), nil
}

func (e *Engine) btcPx(ctx context.Context) (float64, error) {
	if e.HTTP == nil {
		return e.BN.Close1s(ctx, "BTCUSDT", e.now().Unix())
	}
	return e.HTTP.LastPrice(ctx, e.Cfg.Symbol)
}

func (e *Engine) fillShadow(ctx context.Context, ts int64) error {
	if e.BN == nil || !e.Cfg.ShadowBinance1s {
		return nil
	}
	w, err := e.Store.GetWindow(ctx, ts)
	if err != nil {
		return err
	}
	px1, err1 := e.BN.Close1s(ctx, "BTCUSDT", ts+1)
	pxC, err2 := e.BN.Close5m(ctx, "BTCUSDT", ts)
	if err1 == nil && px1 > 0 {
		w.ShadowBinancePxT1 = sql.NullFloat64{Float64: px1, Valid: true}
	}
	side := w.Side
	if side == strategy.SideLong || side == strategy.SideShort {
		if err1 == nil && err2 == nil && px1 > 0 && pxC > 0 {
			ret := strategy.Sign(side) * (pxC/px1 - 1)
			w.ShadowRetBps = sql.NullFloat64{Float64: ret * 1e4, Valid: true}
			e.global.ShadowEquity *= (1 + ret*e.Cfg.Leverage*e.Cfg.EquityFraction)
			_ = e.Store.InsertEquity(ctx, ts+300, "shadow", e.global.ShadowEquity)
			_ = e.Store.SaveGlobal(ctx, e.global)
		}
	}
	return e.Store.UpdateWindow(ctx, w)
}

func (e *Engine) nextClientIdx(ctx context.Context) int64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.global.ClientOrderSeq == 0 {
		e.global.ClientOrderSeq = time.Now().UnixMilli() % 1_000_000_000
	}
	e.global.ClientOrderSeq++
	_ = e.Store.SaveGlobal(ctx, e.global)
	return e.global.ClientOrderSeq
}

func (e *Engine) bumpLateStreak(ctx context.Context, bad bool) {
	e.mu.Lock()
	if bad {
		e.global.LateStreak++
	} else {
		e.global.LateStreak = 0
	}
	g := e.global
	e.mu.Unlock()
	_ = e.Store.SaveGlobal(ctx, g)
}

func (e *Engine) applyPeak(ctx context.Context, eq float64) {
	e.mu.Lock()
	if eq > e.global.PeakEquity {
		e.global.PeakEquity = eq
	}
	peak := e.global.PeakEquity
	ddHalt := e.Cfg.HaltDDpct
	e.mu.Unlock()
	if peak > 0 && eq > 0 && (peak-eq)/peak > ddHalt {
		e.halt(ctx, "dd")
		e.alert(ctx, "error", "halt", fmt.Sprintf("equity drop %.2f%% from peak %.2f", 100*(peak-eq)/peak, peak))
	}
	_ = e.Store.SaveGlobal(ctx, e.global)
}

func (e *Engine) halt(ctx context.Context, reason string) {
	e.mu.Lock()
	e.global.Halted = true
	e.global.HaltReason = reason
	e.global.KillSwitch = true
	e.mu.Unlock()
	e.setState(StateHalt)
	_ = e.Store.SaveGlobal(ctx, e.global)
	e.alert(ctx, "error", "halt", reason)
}

func (e *Engine) isHalted() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.global.Halted || e.global.KillSwitch
}

func (e *Engine) KillAll(ctx context.Context) error {
	e.mu.Lock()
	e.global.KillSwitch = true
	e.mu.Unlock()
	_ = e.Store.SaveGlobal(ctx, e.global)
	e.setState(StateHalt)
	e.alert(ctx, "warn", "kill", "kill-switch: flatten, входы запрещены")
	return e.ensureFlat(ctx, "kill")
}

func (e *Engine) Resume(ctx context.Context) {
	e.mu.Lock()
	e.global.KillSwitch = false
	e.global.Halted = false
	e.global.HaltReason = ""
	e.global.LateStreak = 0
	e.mu.Unlock()
	_ = e.Store.SaveGlobal(ctx, e.global)
	e.setState(StateIdle)
	e.alert(ctx, "info", "kill", "kill-switch снят")
}

func (e *Engine) Health() (bool, string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.global.Halted {
		return false, "HALT " + e.global.HaltReason
	}
	if e.global.KillSwitch {
		return false, "kill-switch"
	}
	if absDur(e.offset) > e.Cfg.ClockSkipOffset {
		return false, "clock offset"
	}
	return true, e.state
}

func (e *Engine) WSState() (bool, string) {
	if e.PM == nil {
		return false, "no pm"
	}
	return e.PM.WSState()
}

func (e *Engine) setState(s string) {
	e.mu.Lock()
	e.state = s
	e.mu.Unlock()
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
		"side": w.Side, "skip_reason": w.SkipReason,
	})
}

func absDur(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
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
