package engine

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"math"
	"strings"
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
	ArmedAt       time.Time
	EntryHash     string
	SeenAcctOpen  bool
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
	clockSyncMu sync.Mutex
	state       string
	offset      time.Duration
	offsetOK    bool
	offsetAt    time.Time
	clockRTT    time.Duration
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
	consumedTS  int64
	readyEq     float64
	readyPx     float64
	readyAt     time.Time
	readyOK     bool
	tr          *winTrace
	lastTimings *store.Timings
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
	// The VPS system clock is disciplined by chrony/NTP. Binance time is
	// telemetry only and must not become a runtime dependency for trading.
	return time.Now().UTC()
}

func (e *Engine) entryClock() time.Time {
	return e.now()
}

func (e *Engine) entryDeadline(ts int64) time.Time {
	return time.Unix(ts, 0).UTC().Add(e.Cfg.EntryMaxLag)
}

func (e *Engine) entryElapsed(ts int64) (mid, lower, upper time.Duration) {
	mid = e.now().Sub(time.Unix(ts, 0).UTC())
	return mid, mid, mid
}

func (e *Engine) correctedTime(local time.Time) time.Time {
	return local.UTC()
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
	if e.Signer != nil && !e.Cfg.DryRun {
		if err := e.Signer.UpdateLeverage(ctx, e.Cfg.Symbol, e.Cfg.Leverage); err != nil {
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
	go func() {
		if err := e.syncClock(context.Background(), 3); err != nil {
			e.Log.Debug("binance clock telemetry unavailable", "err", err)
		}
	}()
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
		if !e.Cfg.DryRun && e.Signer != nil {
			if reason, px, fee, ok := e.detectExit(ctx, pos); ok {
				return e.settleWindow(ctx, pos, reason, px, fee)
			}
		}
		if now.Sub(time.Unix(0, int64(pos.EntryTS*1e9))) > e.Cfg.HaltPositionMax && pos.EntryTS > 0 {
			e.halt(ctx, "position_over_6m")
			return e.settleWindow(ctx, pos, "halt_age", 0, 0)
		}
		exitAt := time.Unix(pos.WindowTS, 0).UTC().Add(300*time.Second - e.Cfg.ExitEarly)
		if now.After(exitAt) || now.Equal(exitAt) {
			return e.settleWindow(ctx, pos, "", 0, 0)
		}
		pollAt := e.now().Add(time.Second)
		if pollAt.After(exitAt) {
			pollAt = exitAt
		}
		return clock.SleepUntil(ctx, pollAt, e.now)
	}

	// Restart mid-window: never enter the boot window.
	if ts == e.bootWindow && now.Unix() > ts {
		return e.waitNextOpen(ctx, ts+clock.WindowSec)
	}

	// Prewarm current if we arrived early (clock just crossed into last 15s of previous).
	if now.Unix() < ts {
		return e.waitNextOpen(ctx, ts)
	}

	return e.decide(ctx, ts)
}

func (e *Engine) waitNextOpen(ctx context.Context, next int64) error {
	e.beginTrace(next)
	serverOpen := time.Unix(next, 0).UTC()
	initialPrewarm := serverOpen.Add(-time.Duration(e.Cfg.PrewarmSeconds * float64(time.Second)))
	if e.now().Before(initialPrewarm) {
		e.setState(StateIdle)
		if err := clock.SleepUntil(ctx, initialPrewarm, e.now); err != nil {
			return err
		}
	}
	if e.now().Before(serverOpen.Add(-3 * time.Second)) {
		// Binance remains a best-effort reference for the dashboard. Never wait
		// on it while scheduling or preparing a live entry.
		go func() {
			cctx, cancel := context.WithTimeout(ctx, 4*time.Second)
			defer cancel()
			if err := e.syncClock(cctx, 3); err != nil {
				e.Log.Debug("binance clock telemetry unavailable", "err", err)
			}
		}()
	}
	// Chrony/NTP disciplines the host's system UTC clock. Binance API time is
	// not used to move the boundary or determine the entry deadline.
	scheduleOpen := serverOpen
	prewarm := scheduleOpen.Add(-time.Duration(e.Cfg.PrewarmSeconds * float64(time.Second)))
	if e.now().Before(prewarm) {
		if err := clock.SleepUntil(ctx, prewarm, e.now); err != nil {
			return err
		}
	}
	tArm := time.Now()
	if err := e.arm(ctx, next); err != nil {
		e.Log.Warn("arm", "err", err, "ts", next)
	}
	e.traceDur("arm", tArm)
	tReady := time.Now()
	_ = e.refreshReady(ctx)
	e.traceDur("ready", tReady)
	// Stop doing synchronous account/price work well before :00. A slow REST
	// refresh must never consume the entry budget at the boundary.
	for e.now().Before(scheduleOpen.Add(-1200 * time.Millisecond)) {
		rctx, cancel := context.WithDeadline(ctx, scheduleOpen.Add(-800*time.Millisecond))
		_ = e.refreshReady(rctx)
		cancel()
		wake := e.now().Add(time.Second)
		if wake.After(scheduleOpen.Add(-1200 * time.Millisecond)) {
			wake = scheduleOpen.Add(-1200 * time.Millisecond)
		}
		if err := clock.SleepUntil(ctx, wake, e.now); err != nil {
			return err
		}
	}
	if err := clock.SleepUntil(ctx, scheduleOpen, e.now); err != nil {
		return err
	}
	return e.decide(ctx, next)
}

func (e *Engine) refreshReady(ctx context.Context) error {
	e.mu.Lock()
	e.readyOK = false
	e.mu.Unlock()
	var eq float64
	var flat bool
	if e.HTTP != nil {
		acc, err := e.HTTP.Account(ctx, e.Cfg.AccountIndex)
		if err != nil {
			return err
		}
		eq = acc.Equity()
		_, open := acc.Position(e.Cfg.Symbol, e.Markets[e.Cfg.Symbol].MarketID)
		flat = !open
	} else {
		var err error
		eq, err = e.equity(ctx)
		if err != nil {
			return err
		}
		flat = true
	}
	px, err := e.btcPx(ctx)
	if err != nil {
		return err
	}
	if px <= 0 || eq <= 0 {
		return fmt.Errorf("invalid ready values: equity=%v price=%v", eq, px)
	}
	e.mu.Lock()
	e.readyEq, e.readyPx, e.readyAt = eq, px, time.Now()
	e.readyOK = flat || e.Cfg.DryRun
	e.mu.Unlock()
	if !flat && !e.Cfg.DryRun {
		go func() {
			fctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := e.ensureFlat(fctx, "prewarm"); err != nil {
				e.Log.Error("prewarm flatten", "err", err)
			}
		}()
		return fmt.Errorf("unexpected position flattened during prewarm; entry disabled for this window")
	}
	return nil
}

func (e *Engine) arm(ctx context.Context, ts int64) error {
	e.setState(StateArming)
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

func (e *Engine) saveWindow(ctx context.Context, w store.Window) {
	e.applyTrace(&w)
	tSQL := time.Now()
	if err := e.Store.InsertWindow(ctx, w); err != nil {
		_ = e.Store.UpdateWindow(ctx, w)
	}
	e.traceDur("sqlite", tSQL)
}

func (e *Engine) decide(ctx context.Context, ts int64) error {
	e.beginTrace(ts)
	e.stampLateStart()
	e.setState(StateDecide)
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
		e.alert(ctx, "error", "quote", "обе стороны ≥ 0.65 — skip")
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
		_ = e.Store.InsertOrder(ctx, ts, "entry", "dry_run", clientIdx, "", qty, px, map[string]any{"side": side})
		e.mu.Lock()
		e.pos = &livePos{WindowTS: ts, Side: side, Qty: qty, Notional: qty * px, EntryPx: px, EntryTS: e.unix(), EquityBefore: eq, ClientIdx: clientIdx}
		e.mu.Unlock()
		w.LighterEntryTS = sql.NullFloat64{Float64: e.unix(), Valid: true}
		w.LighterEntryPx = sql.NullFloat64{Float64: px, Valid: true}
		w.State = StateInPosition
		e.saveWindow(ctx, w)
		e.setState(StateInPosition)
		e.flogWindow("entry", w)
		e.alert(ctx, "info", "entry", fmt.Sprintf("DRY %s qty=%.6f px=%.2f p_up=%.3f", side, qty, px, pUp))
		return nil
	}

	if _, _, latest := e.entryElapsed(ts); latest >= e.Cfg.EntryMaxLag {
		return e.finishSkip(ctx, w, "late", "deadline expired before order request")
	}
	sendCtx, sendCancel := context.WithDeadline(ctx, deadline)
	tSend := time.Now()
	e.traceOpenTo("send", e.correctedTime(tSend).UTC())
	res, err := e.Signer.MarketIOC(sendCtx, e.Cfg.Symbol, buy, qty, px, e.Cfg.MarketSlippage, clientIdx, false)
	sendCancel()
	e.traceDur("send", tSend)
	e.traceOpenTo("send_ack", e.correctedTime(time.Now()).UTC())
	w.State = "sent"
	e.saveWindow(ctx, w)
	_ = e.Store.InsertOrder(ctx, ts, "entry", errString(err, "sent"), clientIdx, hashOf(res), qty, px, nil)
	if err != nil {
		return e.finishSkip(ctx, w, "order_failed", err.Error())
	}

	tFill := time.Now()
	fillPx, fillQty, fee, fillUnix, oversize := e.confirmLiveFill(ctx, meta, clientIdx, hashOf(res), qty, px)
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
	if stopPx > 0 {
		stopIdx = e.nextClientIdx(ctx)
		tStop := time.Now()
		sctx, scancel := context.WithTimeout(ctx, 1500*time.Millisecond)
		stopResult, stopErr := e.Signer.StopLoss(sctx, e.Cfg.Symbol, side == strategy.SideLong, usedQty, stopPx, e.Cfg.MarketSlippage, stopIdx)
		scancel()
		e.traceDur("stop", tStop)
		status := errString(stopErr, "sent")
		_ = e.Store.InsertOrder(ctx, ts, "stop_loss", status, stopIdx, hashOf(stopResult), usedQty, stopPx, map[string]any{"trigger_price": stopPx, "reduce_only": true})
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
	if fillLag.Valid && e.tr != nil {
		e.tr.t.MsOpenToExchange = fillLag.Float64
	}
	e.mu.Lock()
	e.pos = &livePos{
		WindowTS: ts, Side: side, Qty: usedQty, Notional: usedQty * fillPx,
		EntryPx: fillPx, EntryTS: fillTS, EquityBefore: eq, ClientIdx: clientIdx, Fees: fee,
		StopClientIdx: stopIdx, StopPx: stopPx,
		FillLagMS: fillLag.Float64, ArmedAt: time.Now(), EntryHash: hashOf(res),
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

func (e *Engine) sleepToExit(ctx context.Context, ts int64) error {
	e.setState(StateIdle)
	e.shadowAsync(ts)
	return e.waitNextOpen(ctx, ts+clock.WindowSec)
}

func (e *Engine) shadowAsync(ts int64) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		_ = e.fillShadow(ctx, ts)
	}()
}

func (e *Engine) settleWindow(ctx context.Context, pos *livePos, reason string, px, fee float64) error {
	e.setState(StateFlattening)
	late := false
	deadline := time.Unix(pos.WindowTS, 0).UTC().Add(300 * time.Second)
	alreadyFlat := false
	if flat, _, err := e.isFlat(ctx); err == nil && flat {
		alreadyFlat = true
	}
	if px <= 0 {
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
	if alreadyFlat {
		if e.Signer != nil {
			if err := e.Signer.CancelAll(ctx); err != nil {
				e.Log.Warn("cancel residual after remote close", "err", err)
			}
		}
	} else if reason == "stop" || reason == "closed_away" {
		e.Log.Warn("account still open after detected close, flattening", "reason", reason)
		cpx, cfee, err := e.marketClose(ctx, pos)
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
		cpx, cfee, err := e.marketClose(ctx, pos)
		if err != nil {
			e.alert(ctx, "error", "flatten", err.Error())
		}
		if cpx > 0 {
			px, fee = cpx, cfee
		}
	}
	retryUntil := deadline.Add(2 * time.Second)
	if reason == "stop" || reason == "closed_away" {
		retryUntil = e.now().Add(2 * time.Second)
	}
	for !alreadyFlat && e.now().Before(retryUntil) {
		flat, _, err := e.isFlat(ctx)
		if err == nil && flat {
			break
		}
		late = e.now().After(deadline)
		cpx, cfee, _ := e.marketClose(ctx, pos)
		if cpx > 0 {
			px = cpx
			fee += cfee
		}
		time.Sleep(200 * time.Millisecond)
	}
	if flat, _, err := e.isFlat(ctx); err != nil || !flat {
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
	e.shadowAsync(pos.WindowTS)
	return e.waitNextOpen(ctx, pos.WindowTS+clock.WindowSec)
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
	res, err := e.Signer.MarketIOC(ctx, e.Cfg.Symbol, buy, pos.Qty, ref, e.Cfg.MarketSlippage, idx, true)
	_ = e.Store.InsertOrder(ctx, pos.WindowTS, "exit", errString(err, "sent"), idx, hashOf(res), pos.Qty, ref, nil)
	if err != nil {
		return 0, 0, err
	}
	meta := e.Markets[e.Cfg.Symbol]
	fillPx, _, fillFee, _ := e.HTTP.WaitFillFor(ctx, e.Cfg.AccountIndex, meta.MarketID, idx, hashOf(res), ref, 1500*time.Millisecond)
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
	if why == "prewarm" || why == "skip" {
		e.mu.Lock()
		live := e.pos
		e.mu.Unlock()
		if live != nil {
			return nil
		}
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
		_, _, err := e.marketClose(ctx, live)
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
	p, q, f, sl := e.closingTradeVWAP(ctx, pos)
	if q > 0 {
		px, fee = p, f
	} else {
		px, _ = e.btcPx(ctx)
	}
	if sl || (pos.StopPx > 0 && strategy.StopHit(px, pos.Side, pos.StopPx)) {
		return "stop", px, fee, true
	}
	return "closed_away", px, fee, true
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
	if pos.StopClientIdx != 0 {
		if p, q, f, _ := lighter.VWAP(lighter.FillsMatching(fills, pos.StopClientIdx, "")); q > 0 {
			return p, q, f, true
		}
	}
	after := pos.EntryTS
	if after <= 0 && !pos.ArmedAt.IsZero() {
		after = float64(pos.ArmedAt.Unix())
	}
	p, q, f, _ := lighter.VWAP(lighter.FillsClosingPosition(fills, pos.ClientIdx, pos.EntryHash, pos.Qty, after))
	return p, q, f, false
}

const entryFillConfirm = 5 * time.Second

func (e *Engine) confirmLiveFill(ctx context.Context, meta lighter.MarketMeta, clientIdx int64, txHash string, wantQty, fallbackPx float64) (px, qty, fee, unix float64, oversized error) {
	px = fallbackPx
	deadline := time.Now().Add(entryFillConfirm)
	lot := math.Pow10(-meta.SizeDecimals)
	for {
		fills, err := e.HTTP.AccountTradesUrgent(ctx, e.Cfg.AccountIndex, meta.MarketID, 100)
		if err == nil {
			if p, q, f, u := lighter.VWAP(lighter.FillsMatching(fills, clientIdx, txHash)); q > 0 {
				px, qty, fee, unix = p, q, f, u
			}
		}
		acc, aerr := e.HTTP.AccountUrgent(ctx, e.Cfg.AccountIndex)
		if aerr == nil {
			if p, open := acc.Position(e.Cfg.Symbol, meta.MarketID); open && p.Size > 0 {
				if p.Size > wantQty+lot {
					return px, p.Size, fee, unix, fmt.Errorf("position %.8f exceeded requested %.8f", p.Size, wantQty)
				}
				if qty <= 0 || p.Size > qty {
					qty = p.Size
				}
				if p.AvgEntry > 0 {
					px = p.AvgEntry
				}
			}
		}
		if qty > 0 && px > 0 {
			return px, qty, fee, unix, nil
		}
		if !time.Now().Before(deadline) {
			e.Log.Warn("fill not confirmed", "client_idx", clientIdx, "hash", txHash, "want_qty", wantQty, "trades_err", errString(err, ""), "acct_err", errString(aerr, ""))
			return px, qty, fee, unix, nil
		}
		select {
		case <-ctx.Done():
			return px, qty, fee, unix, nil
		case <-time.After(80 * time.Millisecond):
		}
	}
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
		if err1 == nil && px1 > 0 {
			stopPx := strategy.StopPx(px1, side, e.Cfg.StopPricePct)
			hit, _ := e.BN.StopHit1s(ctx, "BTCUSDT", ts+1, ts+300, side == strategy.SideLong, stopPx)
			var ret float64
			if hit && stopPx > 0 {
				ret = strategy.Sign(side) * (stopPx/px1 - 1)
			} else if err2 == nil && pxC > 0 {
				ret = strategy.Sign(side) * (pxC/px1 - 1)
			} else {
				return e.Store.UpdateWindow(ctx, w)
			}
			w.ShadowRetBps = sql.NullFloat64{Float64: ret * 1e4, Valid: true}
			e.mu.Lock()
			e.global.ShadowEquity *= (1 + ret*e.Cfg.Leverage*e.Cfg.EquityFraction)
			g := e.global
			e.mu.Unlock()
			_ = e.Store.InsertEquity(ctx, ts+300, "shadow", g.ShadowEquity)
			_ = e.Store.SaveGlobal(ctx, g)
		}
	}
	return e.Store.UpdateWindow(ctx, w)
}

func (e *Engine) nextClientIdx(ctx context.Context) int64 {
	e.mu.Lock()
	if e.global.ClientOrderSeq == 0 {
		e.global.ClientOrderSeq = time.Now().UnixMilli() % 1_000_000_000
	}
	e.global.ClientOrderSeq++
	seq := e.global.ClientOrderSeq
	g := e.global
	e.mu.Unlock()
	go func() { _ = e.Store.SaveGlobal(context.Background(), g) }()
	return seq
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
	prev := e.state
	e.state = s
	e.mu.Unlock()
	if prev != s && e.Log != nil {
		e.Log.Info("state", "from", prev, "to", s)
	}
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
