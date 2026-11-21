package store

import "math"

// Forecast projects equity forward from observed trade rate and mean return.
// After a wipe it is noisy; confidence rises with n_trades and sample span.
type Forecast struct {
	Ready bool `json:"ready"`

	EquityNow    float64 `json:"equity_now"`
	MeanPnLUSD   float64 `json:"mean_pnl_usd"`
	MeanBps      float64 `json:"mean_bps"`
	WR           float64 `json:"wr"`
	TradesPerDay float64 `json:"trades_per_day"`
	TradeRate    float64 `json:"trade_rate"` // trades / windows
	NTrades      int     `json:"n_trades"`
	NWindows     int     `json:"n_windows"`
	SampleHours  float64 `json:"sample_hours"`
	SampleFromTS int64   `json:"sample_from_ts"`
	SampleToTS   int64   `json:"sample_to_ts"`

	Equity1W float64 `json:"equity_1w"`
	Equity1M float64 `json:"equity_1m"`
	Equity1Y float64 `json:"equity_1y"`
	Ret1WPct float64 `json:"ret_1w_pct"`
	Ret1MPct float64 `json:"ret_1m_pct"`
	Ret1YPct float64 `json:"ret_1y_pct"`

	// low | medium | high
	Confidence      string  `json:"confidence"`
	ConfidenceScore float64 `json:"confidence_score"` // 0..1
	Note            string  `json:"note"`
}

// BuildForecast compounds current equity by expected trades over horizons.
// Model: E_T = E_0 * (1 + mean_bps/1e4) ^ (trades_per_day * days)
// Frequency uses calendar span of stored windows (not wall uptime alone).
func BuildForecast(equity float64, nTrades, nWindows, wins int, meanBps, meanPnL float64, sampleFrom, sampleTo int64) Forecast {
	f := Forecast{
		EquityNow:    equity,
		MeanBps:      meanBps,
		MeanPnLUSD:   meanPnL,
		NTrades:      nTrades,
		NWindows:     nWindows,
		SampleFromTS: sampleFrom,
		SampleToTS:   sampleTo,
	}
	if nTrades > 0 {
		f.WR = float64(wins) / float64(nTrades)
	}
	if nWindows > 0 {
		f.TradeRate = float64(nTrades) / float64(nWindows)
	}

	spanSec := float64(0)
	if sampleTo > sampleFrom {
		// Include the last window's 5m duration.
		spanSec = float64(sampleTo-sampleFrom) + 300
	} else if nWindows > 0 {
		spanSec = float64(nWindows) * 300
	}
	f.SampleHours = spanSec / 3600
	if spanSec > 0 {
		f.TradesPerDay = float64(nTrades) / (spanSec / 86400)
	}

	f.ConfidenceScore, f.Confidence, f.Note = forecastConfidence(nTrades, f.SampleHours)

	if equity <= 0 || nTrades <= 0 || f.TradesPerDay <= 0 {
		f.Ready = false
		if f.Note == "" {
			f.Note = "мало сделок для прогноза — копите окна"
		}
		f.Equity1W, f.Equity1M, f.Equity1Y = equity, equity, equity
		return f
	}

	f.Ready = true
	mu := meanBps / 1e4
	f.Equity1W = projectEquity(equity, mu, f.TradesPerDay*7)
	f.Equity1M = projectEquity(equity, mu, f.TradesPerDay*30)
	f.Equity1Y = projectEquity(equity, mu, f.TradesPerDay*365)
	f.Ret1WPct = (f.Equity1W/equity - 1) * 100
	f.Ret1MPct = (f.Equity1M/equity - 1) * 100
	f.Ret1YPct = (f.Equity1Y/equity - 1) * 100
	return f
}

func projectEquity(equity, muPerTrade, expectedTrades float64) float64 {
	if equity <= 0 || expectedTrades <= 0 {
		return equity
	}
	// Guard pathological compounding from tiny samples with huge |mean|.
	if muPerTrade <= -0.99 {
		muPerTrade = -0.99
	}
	if muPerTrade > 0.05 {
		muPerTrade = 0.05 // 50 bps/trade cap for display sanity
	}
	out := equity * math.Pow(1+muPerTrade, expectedTrades)
	if math.IsNaN(out) || math.IsInf(out, 0) {
		return equity
	}
	if out < 0 {
		return 0
	}
	return out
}

func forecastConfidence(nTrades int, sampleHours float64) (score float64, level, note string) {
	// n_trades dominates; sample span softens short bursts.
	nScore := 0.0
	switch {
	case nTrades >= 200:
		nScore = 1
	case nTrades >= 80:
		nScore = 0.75
	case nTrades >= 30:
		nScore = 0.5
	case nTrades >= 10:
		nScore = 0.3
	case nTrades >= 3:
		nScore = 0.15
	default:
		nScore = 0.05
	}
	hScore := 0.0
	switch {
	case sampleHours >= 24*14:
		hScore = 1
	case sampleHours >= 24*3:
		hScore = 0.7
	case sampleHours >= 24:
		hScore = 0.45
	case sampleHours >= 6:
		hScore = 0.25
	default:
		hScore = 0.1
	}
	score = 0.7*nScore + 0.3*hScore
	switch {
	case score >= 0.7:
		level = "high"
		note = "выборка уже приличная — ориентир, не гарантия"
	case score >= 0.35:
		level = "medium"
		note = "средняя надёжность — после сброса ещё шумит"
	default:
		level = "low"
		note = "мало данных — прогноз грубый, точнее по мере сделок"
	}
	return score, level, note
}
