package lighter

import (
	"encoding/json"
	"strconv"
	"strings"
)

// parseTradeRaw accepts Lighter trade payloads. Live trades encode price, size,
// and client ids as strings; a typed float64/int64 struct drops the whole row.
func parseTradeRaw(raw json.RawMessage) (Fill, bool) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil || len(m) == 0 {
		return Fill{}, false
	}
	mid, ok := asMarketID(int(int64Field(m, "market_id")))
	if !ok {
		return Fill{}, false
	}
	price := floatField(m, "price")
	size := floatField(m, "size")
	if price <= 0 || size <= 0 {
		return Fill{}, false
	}
	ask := int64FieldNonZero(m,
		"ask_client_id",
		"ask_client_order_index",
		"ask_client_order_id",
		"client_order_index",
		"ask_client_id_str",
	)
	bid := int64FieldNonZero(m,
		"bid_client_id",
		"bid_client_order_index",
		"bid_client_order_id",
		"bid_client_id_str",
	)
	return Fill{
		MarketID:  mid,
		Price:     price,
		Size:      size,
		USD:       floatField(m, "usd_amount"),
		Fee:       takerFeeUSD(m),
		Timestamp: int64Field(m, "timestamp"),
		TxHash:    strField(m, "tx_hash"),
		AskClient: ask,
		BidClient: bid,
	}, true
}

func int64Field(m map[string]any, key string) int64 {
	v, ok := m[key]
	if !ok || v == nil {
		return 0
	}
	n, ok := int64FromAny(v)
	if !ok {
		return 0
	}
	return n
}

func int64FieldNonZero(m map[string]any, keys ...string) int64 {
	for _, key := range keys {
		v, ok := m[key]
		if !ok || v == nil {
			continue
		}
		n, ok := int64FromAny(v)
		if ok && n != 0 {
			return n
		}
	}
	return 0
}

func int64FromAny(v any) (int64, bool) {
	switch t := v.(type) {
	case float64:
		return int64(t), true
	case string:
		t = strings.TrimSpace(t)
		if t == "" {
			return 0, false
		}
		n, err := strconv.ParseInt(t, 10, 64)
		if err != nil {
			return 0, false
		}
		return n, true
	case json.Number:
		n, err := t.Int64()
		if err != nil {
			f, ferr := t.Float64()
			if ferr != nil {
				return 0, false
			}
			return int64(f), true
		}
		return n, true
	default:
		return 0, false
	}
}
