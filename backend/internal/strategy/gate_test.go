package strategy

import (
	"testing"
	"time"
)

func TestDecideSideInclusive(t *testing.T) {
	side, reason, both := DecideSide(0.65, 0.65)
	if side != SideLong || reason != "" || both {
		t.Fatalf("got %s %s %v", side, reason, both)
	}
	side, reason, _ = DecideSide(0.35, 0.65)
	if side != SideShort || reason != "" {
		t.Fatalf("short got %s %s", side, reason)
	}
	side, reason, _ = DecideSide(0.649, 0.65)
	if side != SideSkip || reason != "below_th" {
		t.Fatalf("skip got %s %s", side, reason)
	}
	side, reason, both = DecideSide(0.70, 0.65)
	if side != SideLong || both {
		t.Fatal("0.70")
	}
}

func TestQuoteOK(t *testing.T) {
	now := time.Now()
	q := Quote{BidUp: 0.64, AskUp: 0.66, Recv: now, Source: "ws_clob"}
	ok, why := QuoteOK(q, 0.05, 1.0, now)
	if !ok {
		t.Fatal(why)
	}
	q.AskUp = 0.80
	ok, why = QuoteOK(q, 0.05, 1.0, now)
	if ok || why != "bad_quote" {
		t.Fatalf("wide spread %v %s", ok, why)
	}
	q = Quote{BidUp: 0.64, AskUp: 0.66, Recv: now.Add(-2 * time.Second)}
	ok, why = QuoteOK(q, 0.05, 1.0, now)
	if ok || why != "stale_quote" {
		t.Fatalf("stale %v %s", ok, why)
	}
	q = Quote{BidUp: 0.01, AskUp: 0.99, Recv: now}
	ok, _ = QuoteOK(q, 0.05, 1.0, now)
	if ok {
		t.Fatal("walls")
	}
	q = Quote{BidUp: 0.70, AskUp: 0.71, BidDown: 0.20, AskDown: 0.50, HasDown: true, Recv: now}
	ok, why = QuoteOK(q, 0.05, 1.0, now)
	if ok || why != "quote_incoherent" {
		t.Fatalf("incoherent %v %s", ok, why)
	}
}

func TestFloorLot(t *testing.T) {
	if FloorLot(1.239, 2) != 1.23 {
		t.Fatal(FloorLot(1.239, 2))
	}
}
