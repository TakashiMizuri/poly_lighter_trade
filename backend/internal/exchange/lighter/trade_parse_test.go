package lighter

import (
	"encoding/json"
	"testing"
)

func TestParseTradeRaw(t *testing.T) {
	raw := json.RawMessage(`{
		"market_id": 1,
		"price": 100.5,
		"size": 0.01,
		"usd_amount": 1.005,
		"taker_fee": 0,
		"timestamp": 1700000000,
		"tx_hash": "0xabc",
		"ask_client_order_index": 42,
		"bid_client_id": 7
	}`)
	f, ok := parseTradeRaw(raw)
	if !ok {
		t.Fatal("parse")
	}
	if f.MarketID != 1 || f.Price != 100.5 || f.Size != 0.01 || f.AskClient != 42 || f.BidClient != 7 {
		t.Fatalf("%+v", f)
	}
}

func TestParseTradeRawStringPayload(t *testing.T) {
	raw := json.RawMessage(`{
		"market_id": "1",
		"price": "60078.0",
		"size": "0.00041",
		"usd_amount": "24.63198",
		"taker_fee": "0",
		"timestamp": "1763752501000",
		"tx_hash": "f40b4fed",
		"ask_client_id": "0",
		"ask_client_order_index": "287000009",
		"bid_client_id": 0
	}`)
	f, ok := parseTradeRaw(raw)
	if !ok {
		t.Fatal("string payload must parse")
	}
	if f.MarketID != 1 || f.Price != 60078 || f.Size != 0.00041 || f.AskClient != 287000009 || f.BidClient != 0 {
		t.Fatalf("%+v", f)
	}
	if f.Timestamp != 1763752501000 || f.TxHash != "f40b4fed" {
		t.Fatalf("meta %+v", f)
	}
	matched := FillsMatching([]Fill{f}, 287000009, "")
	if len(matched) != 1 || matched[0].Price != 60078 {
		t.Fatalf("match %+v", matched)
	}
}
