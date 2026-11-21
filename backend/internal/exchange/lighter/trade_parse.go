package lighter

import (
	"encoding/json"
	"strings"
)

// tradeRow is a typed Lighter trade payload with aliases for evolving field names.
type tradeRow struct {
	MarketID             int64   `json:"market_id"`
	Price                float64 `json:"price"`
	Size                 float64 `json:"size"`
	USDAmount            float64 `json:"usd_amount"`
	TakerFee             float64 `json:"taker_fee"`
	Timestamp            int64   `json:"timestamp"`
	TxHash               string  `json:"tx_hash"`
	AskClientID          int64   `json:"ask_client_id"`
	AskClientIDStr       string  `json:"ask_client_id_str"`
	AskClientOrderIndex  int64   `json:"ask_client_order_index"`
	AskClientOrderID     int64   `json:"ask_client_order_id"`
	ClientOrderIndex     int64   `json:"client_order_index"`
	BidClientID          int64   `json:"bid_client_id"`
	BidClientIDStr       string  `json:"bid_client_id_str"`
	BidClientOrderIndex  int64   `json:"bid_client_order_index"`
	BidClientOrderID     int64   `json:"bid_client_order_id"`
}

func parseTradeRaw(raw json.RawMessage) (Fill, bool) {
	var row tradeRow
	if err := json.Unmarshal(raw, &row); err != nil {
		return Fill{}, false
	}
	mid, ok := asMarketID(int(row.MarketID))
	if !ok {
		return Fill{}, false
	}
	ask := firstNonZeroInt64(row.AskClientID, row.AskClientOrderIndex, row.AskClientOrderID, row.ClientOrderIndex)
	if ask == 0 && row.AskClientIDStr != "" {
		ask = parseInt64String(row.AskClientIDStr)
	}
	bid := firstNonZeroInt64(row.BidClientID, row.BidClientOrderIndex, row.BidClientOrderID)
	if bid == 0 && row.BidClientIDStr != "" {
		bid = parseInt64String(row.BidClientIDStr)
	}
	m := map[string]any{
		"taker_fee":  row.TakerFee,
		"usd_amount": row.USDAmount,
		"price":      row.Price,
		"size":       row.Size,
	}
	return Fill{
		MarketID:  mid,
		Price:     row.Price,
		Size:      row.Size,
		USD:       row.USDAmount,
		Fee:       takerFeeUSD(m),
		Timestamp: row.Timestamp,
		TxHash:    row.TxHash,
		AskClient: ask,
		BidClient: bid,
	}, true
}

func firstNonZeroInt64(vals ...int64) int64 {
	for _, v := range vals {
		if v != 0 {
			return v
		}
	}
	return 0
}

func parseInt64String(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	var n int64
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int64(r-'0')
	}
	return n
}
