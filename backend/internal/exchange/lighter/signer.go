package lighter

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"time"

	lighterclient "github.com/elliottech/lighter-go/client"
	"github.com/elliottech/lighter-go/types"
	"github.com/elliottech/lighter-go/types/txtypes"
)

const (
	authTokenTTL    = 7 * time.Hour
	authRefreshSkew = 10 * time.Minute
)

type Signer struct {
	Tx     *lighterclient.TxClient
	HTTP   *HTTPClient
	Stream *Stream
	Meta   map[string]MarketMeta
	Log    *slog.Logger

	tokMu      sync.Mutex
	token      string
	tokExp     time.Time
	txMu       sync.Mutex
	nextNonce  int64
	nonceReady bool
}

func NewSigner(httpClient *HTTPClient, privKey string, accountIndex int64, apiKeyIndex uint8, chainID uint32) (*Signer, error) {
	tx, err := lighterclient.NewTxClient(httpClient, privKey, accountIndex, apiKeyIndex, chainID)
	if err != nil {
		return nil, err
	}
	return &Signer{Tx: tx, HTTP: httpClient, Meta: map[string]MarketMeta{}}, nil
}

func (s *Signer) AuthToken() (string, error) {
	now := time.Now()
	s.tokMu.Lock()
	defer s.tokMu.Unlock()
	if s.token != "" && now.Add(authRefreshSkew).Before(s.tokExp) {
		return s.token, nil
	}
	deadline := now.Add(authTokenTTL)
	tok, err := s.Tx.GetAuthToken(deadline)
	if err != nil {
		return "", err
	}
	s.token = tok
	s.tokExp = deadline
	return tok, nil
}

type OrderResult struct {
	TxHash           string
	ClientOrderIndex int64
	Nonce            int64
	TxInfo           string
}

func (s *Signer) audit(msg string, args ...any) {
	if s == nil || s.Log == nil {
		return
	}
	s.Log.Info(msg, args...)
}

func (s *Signer) meta(symbol string) (MarketMeta, error) {
	m, ok := s.Meta[symbol]
	if !ok {
		return MarketMeta{}, fmt.Errorf("unknown market %s", symbol)
	}
	return m, nil
}

// PrepareNonce moves the Lighter nonce lookup off the time-critical order path.
// All transactions sent by this Signer share the same serialized nonce stream.
func (s *Signer) PrepareNonce(ctx context.Context) error {
	s.txMu.Lock()
	defer s.txMu.Unlock()
	n, err := s.HTTP.GetNextNonceContext(ctx, s.Tx.GetAccountIndex(), s.Tx.GetApiKeyIndex())
	if err != nil {
		s.nonceReady = false
		return err
	}
	s.nextNonce, s.nonceReady = n, true
	return nil
}

func (s *Signer) takeNonce(ctx context.Context) (int64, error) {
	if !s.nonceReady {
		n, err := s.HTTP.GetNextNonceContext(ctx, s.Tx.GetAccountIndex(), s.Tx.GetApiKeyIndex())
		if err != nil {
			return 0, err
		}
		s.nextNonce, s.nonceReady = n, true
	}
	n := s.nextNonce
	s.nextNonce++
	return n, nil
}

func (s *Signer) invalidateNonce() { s.nonceReady = false }

// noteSubmitErr rolls the local nonce back when the tx never left.
// An ambiguous ack keeps the incremented nonce. A venue reject forces a refetch.
func (s *Signer) noteSubmitErr(nonce int64, err error) {
	if err == nil {
		return
	}
	if IsPreConnectFail(err) {
		if s.nonceReady && s.nextNonce == nonce+1 {
			s.nextNonce = nonce
		}
		return
	}
	if IsAmbiguousSubmit(err) {
		return
	}
	s.invalidateNonce()
}

func notSubmitted(err error) error {
	if err == nil || errors.Is(err, ErrNotSubmitted) {
		return err
	}
	return fmt.Errorf("%w: %v", ErrNotSubmitted, err)
}

func (s *Signer) submit(ctx context.Context, txType int, txInfo string) (*SendTxResp, error) {
	if err := ctx.Err(); err != nil {
		return nil, notSubmitted(err)
	}
	if s.Stream != nil && s.Stream.Connected() {
		resp, err := s.Stream.SendTx(ctx, txType, txInfo)
		if err == nil || ctx.Err() != nil || !errors.Is(err, ErrStreamDown) {
			return resp, err
		}
		s.audit("order send fallback", "err", err.Error())
	}
	if s.HTTP == nil {
		return nil, ErrStreamDown
	}
	return s.HTTP.SendTx(ctx, txType, txInfo)
}

func (s *Signer) MarketIOC(ctx context.Context, symbol string, buy bool, qty, refPrice, slippage float64, clientIdx int64, reduceOnly bool) (*OrderResult, error) {
	s.txMu.Lock()
	defer s.txMu.Unlock()
	m, err := s.meta(symbol)
	if err != nil {
		return nil, err
	}
	if slippage <= 0 {
		slippage = 0.02
	}
	worst := refPrice * (1 + slippage)
	if !buy {
		worst = refPrice * (1 - slippage)
		if worst <= 0 {
			worst = refPrice * 0.5
		}
	}
	isAsk := uint8(0)
	if !buy {
		isAsk = 1
	}
	ro := uint8(0)
	if reduceOnly {
		ro = 1
	}
	amt := ScaleAmount(qty, m.SizeDecimals)
	if amt <= 0 {
		return nil, fmt.Errorf("qty scales to 0 (qty=%v decimals=%d)", qty, m.SizeDecimals)
	}
	px := ScalePrice(worst, m.PriceDecimals)
	if px < 1 {
		px = 1
	}
	req := &types.CreateOrderTxReq{
		MarketIndex:      int16(m.MarketID),
		ClientOrderIndex: clientIdx,
		BaseAmount:       amt,
		Price:            px,
		IsAsk:            isAsk,
		Type:             txtypes.MarketOrder,
		TimeInForce:      txtypes.ImmediateOrCancel,
		ReduceOnly:       ro,
		TriggerPrice:     txtypes.NilOrderTriggerPrice,
		OrderExpiry:      txtypes.NilOrderExpiry,
	}
	nonce, err := s.takeNonce(ctx)
	if err != nil {
		s.audit("order skip", "kind", "market_ioc", "symbol", symbol, "client_idx", clientIdx, "err", err.Error())
		return nil, notSubmitted(err)
	}
	info, err := s.Tx.GetCreateOrderTransaction(req, &types.TransactOpts{Nonce: &nonce})
	if err != nil {
		s.noteSubmitErr(nonce, ErrNotSubmitted)
		s.audit("order sign", "kind", "market_ioc", "nonce", nonce, "client_idx", clientIdx, "err", err.Error())
		return nil, notSubmitted(err)
	}
	js, err := info.GetTxInfo()
	if err != nil {
		s.noteSubmitErr(nonce, ErrNotSubmitted)
		s.audit("order sign", "kind", "market_ioc", "nonce", nonce, "client_idx", clientIdx, "err", err.Error())
		return nil, notSubmitted(err)
	}
	s.audit("order send", "kind", "market_ioc", "symbol", symbol, "buy", buy, "qty", qty, "ref", refPrice, "worst", worst, "reduce_only", reduceOnly, "client_idx", clientIdx, "nonce", nonce, "is_ask", isAsk)
	resp, err := s.submit(ctx, txtypes.TxTypeL2CreateOrder, js)
	if err != nil {
		s.noteSubmitErr(nonce, err)
		s.audit("order result", "kind", "market_ioc", "client_idx", clientIdx, "nonce", nonce, "err", err.Error())
		return nil, err
	}
	s.audit("order result", "kind", "market_ioc", "client_idx", clientIdx, "nonce", nonce, "hash", resp.TxHash)
	return &OrderResult{TxHash: resp.TxHash, ClientOrderIndex: clientIdx, Nonce: nonce, TxInfo: js}, nil
}

// UpdateLeverage sets isolated margin so notional ≈ equity * leverage is allowed by the venue.
// IMF tick is 10_000 = 100%; 3x → ~3333, 50x → 200.
func (s *Signer) UpdateLeverage(ctx context.Context, symbol string, leverage float64) error {
	s.txMu.Lock()
	defer s.txMu.Unlock()
	m, err := s.meta(symbol)
	if err != nil {
		return err
	}
	if leverage < 1 {
		leverage = 1
	}
	imf := uint16(math.Round(10000 / leverage))
	if imf < 1 {
		imf = 1
	}
	req := &types.UpdateLeverageTxReq{
		MarketIndex:           int16(m.MarketID),
		InitialMarginFraction: imf,
		MarginMode:            txtypes.IsolatedMargin,
	}
	nonce, err := s.takeNonce(ctx)
	if err != nil {
		return notSubmitted(err)
	}
	info, err := s.Tx.GetUpdateLeverageTransaction(req, &types.TransactOpts{Nonce: &nonce})
	if err != nil {
		s.noteSubmitErr(nonce, ErrNotSubmitted)
		return notSubmitted(err)
	}
	js, err := info.GetTxInfo()
	if err != nil {
		s.noteSubmitErr(nonce, ErrNotSubmitted)
		return notSubmitted(err)
	}
	s.audit("order send", "kind", "leverage", "symbol", symbol, "leverage", leverage, "imf", imf, "nonce", nonce)
	_, err = s.submit(ctx, txtypes.TxTypeL2UpdateLeverage, js)
	if err != nil {
		s.noteSubmitErr(nonce, err)
		s.audit("order result", "kind", "leverage", "nonce", nonce, "err", err.Error())
	} else {
		s.audit("order result", "kind", "leverage", "nonce", nonce)
	}
	return err
}

func (s *Signer) StopLoss(ctx context.Context, symbol string, buyPosition bool, qty, stop, slippage float64, clientIdx int64) (*OrderResult, error) {
	s.txMu.Lock()
	defer s.txMu.Unlock()
	m, err := s.meta(symbol)
	if err != nil {
		return nil, err
	}
	if slippage <= 0 {
		slippage = 0.02
	}
	isAsk := uint8(1)
	worst := stop * (1 - slippage)
	if !buyPosition {
		isAsk = 0
		worst = stop * (1 + slippage)
	}
	if worst <= 0 {
		worst = stop
	}
	amt := ScaleAmount(qty, m.SizeDecimals)
	if amt <= 0 {
		return nil, fmt.Errorf("SL qty scales to 0")
	}
	px := ScalePrice(worst, m.PriceDecimals)
	trig := ScalePrice(stop, m.PriceDecimals)
	if px < 1 {
		px = 1
	}
	if trig < 1 {
		trig = 1
	}
	req := &types.CreateOrderTxReq{
		MarketIndex:      int16(m.MarketID),
		ClientOrderIndex: clientIdx,
		BaseAmount:       amt,
		Price:            px,
		IsAsk:            isAsk,
		Type:             txtypes.StopLossOrder,
		TimeInForce:      txtypes.ImmediateOrCancel,
		ReduceOnly:       1,
		TriggerPrice:     trig,
		OrderExpiry:      time.Now().Add(28 * 24 * time.Hour).UnixMilli(),
	}
	nonce, err := s.takeNonce(ctx)
	if err != nil {
		s.audit("order skip", "kind", "stop_loss", "client_idx", clientIdx, "err", err.Error())
		return nil, notSubmitted(err)
	}
	info, err := s.Tx.GetCreateOrderTransaction(req, &types.TransactOpts{Nonce: &nonce})
	if err != nil {
		s.noteSubmitErr(nonce, ErrNotSubmitted)
		s.audit("order sign", "kind", "stop_loss", "nonce", nonce, "client_idx", clientIdx, "err", err.Error())
		return nil, notSubmitted(err)
	}
	js, err := info.GetTxInfo()
	if err != nil {
		s.noteSubmitErr(nonce, ErrNotSubmitted)
		s.audit("order sign", "kind", "stop_loss", "nonce", nonce, "client_idx", clientIdx, "err", err.Error())
		return nil, notSubmitted(err)
	}
	s.audit("order send", "kind", "stop_loss", "symbol", symbol, "buy_position", buyPosition, "qty", qty, "stop", stop, "worst", worst, "client_idx", clientIdx, "nonce", nonce, "is_ask", isAsk, "reduce_only", true)
	resp, err := s.submit(ctx, txtypes.TxTypeL2CreateOrder, js)
	if err != nil {
		s.noteSubmitErr(nonce, err)
		s.audit("order result", "kind", "stop_loss", "client_idx", clientIdx, "nonce", nonce, "err", err.Error())
		return nil, err
	}
	s.audit("order result", "kind", "stop_loss", "client_idx", clientIdx, "nonce", nonce, "hash", resp.TxHash)
	return &OrderResult{TxHash: resp.TxHash, ClientOrderIndex: clientIdx, Nonce: nonce, TxInfo: js}, nil
}

func (s *Signer) CancelAll(ctx context.Context) error {
	s.txMu.Lock()
	defer s.txMu.Unlock()
	nonce, err := s.takeNonce(ctx)
	if err != nil {
		s.audit("order skip", "kind", "cancel_all", "err", err.Error())
		return notSubmitted(err)
	}
	info, err := s.Tx.GetCancelAllOrdersTransaction(&types.CancelAllOrdersTxReq{
		TimeInForce: txtypes.ImmediateCancelAll,
		Time:        0,
	}, &types.TransactOpts{Nonce: &nonce})
	if err != nil {
		s.noteSubmitErr(nonce, ErrNotSubmitted)
		s.audit("order sign", "kind", "cancel_all", "nonce", nonce, "err", err.Error())
		return notSubmitted(err)
	}
	js, err := info.GetTxInfo()
	if err != nil {
		s.noteSubmitErr(nonce, ErrNotSubmitted)
		s.audit("order sign", "kind", "cancel_all", "nonce", nonce, "err", err.Error())
		return notSubmitted(err)
	}
	s.audit("order send", "kind", "cancel_all", "nonce", nonce)
	_, err = s.submit(ctx, txtypes.TxTypeL2CancelAllOrders, js)
	if err != nil {
		s.noteSubmitErr(nonce, err)
		s.audit("order result", "kind", "cancel_all", "nonce", nonce, "err", err.Error())
	} else {
		s.audit("order result", "kind", "cancel_all", "nonce", nonce)
	}
	return err
}
