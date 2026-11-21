package lighter

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
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
	return c.accountTrades(ctx, accountIndex, marketID, limit, false)
}

func (c *HTTPClient) AccountTradesUrgent(ctx context.Context, accountIndex int64, marketID uint16, limit int) ([]Fill, error) {
	return c.accountTrades(ctx, accountIndex, marketID, limit, true)
}

func (c *HTTPClient) accountTrades(ctx context.Context, accountIndex int64, marketID uint16, limit int, urgent bool) ([]Fill, error) {
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
	var b []byte
	var err error
	if urgent {
		b, err = c.getUrgent(ctx, "/api/v1/trades", q, true)
	} else {
		b, err = c.get(ctx, "/api/v1/trades", q, true)
	}
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
		f, ok := parseTradeRaw(raw)
		if !ok {
			continue
		}
		out = append(out, f)
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

func FillsForTx(fills []Fill, txHash string) []Fill {
	want := normTxHash(txHash)
	if want == "" {
		return nil
	}
	var out []Fill
	for _, f := range fills {
		if sameTxHash(f.TxHash, want) {
			out = append(out, f)
		}
	}
	return out
}

func FillsMatching(fills []Fill, clientIdx int64, txHash string) []Fill {
	if subset := FillsForClient(fills, clientIdx); len(subset) > 0 {
		return subset
	}
	return FillsForTx(fills, txHash)
}

// FillsClosingPosition are later prints of about the same size that are not the entry.
func FillsClosingPosition(fills []Fill, entryClient int64, entryHash string, qty, afterUnix float64) []Fill {
	var out []Fill
	tol := math.Max(qty*0.15, 1e-8)
	for _, f := range fills {
		if entryClient != 0 && (f.AskClient == entryClient || f.BidClient == entryClient) {
			continue
		}
		if entryHash != "" && sameTxHash(f.TxHash, entryHash) {
			continue
		}
		if afterUnix > 0 {
			if u := tradeUnix(f.Timestamp); u > 0 && u < afterUnix-0.5 {
				continue
			}
		}
		if qty > 0 && (f.Size+1e-12 < qty-tol || f.Size > qty+tol) {
			continue
		}
		if f.Price <= 0 || f.Size <= 0 {
			continue
		}
		out = append(out, f)
	}
	return out
}

func normTxHash(h string) string {
	h = strings.TrimSpace(strings.ToLower(h))
	h = strings.TrimPrefix(h, "0x")
	return h
}

func sameTxHash(a, b string) bool {
	na, nb := normTxHash(a), normTxHash(b)
	if na == "" || nb == "" {
		return false
	}
	if na == nb {
		return true
	}
	return strings.HasPrefix(na, nb) || strings.HasPrefix(nb, na)
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

