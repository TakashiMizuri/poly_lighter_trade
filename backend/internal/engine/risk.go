package engine

import (
	"context"
	"fmt"
	"time"

	"poly-lighter.trade/bot/internal/store"
)

func (e *Engine) nextClientIdx(ctx context.Context) int64 {
	var seq int64
	e.mutateGlobal(ctx, func(g *store.GlobalState) {
		if g.ClientOrderSeq == 0 {
			g.ClientOrderSeq = time.Now().UnixMilli() % 1_000_000_000
		}
		g.ClientOrderSeq++
		seq = g.ClientOrderSeq
	})
	return seq
}

// mutateGlobal applies fn under lock, bumps Version, and durable-persists synchronously.
// Engine is the single writer of GlobalState; CAS rejects stale external overwrites.
func (e *Engine) mutateGlobal(ctx context.Context, fn func(*store.GlobalState)) {
	e.mu.Lock()
	fn(&e.global)
	expected := e.global.Version
	e.global.Version = expected + 1
	g := e.global
	e.mu.Unlock()
	if e.Store == nil {
		return
	}
	ok, err := e.Store.SaveGlobalCAS(ctx, g, expected)
	if err != nil {
		e.Log.Error("persist global", "err", err, "version", g.Version)
		return
	}
	if ok {
		return
	}
	// CAS miss: this process still owns the freshest in-memory state — force save.
	if err := e.Store.SaveGlobal(ctx, g); err != nil {
		e.Log.Error("persist global force", "err", err, "version", g.Version)
	}
}

func (e *Engine) persistGlobal(ctx context.Context) {
	e.mutateGlobal(ctx, func(*store.GlobalState) {})
}

func (e *Engine) bumpLateStreak(ctx context.Context, bad bool) {
	e.mutateGlobal(ctx, func(g *store.GlobalState) {
		if bad {
			g.LateStreak++
		} else {
			g.LateStreak = 0
		}
	})
}

func (e *Engine) applyPeak(ctx context.Context, eq float64) {
	var peak float64
	var ddHalt float64
	e.mutateGlobal(ctx, func(g *store.GlobalState) {
		if eq > g.PeakEquity {
			g.PeakEquity = eq
		}
		peak = g.PeakEquity
	})
	ddHalt = e.Cfg.HaltDDpct
	if peak > 0 && eq > 0 && (peak-eq)/peak > ddHalt {
		e.halt(ctx, "dd")
		e.alert(ctx, "error", "halt", fmt.Sprintf("equity drop %.2f%% from peak %.2f", 100*(peak-eq)/peak, peak))
	}
}

func (e *Engine) halt(ctx context.Context, reason string) {
	e.mutateGlobal(ctx, func(g *store.GlobalState) {
		g.Halted = true
		g.HaltReason = reason
		g.KillSwitch = true
	})
	e.setState(StateHalt)
	e.alert(ctx, "error", "halt", reason)
}

func (e *Engine) isHalted() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.global.Halted || e.global.KillSwitch
}

func (e *Engine) KillAll(ctx context.Context) error {
	e.mutateGlobal(ctx, func(g *store.GlobalState) {
		g.KillSwitch = true
	})
	e.setState(StateHalt)
	e.alert(ctx, "warn", "kill", "kill-switch: flatten, входы запрещены")
	return e.ensureFlat(ctx, "kill")
}

func (e *Engine) Resume(ctx context.Context) {
	e.mutateGlobal(ctx, func(g *store.GlobalState) {
		g.KillSwitch = false
		g.Halted = false
		g.HaltReason = ""
		g.LateStreak = 0
	})
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
