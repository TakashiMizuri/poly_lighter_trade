package lighter

import (
	"context"
	"fmt"
	"math"
	"time"
)

// ConfirmFillOpts controls fill reconciliation after an order submit (entry or exit).
type ConfirmFillOpts struct {
	AccountIndex int64
	MarketID     uint16
	Symbol       string
	ClientIdx    int64
	TxHash       string
	WantQty      float64 // entry: requested size; 0 disables oversize check
	FallbackPx   float64
	SizeDecimals int
	Budget       time.Duration
	// UsePosition enables account-position fallback when trades lag (entry path).
	UsePosition bool
}

// ConfirmFillResult is the reconciled fill (or empty qty if unconfirmed).
type ConfirmFillResult struct {
	Px, Qty, Fee, Unix float64
	Oversize           error
}

// ConfirmFill waits for a fill via the stream, the trade tape, and optionally the account position.
// A partial print is not a fill: the stream must be terminal, or size must reach WantQty.
// Qty==0 means unconfirmed. At the deadline the best partial is returned.
func (c *HTTPClient) ConfirmFill(ctx context.Context, opts ConfirmFillOpts) ConfirmFillResult {
	out := ConfirmFillResult{Px: opts.FallbackPx}
	if c == nil {
		return out
	}
	budget := opts.Budget
	if budget <= 0 {
		budget = 800 * time.Millisecond
	}
	deadline := time.Now().Add(budget)
	lot := math.Pow10(-opts.SizeDecimals)
	if lot <= 0 || math.IsInf(lot, 0) || math.IsNaN(lot) {
		lot = 1e-8
	}
	for {
		var streamPx, streamQty, streamFee float64
		streamTerm := false
		if c.Stream != nil {
			if px, qty, fee, term, ok := c.Stream.FillState(opts.ClientIdx); ok && qty > 0 && px > 0 {
				streamPx, streamQty, streamFee, streamTerm = px, qty, fee, term
			}
		}
		settled := fillSettled(streamQty, streamTerm, opts.WantQty, lot)
		if settled && streamFee > 0 && !opts.UsePosition {
			return adoptFill(out, streamPx, streamQty, streamFee, 0)
		}

		if !(settled && streamFee > 0) {
			fills, err := c.AccountTradesUrgent(ctx, opts.AccountIndex, opts.MarketID, 100)
			if err == nil {
				if p, q, f, u := VWAP(FillsMatching(fills, opts.ClientIdx, opts.TxHash)); q > 0 {
					out = adoptFill(out, p, q, f, u)
				}
			}
		}
		out = adoptFill(out, streamPx, streamQty, streamFee, 0)

		if opts.UsePosition {
			acc, aerr := c.AccountUrgent(ctx, opts.AccountIndex)
			if aerr == nil {
				if p, open := acc.Position(opts.Symbol, opts.MarketID); open && p.Size > 0 {
					if opts.WantQty > 0 && p.Size > opts.WantQty+lot {
						out.Oversize = fmt.Errorf("position %.8f exceeded requested %.8f", p.Size, opts.WantQty)
						out = adoptFill(out, p.AvgEntry, p.Size, 0, 0)
						if out.Qty <= 0 {
							out.Qty = p.Size
						}
						if p.AvgEntry > 0 {
							out.Px = p.AvgEntry
						}
						return out
					}
					px := p.AvgEntry
					if px <= 0 {
						px = out.Px
					}
					out = adoptFill(out, px, p.Size, 0, 0)
				}
			}
		}

		if fillSettled(out.Qty, streamTerm, opts.WantQty, lot) && out.Qty > 0 && out.Px > 0 {
			return out
		}
		if !time.Now().Before(deadline) {
			return out
		}
		select {
		case <-ctx.Done():
			return out
		case <-time.After(80 * time.Millisecond):
		}
	}
}

// fillSettled reports whether qty is safe to book: the order is done, or it already
// covers the requested size. WantQty <= 0 never settles on size alone.
func fillSettled(qty float64, terminal bool, want, lot float64) bool {
	if terminal && qty > 0 {
		return true
	}
	return want > 0 && qty+lot >= want
}

// adoptFill keeps the larger size. Equal size prefers a timestamp and a non-zero fee.
func adoptFill(out ConfirmFillResult, px, qty, fee, unix float64) ConfirmFillResult {
	if qty <= 0 || px <= 0 {
		return out
	}
	if qty > out.Qty+1e-12 {
		if fee <= 0 {
			fee = out.Fee
		}
		if unix <= 0 {
			unix = out.Unix
		}
		out.Px, out.Qty, out.Fee, out.Unix = px, qty, fee, unix
		return out
	}
	if out.Unix <= 0 && unix > 0 {
		out.Unix = unix
	}
	if out.Fee <= 0 && fee > 0 {
		out.Fee = fee
		if unix > 0 {
			out.Px = px
			out.Unix = unix
		}
	}
	return out
}

// WaitFillFor is a trades-only confirm helper kept for callers that do not need
// position fallback. Prefer ConfirmFill for entry and exit reconcile paths.
func (c *HTTPClient) WaitFillFor(ctx context.Context, accountIndex int64, marketID uint16, clientIdx int64, txHash string, fallback float64, budget time.Duration) (px, qty, fee, fillUnix float64) {
	res := c.ConfirmFill(ctx, ConfirmFillOpts{
		AccountIndex: accountIndex,
		MarketID:     marketID,
		ClientIdx:    clientIdx,
		TxHash:       txHash,
		FallbackPx:   fallback,
		Budget:       budget,
		UsePosition:  false,
	})
	return res.Px, res.Qty, res.Fee, res.Unix
}
