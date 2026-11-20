package engine

import (
	"time"

	"poly-lighter.trade/bot/internal/store"
)

type winTrace struct {
	ts   int64
	open time.Time
	t    store.Timings
}

func (e *Engine) beginTrace(ts int64) {
	if e.tr != nil && e.tr.ts == ts {
		return
	}
	e.tr = &winTrace{ts: ts, open: time.Unix(ts, 0).UTC(), t: store.Timings{OpenUnix: float64(ts)}}
}

func (e *Engine) traceDur(name string, started time.Time) {
	if e.tr == nil {
		return
	}
	ms := float64(time.Since(started).Milliseconds())
	if ms < 0 {
		ms = 0
	}
	switch name {
	case "arm":
		e.tr.t.MsArm += ms
	case "ready":
		e.tr.t.MsReady += ms
	case "warm_book":
		e.tr.t.MsWarmBook += ms
	case "nonce":
		e.tr.t.MsNonce += ms
	case "has_window":
		e.tr.t.MsHasWindow += ms
	case "quote":
		e.tr.t.MsQuote += ms
	case "equity":
		e.tr.t.MsEquity += ms
	case "send":
		e.tr.t.MsSend += ms
	case "fill_wait":
		e.tr.t.MsFillWait += ms
	case "pos_check":
		e.tr.t.MsPosCheck += ms
	case "order_get":
		e.tr.t.MsOrderGet += ms
	case "stop":
		e.tr.t.MsStop += ms
	case "sqlite":
		e.tr.t.MsSqlite += ms
	}
}

func (e *Engine) traceOpenTo(which string, at time.Time) {
	if e.tr == nil {
		return
	}
	ms := float64(at.Sub(e.tr.open) / time.Millisecond)
	if ms < 0 {
		ms = 0
	}
	switch which {
	case "quote":
		e.tr.t.MsOpenToQuote = ms
	case "decide":
		e.tr.t.MsOpenToDecide = ms
	case "send":
		e.tr.t.MsOpenToSend = ms
	case "exchange":
		e.tr.t.MsOpenToExchange = ms
	}
}

func (e *Engine) stampLateStart() {
	if e.tr == nil {
		return
	}
	late := time.Now().UTC().Sub(e.tr.open)
	if late > 0 {
		e.tr.t.MsLateStart = float64(late / time.Millisecond)
	}
}

func (e *Engine) applyTrace(w *store.Window) {
	if e.tr == nil || w == nil {
		return
	}
	t := e.tr.t
	t.QuoteSource = w.QuoteSource
	if t.MsOpenToDecide == 0 && t.MsLateStart > 0 {
		t.MsOpenToDecide = t.MsLateStart
	}
	if t.MsOpenToSend > 0 && t.MsOpenToExchange >= t.MsOpenToSend {
		t.MsSendToExchange = t.MsOpenToExchange - t.MsOpenToSend
	}
	cp := t
	w.Timings = &cp
	e.lastTimings = &cp
}
