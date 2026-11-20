package lighter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type HTTPClient struct {
	BaseURL   string
	HTTP      *http.Client
	AuthToken string
	mu        sync.Mutex
	tokenFn   func(context.Context) (string, error)
	minInterval time.Duration
	lastReq     time.Time
	rateMu      sync.Mutex
}

func NewHTTP(baseURL string) *HTTPClient {
	return &HTTPClient{
		BaseURL: strings.TrimRight(baseURL, "/"),
		HTTP: &http.Client{
			Timeout: 4 * time.Second,
			Transport: &http.Transport{
				Proxy: http.ProxyFromEnvironment,
				DialContext: (&net.Dialer{
					Timeout:   800 * time.Millisecond,
					KeepAlive: 30 * time.Second,
				}).DialContext,
				MaxIdleConns:        32,
				MaxIdleConnsPerHost: 16,
				IdleConnTimeout:     90 * time.Second,
				TLSHandshakeTimeout: 800 * time.Millisecond,
			},
		},
		minInterval: 200 * time.Millisecond,
	}
}

func (c *HTTPClient) SetTokenSource(fn func(context.Context) (string, error)) {
	c.tokenFn = fn
}

func (c *HTTPClient) pace(ctx context.Context) error {
	c.rateMu.Lock()
	defer c.rateMu.Unlock()
	gap := c.minInterval
	if gap <= 0 {
		gap = 200 * time.Millisecond
	}
	if !c.lastReq.IsZero() {
		if wait := gap - time.Since(c.lastReq); wait > 0 {
			t := time.NewTimer(wait)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
			}
		}
	}
	c.lastReq = time.Now()
	return nil
}

func (c *HTTPClient) get(ctx context.Context, path string, q url.Values, auth bool) ([]byte, error) {
	return c.doGET(ctx, path, q, auth, true)
}

func (c *HTTPClient) getUrgent(ctx context.Context, path string, q url.Values, auth bool) ([]byte, error) {
	return c.doGET(ctx, path, q, auth, false)
}

func (c *HTTPClient) doGET(ctx context.Context, path string, q url.Values, auth, paced bool) ([]byte, error) {
	u := c.BaseURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var lastErr error
	attempts := 12
	if !paced {
		attempts = 2
	}
	for attempt := 0; attempt < attempts; attempt++ {
		if paced {
			if err := c.pace(ctx); err != nil {
				return nil, err
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		if auth {
			tok := c.AuthToken
			if c.tokenFn != nil {
				t, err := c.tokenFn(ctx)
				if err != nil {
					return nil, err
				}
				tok = t
			}
			if tok != "" {
				req.Header.Set("Authorization", tok)
			}
		}
		resp, err := c.HTTP.Do(req)
		if err != nil {
			return nil, err
		}
		b, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if retryableHTTP(resp.StatusCode, b) {
			wait := retryAfter(resp.Header.Get("Retry-After"), attempt)
			lastErr = fmt.Errorf("GET %s: HTTP %d: %s", path, resp.StatusCode, truncate(string(b), 200))
			t := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				t.Stop()
				return nil, ctx.Err()
			case <-t.C:
			}
			continue
		}
		if resp.StatusCode >= 400 {
			return nil, fmt.Errorf("GET %s: HTTP %d: %s", path, resp.StatusCode, truncate(string(b), 400))
		}
		return b, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("GET %s: rate limited", path)
	}
	return nil, lastErr
}

func retryableHTTP(code int, body []byte) bool {
	if code == http.StatusTooManyRequests || code == http.StatusServiceUnavailable || code == http.StatusBadGateway {
		return true
	}
	if code == http.StatusMethodNotAllowed || code == http.StatusForbidden || code == http.StatusAccepted {
		s := string(body)
		if strings.Contains(s, "Human Verification") || strings.Contains(s, "awsWaf") || strings.Contains(s, "Just a moment") || strings.Contains(s, "<!DOCTYPE html>") {
			return true
		}
	}
	return false
}

func retryAfter(hdr string, attempt int) time.Duration {
	if hdr != "" {
		if sec, err := strconv.Atoi(strings.TrimSpace(hdr)); err == nil && sec > 0 {
			d := time.Duration(sec) * time.Second
			if d > 3*time.Minute {
				d = 3 * time.Minute
			}
			return d
		}
	}
	d := time.Duration(1<<min(attempt, 5)) * time.Second
	if d > 30*time.Second {
		d = 30 * time.Second
	}
	return d
}

func (c *HTTPClient) postForm(ctx context.Context, path string, form url.Values) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("POST %s: HTTP %d: %s", path, resp.StatusCode, truncate(string(b), 400))
	}
	return b, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

type MarketMeta struct {
	Symbol         string
	MarketID       uint16
	SizeDecimals   int
	PriceDecimals  int
	MinBaseAmount  float64
	LastTradePrice float64
}

type OrderBookDetailsResp struct {
	Code             int               `json:"code"`
	Message          string            `json:"message"`
	OrderBookDetails []json.RawMessage `json:"order_book_details"`
	OrderBooks       []json.RawMessage `json:"order_books"`
}

func (c *HTTPClient) Markets(ctx context.Context) ([]MarketMeta, error) {
	b, err := c.get(ctx, "/api/v1/orderBookDetails", nil, false)
	if err != nil {
		return nil, err
	}
	var wrap OrderBookDetailsResp
	if err := json.Unmarshal(b, &wrap); err != nil {
		return nil, err
	}
	raw := wrap.OrderBookDetails
	if len(raw) == 0 {
		raw = wrap.OrderBooks
	}
	var out []MarketMeta
	for _, r := range raw {
		var m map[string]any
		if err := json.Unmarshal(r, &m); err != nil {
			continue
		}
		sym := strField(m, "symbol", "market_symbol")
		id := intField(m, "market_id", "market_index", "index")
		mid, ok := asMarketID(id)
		if !ok {
			continue
		}
		out = append(out, MarketMeta{
			Symbol:         strings.ToUpper(sym),
			MarketID:       mid,
			SizeDecimals:   intField(m, "supported_size_decimals", "size_decimals"),
			PriceDecimals:  intField(m, "supported_price_decimals", "price_decimals"),
			MinBaseAmount:  floatField(m, "min_base_amount"),
			LastTradePrice: floatField(m, "last_trade_price", "last_price", "mark_price"),
		})
	}
	return out, nil
}

func (c *HTTPClient) ResolveMarkets(ctx context.Context, symbols []string) (map[string]MarketMeta, error) {
	all, err := c.Markets(ctx)
	if err != nil {
		return nil, err
	}
	bySym := map[string]MarketMeta{}
	for _, m := range all {
		bySym[m.Symbol] = m
		if strings.HasSuffix(m.Symbol, "USDT") {
			bySym[strings.TrimSuffix(m.Symbol, "USDT")] = m
		}
		if strings.HasSuffix(m.Symbol, "USD") {
			bySym[strings.TrimSuffix(m.Symbol, "USD")] = m
		}
	}
	out := map[string]MarketMeta{}
	for _, s := range symbols {
		m, ok := bySym[strings.ToUpper(s)]
		if !ok {
			return nil, fmt.Errorf("market %s not found on Lighter (have %d books)", s, len(all))
		}
		out[strings.ToUpper(s)] = m
	}
	return out, nil
}

func (c *HTTPClient) LastPrice(ctx context.Context, symbol string) (float64, error) {
	all, err := c.Markets(ctx)
	if err != nil {
		return 0, err
	}
	want := strings.ToUpper(symbol)
	for _, m := range all {
		if m.Symbol == want || strings.TrimSuffix(m.Symbol, "USD") == want || strings.TrimSuffix(m.Symbol, "USDT") == want {
			if m.LastTradePrice > 0 {
				return m.LastTradePrice, nil
			}
		}
	}
	return 0, fmt.Errorf("no last price for %s", symbol)
}

type Account struct {
	Index           int64
	Collateral      float64
	Available       float64
	TotalAssetValue float64
	Positions       []Position
}

type Position struct {
	MarketID      uint16
	Symbol        string
	Sign          int
	Size          float64
	AvgEntry      float64
	UnrealizedPnL float64
	PositionValue float64
}

func (c *HTTPClient) Account(ctx context.Context, accountIndex int64) (*Account, error) {
	q := url.Values{}
	q.Set("by", "index")
	q.Set("value", strconv.FormatInt(accountIndex, 10))
	b, err := c.get(ctx, "/api/v1/account", q, false)
	if err != nil {
		return nil, err
	}
	var wrap struct {
		Code     int               `json:"code"`
		Message  string            `json:"message"`
		Accounts []json.RawMessage `json:"accounts"`
	}
	if err := json.Unmarshal(b, &wrap); err != nil {
		return nil, err
	}
	if len(wrap.Accounts) == 0 {
		return parseAccount(b)
	}
	return parseAccount(wrap.Accounts[0])
}

func parseAccount(b []byte) (*Account, error) {
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	acc := &Account{
		Index:           int64(intField(m, "account_index", "index")),
		Collateral:      floatField(m, "collateral"),
		Available:       floatField(m, "available_balance"),
		TotalAssetValue: floatField(m, "total_asset_value"),
	}
	if acc.TotalAssetValue == 0 {
		acc.TotalAssetValue = acc.Collateral
	}
	rawPos, _ := m["positions"].([]any)
	for _, p := range rawPos {
		pm, ok := p.(map[string]any)
		if !ok {
			continue
		}
		id := intField(pm, "market_id")
		mid, ok := asMarketID(id)
		if !ok {
			continue
		}
		acc.Positions = append(acc.Positions, Position{
			MarketID:      mid,
			Symbol:        strings.ToUpper(strField(pm, "symbol")),
			Sign:          intField(pm, "sign"),
			Size:          math.Abs(floatField(pm, "position")),
			AvgEntry:      floatField(pm, "avg_entry_price"),
			UnrealizedPnL: floatField(pm, "unrealized_pnl"),
			PositionValue: floatField(pm, "position_value"),
		})
	}
	return acc, nil
}

func (a *Account) Equity() float64 {
	if a == nil {
		return 0
	}
	if a.TotalAssetValue > 0 {
		return a.TotalAssetValue
	}
	return a.Collateral
}

func (a *Account) Position(symbol string, marketID uint16) (Position, bool) {
	if a == nil {
		return Position{}, false
	}
	want := strings.ToUpper(symbol)
	for _, p := range a.Positions {
		if p.Size == 0 {
			continue
		}
		if p.MarketID == marketID || (want != "" && p.Symbol == want) {
			return p, true
		}
	}
	return Position{}, false
}

type SendTxResp struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	TxHash  string `json:"tx_hash"`
}

func (c *HTTPClient) GetApiKey(accountIndex int64, apiKeyIndex uint8) (string, error) {
	q := url.Values{}
	q.Set("account_index", strconv.FormatInt(accountIndex, 10))
	b, err := c.get(context.Background(), "/api/v1/apikeys", q, false)
	if err != nil {
		return "", err
	}
	var wrap struct {
		ApiKeys []struct {
			ApiKeyIndex uint8  `json:"api_key_index"`
			PublicKey   string `json:"public_key"`
		} `json:"api_keys"`
	}
	if err := json.Unmarshal(b, &wrap); err != nil {
		return "", err
	}
	for _, k := range wrap.ApiKeys {
		if k.ApiKeyIndex == apiKeyIndex {
			return k.PublicKey, nil
		}
	}
	return "", fmt.Errorf("no api key returned for index %d", apiKeyIndex)
}

func (c *HTTPClient) InvalidateApiKeys(accountIndex int64) {}

func (c *HTTPClient) GetNextNonce(accountIndex int64, apiKeyIndex uint8) (int64, error) {
	q := url.Values{}
	q.Set("account_index", strconv.FormatInt(accountIndex, 10))
	q.Set("api_key_index", strconv.Itoa(int(apiKeyIndex)))
	nctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	b, err := c.getUrgent(nctx, "/api/v1/nextNonce", q, false)
	if err != nil {
		return 0, err
	}
	var wrap struct {
		Code  int   `json:"code"`
		Nonce int64 `json:"nonce"`
	}
	if err := json.Unmarshal(b, &wrap); err != nil {
		return 0, err
	}
	return wrap.Nonce, nil
}

func (c *HTTPClient) SendTx(ctx context.Context, txType int, txInfo string) (*SendTxResp, error) {
	form := url.Values{}
	form.Set("tx_type", strconv.Itoa(txType))
	form.Set("tx_info", txInfo)
	b, err := c.postForm(ctx, "/api/v1/sendTx", form)
	if err != nil {
		return nil, err
	}
	var resp SendTxResp
	if err := json.Unmarshal(b, &resp); err != nil {
		return nil, err
	}
	if resp.Code != 200 && resp.Code != 0 {
		return &resp, fmt.Errorf("sendTx code=%d msg=%s hash=%s", resp.Code, resp.Message, resp.TxHash)
	}
	return &resp, nil
}

func strField(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			switch t := v.(type) {
			case string:
				return t
			case float64:
				return strconv.FormatInt(int64(t), 10)
			case json.Number:
				return t.String()
			}
		}
	}
	return ""
}

func asMarketID(id int) (uint16, bool) {
	if id < 0 || id > 65535 {
		return 0, false
	}
	return uint16(id), true
}

func intField(m map[string]any, keys ...string) int {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			switch t := v.(type) {
			case float64:
				return int(t)
			case int:
				return t
			case int64:
				return int(t)
			case string:
				n, _ := strconv.Atoi(t)
				return n
			case json.Number:
				n, _ := t.Int64()
				return int(n)
			}
		}
	}
	return 0
}

func floatField(m map[string]any, keys ...string) float64 {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			switch t := v.(type) {
			case float64:
				return t
			case string:
				n, _ := strconv.ParseFloat(t, 64)
				return n
			case json.Number:
				n, _ := t.Float64()
				return n
			case int:
				return float64(t)
			}
		}
	}
	return 0
}

func ScalePrice(px float64, decimals int) uint32 {
	if decimals < 0 {
		decimals = 0
	}
	v := px * math.Pow10(decimals)
	if v < 0 {
		v = 0
	}
	if v > float64(math.MaxUint32) {
		v = float64(math.MaxUint32)
	}
	return uint32(math.Round(v))
}

func ScaleAmount(qty float64, decimals int) int64 {
	if decimals < 0 {
		decimals = 0
	}
	v := qty * math.Pow10(decimals)
	if v < 0 {
		v = 0
	}
	return int64(math.Round(v))
}
