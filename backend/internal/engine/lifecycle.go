package engine

import (
	"context"
	"fmt"
	"time"

	"poly-lighter.trade/bot/internal/clock"
	"poly-lighter.trade/bot/internal/strategy"
)

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
	e.mu.Lock()
	e.global = g
	e.mu.Unlock()
	e.persistGlobal(ctx)
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
		// Local stop watchdog (SLA backup for native SL). Polls flatness first
		// so we do not CancelAll a live exchange stop on DNS flaps.
		if !e.Cfg.DryRun && e.Signer != nil {
			if reason, px, fee, ok := e.resolveStopWatchdog(ctx, pos); ok {
				return e.settleWindow(ctx, pos, reason, px, fee)
			}
		} else if e.Cfg.DryRun && pos.StopPx > 0 {
			if px, err := e.btcPx(ctx); err == nil && strategy.StopHit(px, pos.Side, pos.StopPx) {
				e.Log.Info("dry-run stop hit", "px", px, "stop", pos.StopPx, "side", pos.Side)
				return e.settleWindow(ctx, pos, "stop", px, 0)
			}
		}
		if now.Sub(time.Unix(0, int64(pos.EntryTS*1e9))) > e.Cfg.HaltPositionMax && pos.EntryTS > 0 {
			e.halt(ctx, "position_over_6m")
			return e.settleWindow(ctx, pos, "halt_age", 0, 0)
		}
		exitAt := time.Unix(pos.WindowTS, 0).UTC().Add(time.Duration(clock.WindowSec)*time.Second - e.Cfg.ExitEarly)
		if now.After(exitAt) || now.Equal(exitAt) {
			return e.settleWindow(ctx, pos, "", 0, 0)
		}
		nextTS := pos.WindowTS + clock.WindowSec
		nextOpen := time.Unix(nextTS, 0).UTC()
		prewarmNext := nextOpen.Add(-time.Duration(e.Cfg.PrewarmSeconds * float64(time.Second)))
		if !now.Before(prewarmNext) && now.Before(exitAt.Add(-2*time.Second)) {
			e.mu.Lock()
			armedNext := e.armedFor == nextTS
			e.mu.Unlock()
			if !armedNext {
				if err := e.arm(ctx, nextTS); err != nil {
					e.Log.Warn("arm next", "err", err, "ts", nextTS)
				}
			}
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
	e.mu.Lock()
	alreadyArmed := e.armedFor == next
	e.mu.Unlock()
	if !alreadyArmed {
		tArm := time.Now()
		if err := e.arm(ctx, next); err != nil {
			e.Log.Warn("arm", "err", err, "ts", next)
		}
		e.traceDur("arm", tArm)
	}
	// After a window close we often arrive past :00. Do not spend REST budget
	// here — decide must still fit in T+2s. Ready state was set in settle.
	if e.now().Before(scheduleOpen) {
		tReady := time.Now()
		rctx, cancel := context.WithDeadline(ctx, scheduleOpen.Add(-200*time.Millisecond))
		_ = e.refreshReady(rctx)
		cancel()
		e.traceDur("ready", tReady)
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
	}
	return e.decide(ctx, next)
}

func (e *Engine) refreshReady(ctx context.Context) error {
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

func (e *Engine) markReadyAfterClose(px, eq float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if eq > 0 {
		e.readyEq = eq
	}
	if px > 0 {
		e.readyPx = px
	}
	if e.readyEq > 0 && e.readyPx > 0 {
		e.readyAt = time.Now()
		e.readyOK = true
	}
}

func (e *Engine) sleepToExit(ctx context.Context, ts int64) error {
	e.shadowAsync(ts)
	if e.isHalted() {
		// Stay in HALT and return to the Run/tick halt loop — do not arm/decide
		// the next window (that would flash DECIDE/IDLE while kill-switch is on).
		e.setState(StateHalt)
		return clock.SleepUntil(ctx, e.now().Add(time.Second), e.now)
	}
	e.setState(StateIdle)
	return e.waitNextOpen(ctx, ts+clock.WindowSec)
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
