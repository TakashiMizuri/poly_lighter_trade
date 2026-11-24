package lighter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestStreamSendTxPriceAndFill(t *testing.T) {
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.WriteJSON(map[string]any{
			"type": "update/market_stats",
			"market_stats": map[string]any{
				"last_trade_price": "60250.5",
				"mark_price":       "60240",
			},
		})
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var msg struct {
				Type string `json:"type"`
				Data struct {
					ID     string `json:"id"`
					TxType int    `json:"tx_type"`
				} `json:"data"`
			}
			if json.Unmarshal(data, &msg) != nil || msg.Type != "jsonapi/sendtx" {
				continue
			}
			_ = conn.WriteJSON(map[string]any{
				"type":    "jsonapi/sendtx",
				"id":      msg.Data.ID,
				"code":    200,
				"tx_hash": "abc",
			})
			_ = conn.WriteJSON(map[string]any{
				"type": "update/account_all_trades",
				"trades": map[string]any{
					"1": []any{map[string]any{
						"price": "60210.0", "size": "0.00042", "ask_client_id": 99, "ask_account_id": 7, "taker_fee": "0.01",
					}},
				},
			})
		}
	}))
	defer srv.Close()

	hub := NewStream("ws"+strings.TrimPrefix(srv.URL, "http"), 7, 1, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go hub.Run(ctx)

	deadline := time.Now().Add(3 * time.Second)
	var px float64
	var ok bool
	for time.Now().Before(deadline) {
		px, ok = hub.FreshPrice(time.Second)
		if ok {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !ok || px != 60250.5 {
		t.Fatalf("price %v ok %v", px, ok)
	}
	sctx, scancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer scancel()
	resp, err := hub.SendTx(sctx, 14, `{"Nonce":1}`)
	if err != nil || resp == nil || resp.TxHash != "abc" || resp.Code != 200 {
		t.Fatalf("sendtx resp=%+v err=%v", resp, err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		fpx, qty, fee, fok := hub.Fill(99)
		if fok && qty == 0.00042 && fpx == 60210 && fee == 0.01 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("fill missing")
}

func TestNoteSubmitErr(t *testing.T) {
	s := &Signer{nextNonce: 8, nonceReady: true}
	s.noteSubmitErr(7, ErrNotSubmitted)
	if s.nextNonce != 7 || !s.nonceReady {
		t.Fatalf("rollback nonce=%d ready=%v", s.nextNonce, s.nonceReady)
	}
	s.nextNonce = 8
	s.noteSubmitErr(7, context.DeadlineExceeded)
	if s.nextNonce != 8 || !s.nonceReady {
		t.Fatalf("ambiguous nonce=%d ready=%v", s.nextNonce, s.nonceReady)
	}
	s.noteSubmitErr(7, context.DeadlineExceeded)
	s.invalidateNonce()
	if s.nonceReady {
		t.Fatal("explicit invalidate")
	}
	s.nonceReady = true
	s.nextNonce = 8
	s.noteSubmitErr(7, fmt.Errorf("%w: write", ErrAckLost))
	if s.nextNonce != 8 || !s.nonceReady {
		t.Fatalf("ack lost must keep nonce=%d ready=%v", s.nextNonce, s.nonceReady)
	}
}

func TestCachedIPUsedWhenLookupFails(t *testing.T) {
	rememberIP("lighter.test", "203.0.113.8")
	ip, ok := cachedIP("lighter.test")
	if !ok || ip != "203.0.113.8" {
		t.Fatalf("cache %q %v", ip, ok)
	}
}

func TestStreamTradeFeeDedupAndOrderStatus(t *testing.T) {
	s := NewStream("ws://unused", 57890, 0, nil, nil)
	trade := `{
		"trade_id": 16164557907,
		"trade_id_str": "16164557907",
		"price": "2181.83",
		"size": "0.1336",
		"usd_amount": "291.492488",
		"ask_client_id": 363283,
		"bid_client_id": 23004521241,
		"ask_account_id": 57890,
		"bid_account_id": 317068,
		"is_maker_ask": false,
		"taker_fee": 196,
		"maker_fee": 28
	}`
	msg := `{"type":"update/account_all_trades","trades":{"0":[` + trade + `]}}`
	s.handle([]byte(msg))
	s.handle([]byte(msg))
	px, qty, fee, terminal, ok := s.FillState(363283)
	if !ok || terminal {
		t.Fatalf("fill ok=%v terminal=%v", ok, terminal)
	}
	if qty != 0.1336 || px != 2181.83 {
		t.Fatalf("px=%v qty=%v", px, qty)
	}
	wantFee := 291.492488 * 196 / 1e6
	if mathAbs(fee-wantFee) > 1e-9 {
		t.Fatalf("taker fee=%v want=%v", fee, wantFee)
	}

	maker := NewStream("ws://unused", 317068, 0, nil, nil)
	maker.handle([]byte(`{"type":"update/account_all_trades","trades":{"0":[{
		"trade_id_str":"9","price":"2181.83","size":"0.1336","usd_amount":"291.492488",
		"ask_client_id":1,"bid_client_id":2,"ask_account_id":5,"bid_account_id":317068,
		"is_maker_ask":false,"taker_fee":196,"maker_fee":28
	}]}}`))
	_, _, mfee, _, mok := maker.FillState(2)
	if !mok || mathAbs(mfee-291.492488*28/1e6) > 1e-9 {
		t.Fatalf("maker fee=%v ok=%v", mfee, mok)
	}

	s.handle([]byte(`{"type":"update/account_orders","orders":{"0":[{
		"client_order_index":363283,"filled_base_amount":"0.0500","filled_quote_amount":"109.0915","status":"in-progress"
	}]}}`))
	_, qty, _, terminal, _ = s.FillState(363283)
	if terminal || qty != 0.1336 {
		t.Fatalf("tape must win over a smaller snapshot qty=%v terminal=%v", qty, terminal)
	}
	s.handle([]byte(`{"type":"update/account_orders","orders":{"0":[{
		"client_order_index":363283,"filled_base_amount":"0.2000","filled_quote_amount":"436.366","status":"filled"
	}]}}`))
	px, qty, fee, terminal, ok = s.FillState(363283)
	if !ok || !terminal || qty != 0.2 || px != 2181.83 {
		t.Fatalf("order ahead px=%v qty=%v terminal=%v ok=%v", px, qty, terminal, ok)
	}
	if fee != 0 {
		t.Fatalf("fee must stay unknown until the tape covers the order, got %v", fee)
	}
	s.handle([]byte(`{"type":"update/account_orders","orders":{"0":[{
		"client_order_index":363283,"filled_base_amount":"0.0100","filled_quote_amount":"21.8183","status":"in-progress"
	}]}}`))
	_, qty, _, terminal, _ = s.FillState(363283)
	if !terminal || qty != 0.2 {
		t.Fatalf("stale snapshot qty=%v terminal=%v", qty, terminal)
	}
}

func TestSendTxWriteErrorIsAckLost(t *testing.T) {
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := NewStream(wsURL, 1, 1, nil, nil)
	s.mu.Lock()
	s.conn = conn
	s.connected = true
	s.mu.Unlock()

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = s.SendTx(canceled, 14, `{"Nonce":1}`)
	if !errors.Is(err, ErrNotSubmitted) {
		t.Fatalf("canceled before write: %v", err)
	}

	_ = conn.Close()
	_, err = s.SendTx(context.Background(), 14, `{"Nonce":1}`)
	if !errors.Is(err, ErrAckLost) || errors.Is(err, ErrStreamDown) {
		t.Fatalf("write error: %v", err)
	}
}

func mathAbs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
