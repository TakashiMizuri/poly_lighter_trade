package binance

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	Base string
	HTTP *http.Client
}

func New(base string) *Client {
	return &Client{
		Base: strings.TrimRight(base, "/"),
		HTTP: &http.Client{Timeout: 8 * time.Second},
	}
}

// Close1s is the close of the 1-second kline whose open time is unix seconds `sec`.
func (c *Client) Close1s(ctx context.Context, symbol string, sec int64) (float64, error) {
	return c.klineClose(ctx, symbol, "1s", sec*1000, 1)
}

func (c *Client) Close5m(ctx context.Context, symbol string, windowTS int64) (float64, error) {
	return c.klineClose(ctx, symbol, "5m", windowTS*1000, 1)
}

// StopHit1s is startpack 1_1 shadow: 0.125% of T+1s close vs 1s high/low until window end.
func (c *Client) StopHit1s(ctx context.Context, symbol string, fromSec, toSec int64, long bool, stopPx float64) (bool, error) {
	if stopPx <= 0 || toSec <= fromSec {
		return false, nil
	}
	limit := int(toSec - fromSec)
	if limit < 1 {
		return false, nil
	}
	if limit > 1000 {
		limit = 1000
	}
	u := fmt.Sprintf("%s/api/v3/klines?symbol=%s&interval=1s&startTime=%d&limit=%d",
		c.Base, symbol, fromSec*1000, limit)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return false, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, err
	}
	if resp.StatusCode >= 400 {
		return false, fmt.Errorf("binance HTTP %d: %s", resp.StatusCode, string(b))
	}
	var rows [][]any
	if err := json.Unmarshal(b, &rows); err != nil {
		return false, err
	}
	for _, row := range rows {
		if len(row) < 4 {
			continue
		}
		high := klineNum(row[2])
		low := klineNum(row[3])
		if long && low > 0 && low <= stopPx {
			return true, nil
		}
		if !long && high > 0 && high >= stopPx {
			return true, nil
		}
	}
	return false, nil
}

func klineNum(v any) float64 {
	switch t := v.(type) {
	case string:
		n, _ := strconv.ParseFloat(t, 64)
		return n
	case float64:
		return t
	default:
		return 0
	}
}

func (c *Client) klineClose(ctx context.Context, symbol, interval string, startMs int64, limit int) (float64, error) {
	u := fmt.Sprintf("%s/api/v3/klines?symbol=%s&interval=%s&startTime=%d&limit=%d",
		c.Base, symbol, interval, startMs, limit)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}
	if resp.StatusCode >= 400 {
		return 0, fmt.Errorf("binance HTTP %d: %s", resp.StatusCode, string(b))
	}
	var rows [][]any
	if err := json.Unmarshal(b, &rows); err != nil {
		return 0, err
	}
	if len(rows) == 0 || len(rows[0]) < 5 {
		return 0, fmt.Errorf("no kline")
	}
	switch v := rows[0][4].(type) {
	case string:
		return strconv.ParseFloat(v, 64)
	case float64:
		return v, nil
	default:
		return 0, fmt.Errorf("bad close")
	}
}
