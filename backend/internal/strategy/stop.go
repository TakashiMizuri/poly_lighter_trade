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

// StopLikeFill is a stop fill even if Lighter prints a child market without our
// stop client id. Last/mark can sit a couple of points on the safe side of the trigger.
func StopLikeFill(px float64, side string, stopPx float64) bool {
	if StopHit(px, side, stopPx) {
		return true
	}
	if px <= 0 || stopPx <= 0 {
		return false
	}
	band := stopPx * 0.0002 // 2 bps
	switch side {
	case SideLong:
		return px <= stopPx+band
	case SideShort:
		return px >= stopPx-band
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
