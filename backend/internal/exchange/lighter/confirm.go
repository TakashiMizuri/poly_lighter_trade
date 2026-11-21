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

// ConfirmFill waits for a fill via trades and optionally account position.
// It never treats a missing fill as success: Qty==0 means unconfirmed.
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
	var tradesErr, acctErr error
	for {
		fills, err := c.AccountTradesUrgent(ctx, opts.AccountIndex, opts.MarketID, 100)
		tradesErr = err
		if err == nil {
			if p, q, f, u := VWAP(FillsMatching(fills, opts.ClientIdx, opts.TxHash)); q > 0 {
				out.Px, out.Qty, out.Fee, out.Unix = p, q, f, u
			}
		}
		if opts.UsePosition {
			acc, aerr := c.AccountUrgent(ctx, opts.AccountIndex)
			acctErr = aerr
			if aerr == nil {
				if p, open := acc.Position(opts.Symbol, opts.MarketID); open && p.Size > 0 {
					if opts.WantQty > 0 && p.Size > opts.WantQty+lot {
						out.Oversize = fmt.Errorf("position %.8f exceeded requested %.8f", p.Size, opts.WantQty)
						if out.Qty <= 0 {
							out.Qty = p.Size
						}
						if p.AvgEntry > 0 {
							out.Px = p.AvgEntry
						}
						return out
					}
					if out.Qty <= 0 || p.Size > out.Qty {
						out.Qty = p.Size
					}
					if p.AvgEntry > 0 {
						out.Px = p.AvgEntry
					}
				}
			}
		}
		if out.Qty > 0 && out.Px > 0 {
			return out
		}
		if !time.Now().Before(deadline) {
			_ = tradesErr
			_ = acctErr
			return out
		}
		select {
		case <-ctx.Done():
			return out
		case <-time.After(80 * time.Millisecond):
		}
	}
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
