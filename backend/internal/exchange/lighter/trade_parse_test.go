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
