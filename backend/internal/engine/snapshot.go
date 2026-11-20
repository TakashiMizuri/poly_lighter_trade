package engine

import (
	"context"
	"time"

	"poly-lighter.trade/bot/internal/clock"
	"poly-lighter.trade/bot/internal/store"
)

type Snapshot struct {
	Profile        string         `json:"profile"`
	InstanceID     string         `json:"instance_id"`
	InstanceName   string         `json:"instance_name"`
	Network        string         `json:"network"`
	DryRun         bool           `json:"dry_run"`
	State          string         `json:"state"`
	KillSwitch     bool           `json:"kill_switch"`
	Halted         bool           `json:"halted"`
	HaltReason     string         `json:"halt_reason"`
	ClockOffsetMS  float64        `json:"clock_offset_ms"`
	ClockOK        bool           `json:"clock_ok"`
	ClockRTTMS     float64        `json:"clock_rtt_ms"`
	ClockAgeMS     float64        `json:"clock_age_ms"`
	HostUnix       float64        `json:"host_unix"`
	SyncedUnix     float64        `json:"synced_unix"`
	SecsIntoWindow float64        `json:"secs_into_window"`
	SecsToClose    float64        `json:"secs_to_close"`
	WindowTS       int64          `json:"window_ts"`
	NextTS         int64          `json:"next_ts"`
	PUp            float64        `json:"p_up"`
	PDown          float64        `json:"p_down"`
	Spread         float64        `json:"spread"`
	QuoteSource    string         `json:"quote_source"`
	QuoteAgeMS     float64        `json:"quote_age_ms"`
	Position       string         `json:"position"`
	Qty            float64        `json:"qty"`
	EntryPx        float64        `json:"entry_px"`
	StopPx         float64        `json:"stop_px"`
	StopPricePct   float64        `json:"stop_price_pct"`
	EntryWindow    int64          `json:"entry_window_ts"`
	Equity         float64        `json:"equity"`
	PeakEquity     float64        `json:"peak_equity"`
	ShadowEquity   float64        `json:"shadow_equity"`
	StartedUnix    int64          `json:"started_unix"`
	LastSkip       string         `json:"last_skip"`
	UptimeSec      float64        `json:"uptime_sec"`
	Symbol         string         `json:"symbol"`
	Threshold      float64        `json:"threshold"`
	Leverage       float64        `json:"leverage"`
	EquityFrac     float64        `json:"equity_fraction"`
	LastTimings    *store.Timings `json:"last_timings,omitempty"`
}

func (e *Engine) Snapshot(ctx context.Context) Snapshot {
	e.mu.Lock()
	host := time.Now().UTC()
	synced := host.Add(e.offset)
	clockAge := time.Duration(0)
	clockOK := e.offsetOK && !e.offsetAt.IsZero()
	if clockOK {
		clockAge = time.Since(e.offsetAt)
		clockOK = clockAge <= 2*time.Minute
	}
	if !clockOK {
		synced = host
	}
	ts := clock.WindowOf(host)
	into := float64(host.Unix()) - float64(ts)
	sn := Snapshot{
		Profile: e.Cfg.Profile, InstanceID: e.Cfg.InstanceID, InstanceName: e.Cfg.InstanceName,
		Network: string(e.Cfg.Network), DryRun: e.Cfg.DryRun, State: e.state,
		KillSwitch: e.global.KillSwitch, Halted: e.global.Halted, HaltReason: e.global.HaltReason,
		ClockOffsetMS: e.offset.Seconds() * 1000, ClockOK: clockOK, ClockRTTMS: e.clockRTT.Seconds() * 1000,
		ClockAgeMS: float64(clockAge.Milliseconds()),
		HostUnix:   float64(host.UnixNano()) / 1e9, SyncedUnix: float64(synced.UnixNano()) / 1e9,
		SecsIntoWindow: into, SecsToClose: 300 - into,
		WindowTS: ts, NextTS: ts + 300,
		PUp: e.lastQuote.MidUp(), PDown: 1 - e.lastQuote.MidUp(), Spread: e.lastQuote.Spread(),
		QuoteSource: e.lastQuote.Source, PeakEquity: e.global.PeakEquity, ShadowEquity: e.global.ShadowEquity,
		StartedUnix: e.started.Unix(), LastSkip: e.lastSkip, UptimeSec: time.Since(e.started).Seconds(),
		Symbol: e.Cfg.Symbol, Threshold: e.Cfg.Threshold, Leverage: e.Cfg.Leverage,
		EquityFrac: e.Cfg.EquityFraction, StopPricePct: e.Cfg.StopPricePct, Position: "flat",
		LastTimings: e.lastTimings,
	}
	if e.lastQuote.HasDown && e.lastQuote.BidDown > 0 && e.lastQuote.AskDown > 0 {
		sn.PDown = (e.lastQuote.BidDown + e.lastQuote.AskDown) / 2
	}
	if !e.lastQuote.Recv.IsZero() {
		sn.QuoteAgeMS = float64(host.Sub(e.lastQuote.Recv).Milliseconds())
	}
	if e.pos != nil {
		sn.Position = e.pos.Side
		sn.Qty = e.pos.Qty
		sn.EntryPx = e.pos.EntryPx
		sn.StopPx = e.pos.StopPx
		sn.EntryWindow = e.pos.WindowTS
	}
	e.mu.Unlock()
	if acc, err := e.equity(ctx); err == nil {
		sn.Equity = acc
	}
	return sn
}

func (e *Engine) Stats(ctx context.Context) (store.Stats, error) {
	return e.Store.ComputeStats(ctx)
}
