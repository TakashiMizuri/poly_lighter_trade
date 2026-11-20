package lighter

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"time"
)

type Fill struct {
	MarketID  uint16
	Price     float64
	Size      float64
	USD       float64
	Fee       float64
	Timestamp int64
	TxHash    string
	AskClient int64
	BidClient int64
}

func (c *HTTPClient) AccountTrades(ctx context.Context, accountIndex int64, marketID uint16, limit int) ([]Fill, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	q := url.Values{}
	q.Set("account_index", strconv.FormatInt(accountIndex, 10))
	q.Set("sort_by", "timestamp")
	q.Set("sort_dir", "desc")
	q.Set("limit", strconv.Itoa(limit))
	q.Set("market_id", strconv.Itoa(int(marketID)))
	b, err := c.get(ctx, "/api/v1/trades", q, true)
	if err != nil {
		return nil, err
	}
	var wrap struct {
		Code    int               `json:"code"`
		Message string            `json:"message"`
		Trades  []json.RawMessage `json:"trades"`
	}
	if err := json.Unmarshal(b, &wrap); err != nil {
		return nil, err
	}
	if wrap.Code != 0 && wrap.Code != 200 {
		return nil, fmt.Errorf("trades code=%d msg=%s", wrap.Code, wrap.Message)
	}
	out := make([]Fill, 0, len(wrap.Trades))
	for _, raw := range wrap.Trades {
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			continue
		}
		mid, ok := asMarketID(intField(m, "market_id"))
		if !ok {
			continue
		}
		out = append(out, Fill{
			MarketID:  mid,
			Price:     floatField(m, "price"),
			Size:      floatField(m, "size"),
			USD:       floatField(m, "usd_amount"),
			Fee:       takerFeeUSD(m),
			Timestamp: int64(intField(m, "timestamp")),
			TxHash:    strField(m, "tx_hash"),
			AskClient: int64(intField(m, "ask_client_id", "ask_client_order_index", "ask_client_order_id", "client_order_index")),
			BidClient: int64(intField(m, "bid_client_id", "bid_client_order_index", "bid_client_order_id")),
		})
	}
	return out, nil
}

func takerFeeUSD(m map[string]any) float64 {
	fee := floatField(m, "taker_fee")
	if fee <= 0 {
		return 0
	}
	notional := floatField(m, "usd_amount")
	if notional <= 0 {
		notional = floatField(m, "price") * floatField(m, "size")
	}
	const maxPPM = 10000
	if fee == float64(int64(fee)) && fee <= maxPPM && notional > 0 {
		return notional * fee / 1e6
	}
	if fee == float64(int64(fee)) && fee > maxPPM {
		if micro := fee / 1e6; notional > 0 && micro > 0 && micro < notional*0.02 {
			return micro
		}
	}
	if notional > 0 && fee < notional*0.02 {
		return fee
	}
	return 0
}

func FillsForClient(fills []Fill, clientIdx int64) []Fill {
	if clientIdx == 0 {
		return nil
	}
	var out []Fill
	for _, f := range fills {
		if f.AskClient == clientIdx || f.BidClient == clientIdx {
			out = append(out, f)
		}
	}
	return out
}

func tradeUnix(ts int64) float64 {
	if ts > 1_000_000_000_000 {
		return float64(ts) / 1000.0
	}
	if ts > 1_000_000_000 {
		return float64(ts)
	}
	return 0
}

func VWAP(fills []Fill) (px, qty, fee, fillUnix float64) {
	var quote float64
	for _, f := range fills {
		if f.Size <= 0 || f.Price <= 0 {
			continue
		}
		qty += f.Size
		quote += f.Size * f.Price
		fee += f.Fee
		if u := tradeUnix(f.Timestamp); u > fillUnix {
			fillUnix = u
		}
	}
	if qty <= 0 {
		return 0, 0, 0, 0
	}
	return quote / qty, qty, fee, fillUnix
}

type ExchangeOrder struct {
	ClientIdx int64
	CreatedAt int64
	Timestamp int64
	TxTime    int64
	Status    string
}

func (o ExchangeOrder) Unix() float64 {
	if u := coarseUnix(o.TxTime); u > 0 {
		return u
	}
	if u := tradeUnix(o.CreatedAt); u > 0 {
		return u
	}
	return tradeUnix(o.Timestamp)
}

// CreatedUnix is the exchange order-creation time (not trade print, not local clock).
func (o ExchangeOrder) CreatedUnix() float64 {
	if u := tradeUnix(o.CreatedAt); u > 0 {
		return u
	}
	return tradeUnix(o.Timestamp)
}

func coarseUnix(ts int64) float64 {
	if ts > 1_000_000_000_000_000 {
		return float64(ts) / 1e6
	}
	if ts > 1_000_000_000_000 {
		return float64(ts) / 1e3
	}
	if ts > 1_000_000_000 {
		return float64(ts)
	}
	return 0
}

func (c *HTTPClient) OrderByClient(ctx context.Context, accountIndex, clientIdx int64) (ExchangeOrder, error) {
	if clientIdx == 0 {
		return ExchangeOrder{}, fmt.Errorf("no client id")
	}
	q := url.Values{}
	q.Set("account_index", strconv.FormatInt(accountIndex, 10))
	q.Set("client_order_indexes", strconv.FormatInt(clientIdx, 10))
	b, err := c.getUrgent(ctx, "/api/v1/accountOrders", q, true)
	if err != nil {
		return ExchangeOrder{}, err
	}
	var wrap struct {
		Code    int               `json:"code"`
		Message string            `json:"message"`
		Orders  []json.RawMessage `json:"orders"`
	}
	if err := json.Unmarshal(b, &wrap); err != nil {
		return ExchangeOrder{}, err
	}
	if wrap.Code != 0 && wrap.Code != 200 {
		return ExchangeOrder{}, fmt.Errorf("accountOrders code=%d msg=%s", wrap.Code, wrap.Message)
	}
	for _, raw := range wrap.Orders {
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			continue
		}
		idx := int64(intField(m, "client_order_index", "client_order_id"))
		if idx != clientIdx {
			continue
		}
		return ExchangeOrder{
			ClientIdx: idx,
			CreatedAt: int64(intField(m, "created_at")),
			Timestamp: int64(intField(m, "timestamp")),
			TxTime:    int64(intField(m, "transaction_time")),
			Status:    strField(m, "status"),
		}, nil
	}
	return ExchangeOrder{}, fmt.Errorf("order %d not found", clientIdx)
}

func (c *HTTPClient) WaitFillFor(ctx context.Context, accountIndex int64, marketID uint16, clientIdx int64, fallback float64, budget time.Duration) (px, qty, fee, fillUnix float64) {
	if budget <= 0 {
		budget = 800 * time.Millisecond
	}
	deadline := time.Now().Add(budget)
	for {
		fills, err := c.AccountTrades(ctx, accountIndex, marketID, 100)
		if err == nil {
			subset := FillsForClient(fills, clientIdx)
			if p, q, f, u := VWAP(subset); p > 0 {
				return p, q, f, u
			}
		}
		if time.Now().After(deadline) {
			return fallback, 0, 0, 0
		}
		select {
		case <-ctx.Done():
			return fallback, 0, 0, 0
		case <-time.After(80 * time.Millisecond):
		}
	}
}
