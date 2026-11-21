package polymarket

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"poly-lighter.trade/bot/internal/strategy"
)

type Client struct {
	Gamma string
	CLOB  string
	WSURL string
	HTTP  *http.Client

	mu       sync.Mutex
	books    map[string]*book
	wsErr    string
	wsOK     bool
	armedTS  int64
	armedMkt Market
	wsCancel context.CancelFunc
}

type book struct {
	bid, ask float64
	recv     time.Time
	source   string
}

type Market struct {
	Slug      string
	TokenUp   string
	TokenDown string
}

func New(gamma, clob, ws string) *Client {
	return &Client{
		Gamma: strings.TrimRight(gamma, "/"),
		CLOB:  strings.TrimRight(clob, "/"),
		WSURL: ws,
		HTTP:  &http.Client{Timeout: 600 * time.Millisecond},
		books: map[string]*book{},
	}
}

func (c *Client) WSState() (ok bool, err string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.wsOK, c.wsErr
}

func (c *Client) Resolve(ctx context.Context, slug string) (Market, error) {
	u := c.Gamma + "/events/slug/" + slug
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return Market{}, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Market{}, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return Market{}, err
	}
	if resp.StatusCode == 404 {
		return Market{}, fmt.Errorf("no_market")
	}
	if resp.StatusCode >= 400 {
		return Market{}, fmt.Errorf("gamma HTTP %d: %s", resp.StatusCode, truncate(string(b), 200))
	}
	return parseEvent(slug, b)
}

func parseEvent(slug string, b []byte) (Market, error) {
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		return Market{}, err
	}
	markets, _ := raw["markets"].([]any)
	if len(markets) == 0 {
		return Market{}, fmt.Errorf("no_market")
	}
	m, _ := markets[0].(map[string]any)
	outcomes := asStringSlice(m["outcomes"])
	tokens := asStringSlice(m["clobTokenIds"])
	if len(outcomes) == 0 || len(tokens) == 0 || len(outcomes) != len(tokens) {
		return Market{}, fmt.Errorf("no_market: outcomes/tokens")
	}
	up, down := -1, -1
	for i, o := range outcomes {
		switch strings.ToLower(strings.TrimSpace(o)) {
		case "up":
			up = i
		case "down":
			down = i
		}
	}
	if up < 0 || down < 0 {
		return Market{}, fmt.Errorf("no_market: up/down outcomes")
	}
	return Market{Slug: slug, TokenUp: tokens[up], TokenDown: tokens[down]}, nil
}

func asStringSlice(v any) []string {
	switch t := v.(type) {
	case []any:
		out := make([]string, 0, len(t))
		for _, x := range t {
			out = append(out, fmt.Sprint(x))
		}
		return out
	case []string:
		return t
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return nil
		}
		var arr []any
		if json.Unmarshal([]byte(s), &arr) == nil {
			return asStringSlice(arr)
		}
		var ss []string
		if json.Unmarshal([]byte(s), &ss) == nil {
			return ss
		}
	}
	return nil
}

type restBook struct {
	Bids []struct {
		Price string `json:"price"`
	} `json:"bids"`
	Asks []struct {
		Price string `json:"price"`
	} `json:"asks"`
}

func (c *Client) RESTBook(ctx context.Context, tokenID string) (bid, ask float64, err error) {
	u := c.CLOB + "/book?token_id=" + url.QueryEscape(tokenID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, 0, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, 0, err
	}
	if resp.StatusCode >= 400 {
		return 0, 0, fmt.Errorf("clob book HTTP %d", resp.StatusCode)
	}
	var book restBook
	if err := json.Unmarshal(b, &book); err != nil {
		return 0, 0, err
	}
	if len(book.Bids) == 0 || len(book.Asks) == 0 {
		return 0, 0, fmt.Errorf("empty book")
	}
	bid, _ = strconv.ParseFloat(book.Bids[0].Price, 64)
	ask, _ = strconv.ParseFloat(book.Asks[0].Price, 64)
	for _, x := range book.Bids {
		if p, e := strconv.ParseFloat(x.Price, 64); e == nil && p > bid {
			bid = p
		}
	}
	for _, x := range book.Asks {
		if p, e := strconv.ParseFloat(x.Price, 64); e == nil && (ask == 0 || p < ask) {
			ask = p
		}
	}
	return bid, ask, nil
}

func (c *Client) GammaMid(ctx context.Context, slug string) (float64, error) {
	u := c.Gamma + "/events/slug/" + slug
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		return 0, err
	}
	markets, _ := raw["markets"].([]any)
	if len(markets) == 0 {
		return 0, fmt.Errorf("no market")
	}
	m, _ := markets[0].(map[string]any)
	outcomes := asStringSlice(m["outcomes"])
	prices := asStringSlice(m["outcomePrices"])
	if len(outcomes) != len(prices) {
		return 0, fmt.Errorf("no gamma prices")
	}
	for i, o := range outcomes {
		if strings.EqualFold(o, "up") {
			return strconv.ParseFloat(prices[i], 64)
		}
	}
	return 0, fmt.Errorf("no up price")
}

func (c *Client) WarmREST(ctx context.Context, mkt Market) {
	if mkt.TokenUp != "" {
		if bid, ask, err := c.RESTBook(ctx, mkt.TokenUp); err == nil {
			c.setBook(mkt.TokenUp, bid, ask, time.Now(), "rest_clob")
		}
	}
	if mkt.TokenDown != "" {
		if bid, ask, err := c.RESTBook(ctx, mkt.TokenDown); err == nil {
			c.setBook(mkt.TokenDown, bid, ask, time.Now(), "rest_clob")
		}
	}
}

func (c *Client) SnapshotCached(mkt Market, now time.Time, maxAge time.Duration) (strategy.Quote, error) {
	q := strategy.Quote{TokenUp: mkt.TokenUp, TokenDown: mkt.TokenDown}
	bid, ask, recv, source, ok := c.cached(mkt.TokenUp, now, maxAge)
	if !ok {
		return q, fmt.Errorf("no live book")
	}
	q.BidUp, q.AskUp, q.Recv, q.Source = bid, ask, recv, source
	if dBid, dAsk, _, _, dOK := c.cached(mkt.TokenDown, now, maxAge); dOK {
		q.BidDown, q.AskDown, q.HasDown = dBid, dAsk, true
	}
	return q, nil
}

func (c *Client) SnapshotUp(ctx context.Context, mkt Market, now time.Time, maxAge time.Duration) (strategy.Quote, error) {
	if q, err := c.SnapshotCached(mkt, now, maxAge); err == nil {
		return q, nil
	}
	q := strategy.Quote{TokenUp: mkt.TokenUp, TokenDown: mkt.TokenDown}
	bid, ask, err := c.RESTBook(ctx, mkt.TokenUp)
	if err != nil {
		// Live gate uses CLOB only. Gamma mid must not become a synthetic book.
		return q, err
	}
	q.BidUp, q.AskUp, q.Recv, q.Source = bid, ask, time.Now(), "rest_clob"
	return q, nil
}

func (c *Client) cached(token string, now time.Time, maxAge time.Duration) (bid, ask float64, recv time.Time, source string, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b := c.books[token]
	if b == nil || b.bid <= 0 || b.ask <= 0 {
		return 0, 0, time.Time{}, "", false
	}
	if now.Sub(b.recv) > maxAge {
		return 0, 0, time.Time{}, "", false
	}
	return b.bid, b.ask, b.recv, b.source, true
}

func (c *Client) setBook(token string, bid, ask float64, recv time.Time, source string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if bid > 0 && ask > 0 {
		c.books[token] = &book{bid: bid, ask: ask, recv: recv, source: source}
	}
}

func (c *Client) Arm(ts int64, mkt Market) {
	c.mu.Lock()
	if c.wsCancel != nil {
		c.wsCancel()
	}
	if c.armedMkt.TokenUp != "" {
		delete(c.books, c.armedMkt.TokenUp)
		delete(c.books, c.armedMkt.TokenDown)
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.wsCancel = cancel
	c.armedTS = ts
	c.armedMkt = mkt
	c.wsOK, c.wsErr = false, "connecting"
	c.mu.Unlock()
	go c.runWS(ctx, mkt)
}

func (c *Client) runWS(ctx context.Context, mkt Market) {
	backoff := 250 * time.Millisecond
	for ctx.Err() == nil {
		if err := c.runWSConn(ctx, mkt); err != nil && ctx.Err() == nil {
			c.mu.Lock()
			c.wsOK, c.wsErr = false, err.Error()
			c.mu.Unlock()
		}
		t := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		if backoff < 3*time.Second {
			backoff *= 2
		}
	}
}

func (c *Client) runWSConn(ctx context.Context, mkt Market) error {
	dialer := websocket.Dialer{HandshakeTimeout: 8 * time.Second}
	conn, _, err := dialer.DialContext(ctx, c.WSURL, nil)
	if err != nil {
		return err
	}
	defer conn.Close()
	sub := map[string]any{
		"type":                   "market",
		"assets_ids":             []string{mkt.TokenUp, mkt.TokenDown},
		"custom_feature_enabled": true,
	}
	if err := conn.WriteJSON(sub); err != nil {
		return err
	}
	c.mu.Lock()
	c.wsOK, c.wsErr = true, ""
	c.mu.Unlock()

	done := make(chan struct{})
	connDone := make(chan struct{})
	go func() {
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				_ = conn.WriteMessage(websocket.TextMessage, []byte("PING"))
			}
		}
	}()
	defer close(done)
	defer close(connDone)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-connDone:
		}
	}()

	conn.SetReadDeadline(time.Now().Add(45 * time.Second))
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		conn.SetReadDeadline(time.Now().Add(45 * time.Second))
		c.handleWS(data, time.Now())
	}
}

func (c *Client) handleWS(data []byte, now time.Time) {
	s := strings.TrimSpace(string(data))
	if s == "" || s == "PONG" || s == "PING" {
		return
	}
	var msg map[string]any
	if json.Unmarshal(data, &msg) != nil {
		var arr []map[string]any
		if json.Unmarshal(data, &arr) == nil {
			for _, m := range arr {
				c.applyWS(m, now)
			}
		}
		return
	}
	c.applyWS(msg, now)
}

func (c *Client) applyWS(msg map[string]any, now time.Time) {
	event, _ := msg["event_type"].(string)
	if event == "" {
		event, _ = msg["type"].(string)
	}
	if event == "" {
		event, _ = msg["eventType"].(string)
	}
	if payload, ok := msg["payload"].(map[string]any); ok {
		msg = payload
	}
	asset, _ := msg["asset_id"].(string)
	if asset == "" {
		asset, _ = msg["assetId"].(string)
	}
	if asset == "" {
		asset, _ = msg["assetID"].(string)
	}
	switch event {
	case "book", "best_bid_ask":
		bid, ask := extractBBO(msg)
		if asset != "" {
			c.setBook(asset, bid, ask, now, "ws_clob")
		}
	case "price_change", "price_changes":
		changes, ok := msg["price_changes"].([]any)
		if !ok {
			changes, ok = msg["priceChanges"].([]any)
		}
		if ok {
			for _, raw := range changes {
				change, _ := raw.(map[string]any)
				if change == nil {
					continue
				}
				id, _ := change["asset_id"].(string)
				if id == "" {
					id, _ = change["assetId"].(string)
				}
				bid, ask := extractBBO(change)
				if id != "" && bid > 0 && ask > 0 {
					c.setBook(id, bid, ask, now, "ws_clob")
				}
			}
			return
		}
		bid, ask := extractBBO(msg)
		if asset != "" && bid > 0 && ask > 0 {
			c.setBook(asset, bid, ask, now, "ws_clob")
		}
	}
}

func extractBBO(msg map[string]any) (bid, ask float64) {
	if v, ok := asFloat(msg["best_bid"]); ok {
		bid = v
	}
	if v, ok := asFloat(msg["best_ask"]); ok {
		ask = v
	}
	if v, ok := asFloat(msg["bestBid"]); ok {
		bid = v
	}
	if v, ok := asFloat(msg["bestAsk"]); ok {
		ask = v
	}
	if bids, ok := msg["bids"].([]any); ok {
		for _, x := range bids {
			p := levelPrice(x)
			if p > bid {
				bid = p
			}
		}
	}
	if asks, ok := msg["asks"].([]any); ok {
		for _, x := range asks {
			p := levelPrice(x)
			if p > 0 && (ask == 0 || p < ask) {
				ask = p
			}
		}
	}
	return bid, ask
}

func levelPrice(x any) float64 {
	switch t := x.(type) {
	case map[string]any:
		if v, ok := asFloat(t["price"]); ok {
			return v
		}
	case []any:
		if len(t) > 0 {
			if v, ok := asFloat(t[0]); ok {
				return v
			}
		}
	}
	return 0
}

func asFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case string:
		n, err := strconv.ParseFloat(t, 64)
		return n, err == nil
	case json.Number:
		n, err := t.Float64()
		return n, err == nil
	default:
		return 0, false
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
