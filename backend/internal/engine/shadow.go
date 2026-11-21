package engine

import (
	"context"
	"database/sql"
	"time"

	"poly-lighter.trade/bot/internal/store"
	"poly-lighter.trade/bot/internal/strategy"
)

func (e *Engine) shadowAsync(ts int64) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		_ = e.fillShadow(ctx, ts)
	}()
}

func (e *Engine) equity(ctx context.Context) (float64, error) {
	if e.HTTP == nil {
		if e.global.StartEquity > 0 {
			return e.global.StartEquity, nil
		}
		return 1000, nil
	}
	acc, err := e.HTTP.Account(ctx, e.Cfg.AccountIndex)
	if err != nil {
		return 0, err
	}
	return acc.Equity(), nil
}

func (e *Engine) btcPx(ctx context.Context) (float64, error) {
	if e.pxProbe != nil {
		return e.pxProbe(ctx)
	}
	if e.HTTP == nil {
		return e.BN.Close1s(ctx, "BTCUSDT", e.now().Unix())
	}
	return e.HTTP.LastPrice(ctx, e.Cfg.Symbol)
}

func (e *Engine) fillShadow(ctx context.Context, ts int64) error {
	if e.BN == nil || !e.Cfg.ShadowBinance1s {
		return nil
	}
	w, err := e.Store.GetWindow(ctx, ts)
	if err != nil {
		return err
	}
	px1, err1 := e.BN.Close1s(ctx, "BTCUSDT", ts+1)
	pxC, err2 := e.BN.Close5m(ctx, "BTCUSDT", ts)
	var px, ret sql.NullFloat64
	if err1 == nil && px1 > 0 {
		px = sql.NullFloat64{Float64: px1, Valid: true}
	}
	side := w.Side
	if (side == strategy.SideLong || side == strategy.SideShort) && px.Valid {
		stopPx := strategy.StopPx(px1, side, e.Cfg.StopPricePct)
		hit, _ := e.BN.StopHit1s(ctx, "BTCUSDT", ts+1, ts+300, side == strategy.SideLong, stopPx)
		var r float64
		switch {
		case hit && stopPx > 0:
			r = strategy.Sign(side) * (stopPx/px1 - 1)
			ret = sql.NullFloat64{Float64: r * 1e4, Valid: true}
		case err2 == nil && pxC > 0:
			r = strategy.Sign(side) * (pxC/px1 - 1)
			ret = sql.NullFloat64{Float64: r * 1e4, Valid: true}
		}
		if ret.Valid {
			var shadowEq float64
			e.mutateGlobal(ctx, func(g *store.GlobalState) {
				g.ShadowEquity *= (1 + r*e.Cfg.Leverage*e.Cfg.EquityFraction)
				shadowEq = g.ShadowEquity
			})
			_ = e.Store.InsertEquity(ctx, ts+300, "shadow", shadowEq)
		}
	}
	return e.Store.UpdateWindowShadow(ctx, ts, px, ret)
}
