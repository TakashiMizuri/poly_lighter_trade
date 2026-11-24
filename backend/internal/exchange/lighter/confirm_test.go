package lighter

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestConfirmFillWaitsForTerminalOrFullSize(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":200,"trades":[]}`))
	}))
	defer srv.Close()

	partial := NewStream("ws://unused", 7, 1, nil, nil)
	partial.handle([]byte(`{"type":"update/account_orders","orders":{"1":[{
		"client_order_index":99,"filled_base_amount":"0.00020","filled_quote_amount":"12.042","status":"in-progress"
	}]}}`))
	c := NewHTTP(srv.URL)
	c.Stream = partial
	started := time.Now()
	got := c.ConfirmFill(context.Background(), ConfirmFillOpts{
		AccountIndex: 7,
		MarketID:     1,
		ClientIdx:    99,
		WantQty:      0.00042,
		FallbackPx:   1,
		SizeDecimals: 5,
		Budget:       250 * time.Millisecond,
	})
	if elapsed := time.Since(started); elapsed < 200*time.Millisecond {
		t.Fatalf("partial fill returned early in %s", elapsed)
	}
	if got.Qty < 0.00019 || got.Qty > 0.00021 || got.Px < 60200 || got.Px > 60220 {
		t.Fatalf("deadline partial = %+v", got)
	}

	done := NewStream("ws://unused", 7, 1, nil, nil)
	done.handle([]byte(`{"type":"update/account_all_trades","trades":{"1":[{
		"trade_id_str":"55","price":"60210","size":"0.00042","ask_account_id":7,"ask_client_id":99,
		"taker_fee":"0.01","is_maker_ask":false
	}]}}`))
	fast := NewHTTP("http://127.0.0.1:1")
	fast.Stream = done
	started = time.Now()
	got = fast.ConfirmFill(context.Background(), ConfirmFillOpts{
		AccountIndex: 7,
		MarketID:     1,
		ClientIdx:    99,
		WantQty:      0.00042,
		SizeDecimals: 5,
		Budget:       3 * time.Second,
	})
	if elapsed := time.Since(started); elapsed > 300*time.Millisecond {
		t.Fatalf("full size waited %s", elapsed)
	}
	if got.Qty != 0.00042 || got.Fee != 0.01 {
		t.Fatalf("full = %+v", got)
	}

	terminal := NewStream("ws://unused", 7, 1, nil, nil)
	terminal.handle([]byte(`{"type":"update/account_orders","orders":{"1":[{
		"client_order_index":99,"filled_base_amount":"0.00020","filled_quote_amount":"12.042","status":"canceled-not-enough-liquidity"
	}]}}`))
	c.Stream = terminal
	started = time.Now()
	got = c.ConfirmFill(context.Background(), ConfirmFillOpts{
		AccountIndex: 7,
		MarketID:     1,
		ClientIdx:    99,
		WantQty:      0.00042,
		SizeDecimals: 5,
		Budget:       3 * time.Second,
	})
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("terminal partial waited %s", elapsed)
	}
	if got.Qty < 0.00019 || got.Qty > 0.00021 {
		t.Fatalf("terminal = %+v", got)
	}
}
