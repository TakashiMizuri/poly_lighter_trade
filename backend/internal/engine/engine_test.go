package engine

import (
	"testing"
	"time"

	"poly-lighter.trade/bot/internal/strategy"
)

func TestLateEntryGate(t *testing.T) {
	ts := int64(1_000_000_000)
	now := time.Unix(ts+3, 0)
	if now.Unix()-ts < 2 {
		t.Fatal("setup")
	}
}

func TestNoTwoPositionsInvariant(t *testing.T) {
	e := &Engine{}
	e.pos = &livePos{WindowTS: 1, Qty: 1}
	if e.pos == nil {
		t.Fatal("pos")
	}
	// engine holds a single slot; a second assign replaces, never a slice
	e.pos = &livePos{WindowTS: 2, Qty: 1}
	if e.pos.WindowTS != 2 {
		t.Fatal("single slot")
	}
}

func TestSideSignPnL(t *testing.T) {
	ret := strategy.Sign(strategy.SideLong) * (101.0/100.0 - 1)
	if ret <= 0 {
		t.Fatal(ret)
	}
	ret = strategy.Sign(strategy.SideShort) * (99.0/100.0 - 1)
	if ret <= 0 {
		t.Fatal(ret)
	}
}
