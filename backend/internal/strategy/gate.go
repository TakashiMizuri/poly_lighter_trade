package strategy

import "time"

const (
	SideLong  = "long"
	SideShort = "short"
	SideSkip  = "skip"
)

type Quote struct {
	BidUp      float64
	AskUp      float64
	BidDown    float64
	AskDown    float64
	HasDown    bool
	Recv       time.Time
	Source     string // ws_clob | rest_clob | gamma
	TokenUp    string
	TokenDown  string
}

func (q Quote) MidUp() float64 {
	if q.BidUp <= 0 || q.AskUp <= 0 {
		return 0
	}
	return (q.BidUp + q.AskUp) / 2
}

func (q Quote) Spread() float64 {
	return q.AskUp - q.BidUp
}

func QuoteOK(q Quote, maxSpread, maxAgeS float64, now time.Time) (bool, string) {
	if q.BidUp <= 0 || q.AskUp <= 0 || q.BidUp >= 1 || q.AskUp >= 1 {
		return false, "bad_quote"
	}
	if q.AskUp < q.BidUp {
		return false, "bad_quote"
	}
	if q.Spread() > maxSpread {
		return false, "bad_quote"
	}
	if q.BidUp < 0.05 || q.AskUp > 0.95 {
		return false, "bad_quote"
	}
	if now.Sub(q.Recv) > time.Duration(maxAgeS*float64(time.Second)) {
		return false, "stale_quote"
	}
	if q.HasDown && q.AskDown > 0 {
		// bid_up + ask_down should sit near 1; >0.08 is incoherent books
		if abs((q.BidUp+q.AskDown)-1) > 0.08 {
			return false, "quote_incoherent"
		}
	}
	return true, ""
}

func DecideSide(pUp, threshold float64) (side, skipReason string, bothGE bool) {
	pDown := 1 - pUp
	favLong := pUp >= threshold
	favShort := pDown >= threshold
	if favLong && favShort {
		return SideSkip, "both_ge_th", true
	}
	if favLong {
		return SideLong, "", false
	}
	if favShort {
		return SideShort, "", false
	}
	return SideSkip, "below_th", false
}

func Sign(side string) float64 {
	switch side {
	case SideLong:
		return 1
	case SideShort:
		return -1
	default:
		return 0
	}
}

func FloorLot(qty float64, decimals int) float64 {
	if decimals < 0 {
		decimals = 0
	}
	p := 1.0
	for i := 0; i < decimals; i++ {
		p *= 10
	}
	return float64(int64(qty*p)) / p
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
