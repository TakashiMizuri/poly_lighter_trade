package strategy

// StopPx is a price stop off the BTC fill. Do NOT multiply pct by leverage.
func StopPx(fill float64, side string, pct float64) float64 {
	if fill <= 0 || pct <= 0 {
		return 0
	}
	if side == SideLong {
		return fill * (1 - pct)
	}
	if side == SideShort {
		return fill * (1 + pct)
	}
	return 0
}

func StopHit(last float64, side string, stopPx float64) bool {
	if last <= 0 || stopPx <= 0 {
		return false
	}
	switch side {
	case SideLong:
		return last <= stopPx
	case SideShort:
		return last >= stopPx
	default:
		return false
	}
}

// SLSlipBps is how much worse the stop fill is than −pct of price (positive = worse).
func SLSlipBps(entry, exit float64, side string, stopPct float64) float64 {
	if entry <= 0 || exit <= 0 || stopPct <= 0 {
		return 0
	}
	actual := Sign(side) * (exit/entry - 1) * 1e4
	expected := -stopPct * 1e4
	return expected - actual
}
