package lighter

import (
	"context"
	"encoding/json"
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
}

func TestCachedIPUsedWhenLookupFails(t *testing.T) {
	rememberIP("lighter.test", "203.0.113.8")
	ip, ok := cachedIP("lighter.test")
	if !ok || ip != "203.0.113.8" {
		t.Fatalf("cache %q %v", ip, ok)
	}
}
