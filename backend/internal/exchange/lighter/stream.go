package lighter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	streamPingEvery   = 60 * time.Second
	streamReadIdle    = 90 * time.Second
	streamPriceMaxAge = 3 * time.Second
	streamFillCap     = 256
)

// ErrStreamDown means the socket is not up, so the caller may use REST.
// A frame that was already written is ErrAckLost, which is ambiguous and must not be retried.
var (
	ErrStreamDown = errors.New("lighter stream down")
	ErrAckLost    = errors.New("lighter stream ack lost")
)

// ErrNotSubmitted means no transaction was handed to the venue.
// A wrapped context deadline is still not an ambiguous submit.
var ErrNotSubmitted = errors.New("lighter tx not submitted")

// Stream is one Lighter websocket: sendtx, account fills, and last price.
// REST remains the path when the socket is down.
type Stream struct {
	URL          string
	AccountIndex int64
	MarketID     uint16
	Auth         func() (string, error)
	Log          *slog.Logger
	NetDial      func(ctx context.Context, network, addr string) (net.Conn, error)

	mu        sync.Mutex
	writeMu   sync.Mutex
	conn      *websocket.Conn
	connected bool
	price     float64
	priceAt   time.Time
	fills     map[int64]*fillAcc
	wait      map[string]chan streamSend
}

type fillAcc struct {
	tradeQuote float64
	tradeQty   float64
	tradeFee   float64
	trades     map[string]struct{}
	orderQuote float64
	orderQty   float64
	status     string
	terminal   bool
	touched    time.Time
}

type streamSend struct {
	resp *SendTxResp
	err  error
}

func NewStream(url string, accountIndex int64, marketID uint16, auth func() (string, error), log *slog.Logger) *Stream {
	if log == nil {
		log = slog.Default()
	}
	return &Stream{
		URL: url, AccountIndex: accountIndex, MarketID: marketID, Auth: auth, Log: log,
		fills: map[int64]*fillAcc{},
		wait:  map[string]chan streamSend{},
	}
}

func (s *Stream) Connected() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connected && s.conn != nil
}

// FreshPrice is the last trade from market_stats, if it is newer than maxAge.
func (s *Stream) FreshPrice(maxAge time.Duration) (float64, bool) {
	if s == nil {
		return 0, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.price <= 0 || s.priceAt.IsZero() || time.Since(s.priceAt) > maxAge {
		return 0, false
	}
	return s.price, true
}

// Fill returns the current size for a client order index, including a partial.
func (s *Stream) Fill(clientIdx int64) (px, qty, fee float64, ok bool) {
	px, qty, fee, _, ok = s.FillState(clientIdx)
	return
}

// FillState returns the accumulated fill and whether the order is terminal
// (filled or canceled). Trades are deduped by trade id. An order snapshot
// contributes size only when it is ahead of the tape.
func (s *Stream) FillState(clientIdx int64) (px, qty, fee float64, terminal, ok bool) {
	if s == nil || clientIdx == 0 {
		return 0, 0, 0, false, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.fills[clientIdx]
	if f == nil {
		return 0, 0, 0, false, false
	}
	return f.view()
}

func (f *fillAcc) view() (px, qty, fee float64, terminal, ok bool) {
	terminal = f.terminal
	tradesCover := f.orderQty <= 0 || f.tradeQty+1e-12 >= f.orderQty
	switch {
	case f.tradeQty > 0 && tradesCover:
		return f.tradeQuote / f.tradeQty, f.tradeQty, f.tradeFee, terminal, true
	case f.orderQty > 0 && f.orderQuote > 0:
		return f.orderQuote / f.orderQty, f.orderQty, 0, terminal, true
	default:
		return 0, 0, 0, terminal, false
	}
}

// SendTx writes jsonapi/sendtx on the open socket and waits for the matching id.
// ErrStreamDown means the frame was not written. Any error after a successful write is ambiguous.
func (s *Stream) SendTx(ctx context.Context, txType int, txInfo string) (*SendTxResp, error) {
	if s == nil || !s.Connected() {
		return nil, ErrStreamDown
	}
	id := strconv.FormatInt(time.Now().UnixNano(), 10)
	ch := make(chan streamSend, 1)
	s.mu.Lock()
	if s.wait == nil {
		s.wait = map[string]chan streamSend{}
	}
	s.wait[id] = ch
	conn := s.conn
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.wait, id)
		s.mu.Unlock()
	}()
	if conn == nil {
		return nil, ErrStreamDown
	}
	body := map[string]any{
		"type": "jsonapi/sendtx",
		"data": map[string]any{
			"id":      id,
			"tx_type": txType,
			"tx_info": txInfoValue(txInfo),
		},
	}
	s.writeMu.Lock()
	if err := ctx.Err(); err != nil {
		s.writeMu.Unlock()
		return nil, fmt.Errorf("%w: %v", ErrNotSubmitted, err)
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetWriteDeadline(dl)
	}
	err := conn.WriteJSON(body)
	if err != nil {
		_ = conn.Close()
		_ = conn.SetWriteDeadline(time.Time{})
		s.writeMu.Unlock()
		return nil, fmt.Errorf("%w: %v", ErrAckLost, err)
	}
	_ = conn.SetWriteDeadline(time.Time{})
	s.writeMu.Unlock()
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("%w: %v", ErrAckLost, ctx.Err())
	case res := <-ch:
		if res.err != nil {
			return res.resp, res.err
		}
		return res.resp, nil
	}
}

func txInfoValue(txInfo string) any {
	var v any
	if json.Unmarshal([]byte(txInfo), &v) == nil {
		return v
	}
	return txInfo
}

// Run keeps the socket up until ctx is cancelled. Reconnects resubscribe.
func (s *Stream) Run(ctx context.Context) {
	if s == nil || s.URL == "" {
		return
	}
	backoff := 500 * time.Millisecond
	for ctx.Err() == nil {
		started := time.Now()
		err := s.serve(ctx)
		s.drop(ErrAckLost)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			s.Log.Warn("lighter stream", "err", err.Error())
		}
		if time.Since(started) > 30*time.Second {
			backoff = 500 * time.Millisecond
		}
		t := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		if backoff < 10*time.Second {
			backoff *= 2
		}
	}
}

func (s *Stream) serve(ctx context.Context) error {
	dialer := websocket.Dialer{
		HandshakeTimeout: 8 * time.Second,
		NetDialContext:   DialContext,
	}
	if s.NetDial != nil {
		dialer.NetDialContext = s.NetDial
	}
	conn, _, err := dialer.DialContext(ctx, s.URL, nil)
	if err != nil {
		return err
	}
	defer conn.Close()
	// Fills stay across reconnects. The account_all_trades subscribe ack is an empty
	// snapshot; trade-id dedup absorbs a replay, and wiping here would drop an in-flight fill.
	s.mu.Lock()
	s.conn = conn
	s.connected = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if s.conn == conn {
			s.conn = nil
			s.connected = false
		}
		s.mu.Unlock()
	}()

	if err := s.subscribe(conn); err != nil {
		return err
	}
	done := make(chan struct{})
	defer close(done)
	go s.ping(ctx, conn, done)

	for ctx.Err() == nil {
		_ = conn.SetReadDeadline(time.Now().Add(streamReadIdle))
		_, data, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		s.handle(data)
	}
	return ctx.Err()
}

func (s *Stream) ping(ctx context.Context, conn *websocket.Conn, done <-chan struct{}) {
	t := time.NewTicker(streamPingEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-t.C:
			s.writeMu.Lock()
			err := conn.WriteJSON(map[string]string{"type": "ping"})
			s.writeMu.Unlock()
			if err != nil {
				_ = conn.Close()
				return
			}
		}
	}
}

func (s *Stream) subscribe(conn *websocket.Conn) error {
	msgs := []any{
		map[string]any{"type": "subscribe", "channel": fmt.Sprintf("market_stats/%d", s.MarketID)},
	}
	if s.AccountIndex != 0 {
		auth := ""
		if s.Auth != nil {
			tok, err := s.Auth()
			if err != nil {
				s.Log.Warn("lighter stream auth", "err", err.Error())
			} else {
				auth = tok
			}
		}
		if auth != "" {
			acct := strconv.FormatInt(s.AccountIndex, 10)
			mkt := strconv.FormatUint(uint64(s.MarketID), 10)
			msgs = append(msgs,
				map[string]any{"type": "subscribe", "channel": "account_orders/" + mkt + "/" + acct, "auth": auth},
				map[string]any{"type": "subscribe", "channel": "account_all_trades/" + acct, "auth": auth},
			)
		}
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	for _, m := range msgs {
		if err := conn.WriteJSON(m); err != nil {
			return err
		}
	}
	return nil
}

func (s *Stream) drop(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.connected = false
	s.conn = nil
	for id, ch := range s.wait {
		select {
		case ch <- streamSend{err: err}:
		default:
		}
		delete(s.wait, id)
	}
}

type streamMsg struct {
	Type        string          `json:"type"`
	ID          string          `json:"id"`
	Code        int             `json:"code"`
	Message     string          `json:"message"`
	TxHash      string          `json:"tx_hash"`
	Error       *streamErr      `json:"error"`
	Data        json.RawMessage `json:"data"`
	MarketStats json.RawMessage `json:"market_stats"`
	Orders      json.RawMessage `json:"orders"`
	Trades      json.RawMessage `json:"trades"`
}

type streamErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (s *Stream) handle(data []byte) {
	var msg streamMsg
	if json.Unmarshal(data, &msg) != nil {
		return
	}
	if msg.Type == "ping" {
		s.writePong()
		return
	}
	if msg.Type == "pong" || msg.Type == "connected" {
		return
	}
	if msg.Error != nil && msg.Type != "jsonapi/sendtx" && msg.TxHash == "" && msg.Code == 0 && msg.ID != "" {
		s.deliver(msg.ID, nil, fmt.Errorf("sendTx code=%d msg=%s", msg.Error.Code, msg.Error.Message))
	} else if msg.Type == "jsonapi/sendtx" || msg.TxHash != "" || msg.Code != 0 {
		if msg.Error != nil {
			if msg.Code == 0 {
				msg.Code = msg.Error.Code
			}
			if msg.Message == "" {
				msg.Message = msg.Error.Message
			}
		}
		s.deliverSend(msg)
	}
	s.ingestPrice(msg.MarketStats)
	s.ingestOrders(msg.Orders)
	s.ingestTrades(msg.Trades)
}

func (s *Stream) writePong() {
	s.mu.Lock()
	conn := s.conn
	s.mu.Unlock()
	if conn == nil {
		return
	}
	s.writeMu.Lock()
	_ = conn.WriteJSON(map[string]string{"type": "pong"})
	s.writeMu.Unlock()
}

func (s *Stream) deliverSend(msg streamMsg) {
	id := msg.ID
	resp := &SendTxResp{Code: msg.Code, Message: msg.Message, TxHash: msg.TxHash}
	if len(msg.Data) > 0 {
		var nested struct {
			ID      string `json:"id"`
			Code    int    `json:"code"`
			Message string `json:"message"`
			TxHash  string `json:"tx_hash"`
		}
		if json.Unmarshal(msg.Data, &nested) == nil {
			if id == "" {
				id = nested.ID
			}
			if resp.Code == 0 {
				resp.Code = nested.Code
			}
			if resp.TxHash == "" {
				resp.TxHash = nested.TxHash
			}
			if resp.Message == "" {
				resp.Message = nested.Message
			}
		}
	}
	if id == "" {
		return
	}
	if resp.Code != 0 && resp.Code != 200 {
		s.deliver(id, resp, fmt.Errorf("sendTx code=%d msg=%s hash=%s", resp.Code, resp.Message, resp.TxHash))
		return
	}
	if resp.TxHash == "" && resp.Code == 0 {
		return
	}
	s.deliver(id, resp, nil)
}

func (s *Stream) deliver(id string, resp *SendTxResp, err error) {
	if id == "" {
		return
	}
	s.mu.Lock()
	ch := s.wait[id]
	s.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- streamSend{resp: resp, err: err}:
	default:
	}
}

func (s *Stream) ingestPrice(raw json.RawMessage) {
	if len(raw) == 0 || string(raw) == "null" {
		return
	}
	var one struct {
		Last string `json:"last_trade_price"`
		Mark string `json:"mark_price"`
	}
	if json.Unmarshal(raw, &one) == nil && (one.Last != "" || one.Mark != "") {
		s.setPrice(one.Last, one.Mark)
		return
	}
	var many map[string]struct {
		Last string `json:"last_trade_price"`
		Mark string `json:"mark_price"`
	}
	if json.Unmarshal(raw, &many) != nil {
		return
	}
	key := strconv.FormatUint(uint64(s.MarketID), 10)
	if row, ok := many[key]; ok {
		s.setPrice(row.Last, row.Mark)
	}
}

func (s *Stream) setPrice(last, mark string) {
	px := parseFlex(last)
	if px <= 0 {
		px = parseFlex(mark)
	}
	if px <= 0 {
		return
	}
	s.mu.Lock()
	s.price = px
	s.priceAt = time.Now()
	s.mu.Unlock()
}

func (s *Stream) ingestOrders(raw json.RawMessage) {
	if len(raw) == 0 || string(raw) == "null" {
		return
	}
	var byMarket map[string][]streamOrder
	if json.Unmarshal(raw, &byMarket) != nil {
		return
	}
	for _, orders := range byMarket {
		for _, o := range orders {
			idx := int64(o.ClientIdx)
			if idx == 0 {
				idx = parseFlexInt(o.ClientID)
			}
			base := parseFlex(string(o.FilledBase))
			quote := parseFlex(string(o.FilledQuote))
			if idx == 0 {
				continue
			}
			if base <= 0 || quote <= 0 {
				if orderTerminal(o.Status) {
					s.noteOrder(idx, 0, 0, o.Status)
				}
				continue
			}
			s.noteOrder(idx, quote/base, base, o.Status)
		}
	}
}

func (s *Stream) ingestTrades(raw json.RawMessage) {
	if len(raw) == 0 || string(raw) == "null" {
		return
	}
	var list []streamTrade
	if json.Unmarshal(raw, &list) == nil {
		s.addTrades(list)
		return
	}
	var byMarket map[string][]streamTrade
	if json.Unmarshal(raw, &byMarket) != nil {
		return
	}
	for _, trades := range byMarket {
		s.addTrades(trades)
	}
}

func (s *Stream) addTrades(trades []streamTrade) {
	for _, t := range trades {
		px := float64(t.Price)
		qty := float64(t.Size)
		if px <= 0 || qty <= 0 {
			continue
		}
		notional := float64(t.USD)
		if notional <= 0 {
			notional = px * qty
		}
		id := t.dedupID()
		if int64(t.AskAccount) == s.AccountIndex && int64(t.AskClient) != 0 {
			fee := sideFeeUSD(true, t.IsMakerAsk, float64(t.TakerFee), float64(t.MakerFee), notional)
			s.addTrade(int64(t.AskClient), id, px, qty, fee)
		}
		if int64(t.BidAccount) == s.AccountIndex && int64(t.BidClient) != 0 && t.BidClient != t.AskClient {
			fee := sideFeeUSD(false, t.IsMakerAsk, float64(t.TakerFee), float64(t.MakerFee), notional)
			s.addTrade(int64(t.BidClient), id, px, qty, fee)
		}
	}
}

func (s *Stream) addTrade(idx int64, tradeID string, px, qty, fee float64) {
	if idx == 0 || qty <= 0 || px <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.ensureLocked(idx)
	if tradeID != "" {
		if _, ok := f.trades[tradeID]; ok {
			f.touched = time.Now()
			return
		}
		f.trades[tradeID] = struct{}{}
	}
	f.tradeQuote += px * qty
	f.tradeQty += qty
	f.tradeFee += fee
	f.touched = time.Now()
}

func (s *Stream) noteOrder(idx int64, px, qty float64, status string) {
	if idx == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.ensureLocked(idx)
	term := orderTerminal(status)
	if qty > 0 && px > 0 && (qty+1e-12 >= f.orderQty || !f.terminal) {
		f.orderQty = qty
		f.orderQuote = px * qty
	}
	if status != "" && (term || !f.terminal) {
		f.status = status
		f.terminal = term
	}
	f.touched = time.Now()
}

func (s *Stream) ensureLocked(idx int64) *fillAcc {
	if s.fills == nil {
		s.fills = map[int64]*fillAcc{}
	}
	if f := s.fills[idx]; f != nil {
		return f
	}
	if len(s.fills) >= streamFillCap {
		var oldK int64
		var oldAt time.Time
		first := true
		for k, v := range s.fills {
			if first || v.touched.Before(oldAt) {
				first = false
				oldK, oldAt = k, v.touched
			}
		}
		if !first {
			delete(s.fills, oldK)
		}
	}
	f := &fillAcc{trades: map[string]struct{}{}}
	s.fills[idx] = f
	return f
}

func orderTerminal(status string) bool {
	switch status {
	case "filled", "canceled":
		return true
	default:
		return strings.HasPrefix(status, "canceled-")
	}
}

type streamOrder struct {
	ClientIdx   flexInt  `json:"client_order_index"`
	ClientID    string   `json:"client_order_id"`
	FilledBase  flexText `json:"filled_base_amount"`
	FilledQuote flexText `json:"filled_quote_amount"`
	Status      string   `json:"status"`
}

type streamTrade struct {
	TradeID    flexText  `json:"trade_id"`
	TradeIDStr string    `json:"trade_id_str"`
	Price      flexFloat `json:"price"`
	Size       flexFloat `json:"size"`
	USD        flexFloat `json:"usd_amount"`
	AskClient  flexInt   `json:"ask_client_id"`
	BidClient  flexInt   `json:"bid_client_id"`
	AskAccount flexInt   `json:"ask_account_id"`
	BidAccount flexInt   `json:"bid_account_id"`
	IsMakerAsk bool      `json:"is_maker_ask"`
	TakerFee   flexFloat `json:"taker_fee"`
	MakerFee   flexFloat `json:"maker_fee"`
}

func (t streamTrade) dedupID() string {
	if id := strings.TrimSpace(t.TradeIDStr); id != "" {
		return id
	}
	id := strings.TrimSpace(string(t.TradeID))
	if id == "" || id == "0" {
		return ""
	}
	return id
}

type flexFloat float64

func (f *flexFloat) UnmarshalJSON(b []byte) error {
	*f = 0
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		if s == "" {
			return nil
		}
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return err
		}
		*f = flexFloat(v)
		return nil
	}
	v, err := strconv.ParseFloat(string(b), 64)
	if err != nil {
		return err
	}
	*f = flexFloat(v)
	return nil
}

type flexInt int64

func (f *flexInt) UnmarshalJSON(b []byte) error {
	var n flexFloat
	if err := n.UnmarshalJSON(b); err != nil {
		return err
	}
	*f = flexInt(n)
	return nil
}

type flexText string

func (f *flexText) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		*f = ""
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*f = flexText(s)
		return nil
	}
	*f = flexText(b)
	return nil
}

func parseFlex(s string) float64 {
	if s == "" {
		return 0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return f
}

func parseFlexInt(s string) int64 {
	if s == "" {
		return 0
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return n
}
