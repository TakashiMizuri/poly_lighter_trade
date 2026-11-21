package engine

import (
	"log/slog"
	"sync"
	"time"

	"poly-lighter.trade/bot/internal/binance"
	"poly-lighter.trade/bot/internal/config"
	"poly-lighter.trade/bot/internal/exchange/lighter"
	"poly-lighter.trade/bot/internal/flog"
	"poly-lighter.trade/bot/internal/notify"
	"poly-lighter.trade/bot/internal/polymarket"
	"poly-lighter.trade/bot/internal/store"
	"poly-lighter.trade/bot/internal/strategy"
)

const (
	StateIdle       = "IDLE"
	StateArming     = "ARMING"
	StateDecide     = "DECIDE"
	StateInPosition = "IN_POSITION"
	StateFlattening = "FLATTENING"
	StateHalt       = "HALT"
)

type livePos struct {
	WindowTS      int64
	Side          string
	Qty           float64
	Notional      float64
	EntryPx       float64
	EntryTS       float64
	EquityBefore  float64
	ClientIdx     int64
	StopClientIdx int64
	StopHash      string
	StopPx        float64
	Fees          float64
	FillLagMS     float64
	ArmedAt       time.Time
	EntryHash     string
	SeenAcctOpen  bool
	ExitClientIdx int64
	ExitHash      string
}

type Engine struct {
	Cfg     *config.Config
	Store   *store.Store
	HTTP    *lighter.HTTPClient
	Signer  *lighter.Signer
	Markets map[string]lighter.MarketMeta
	PM      *polymarket.Client
	BN      *binance.Client
	Notify  notify.Notifier
	Flog    *flog.Logger
	Log     *slog.Logger

	mu          sync.Mutex
	clockSyncMu sync.Mutex
	state       string
	offset      time.Duration
	offsetOK    bool
	offsetAt    time.Time
	clockRTT    time.Duration
	pos         *livePos
	lastQuote   strategy.Quote
	lastSkip    string
	lastWindow  int64
	armedFor    int64
	armedMarket polymarket.Market
	wsPM        string
	started     time.Time
	global      store.GlobalState
	bootWindow  int64
	consumedTS  int64
	readyEq     float64
	readyPx     float64
	readyAt     time.Time
	readyOK     bool
	tr          *winTrace
	lastTimings *store.Timings
}

func New(cfg *config.Config, st *store.Store, httpClient *lighter.HTTPClient, signer *lighter.Signer, markets map[string]lighter.MarketMeta, pm *polymarket.Client, bn *binance.Client, n notify.Notifier, log *slog.Logger) *Engine {
	if n == nil {
		n = notify.Nop{}
	}
	if log == nil {
		log = slog.Default()
	}
	return &Engine{
		Cfg: cfg, Store: st, HTTP: httpClient, Signer: signer, Markets: markets,
		PM: pm, BN: bn, Notify: n, Log: log, state: StateIdle, started: time.Now(),
	}
}

func (e *Engine) now() time.Time {
	// The VPS system clock is disciplined by chrony/NTP. Binance time is
	// telemetry only and must not become a runtime dependency for trading.
	return time.Now().UTC()
}

func (e *Engine) entryClock() time.Time {
	return e.now()
}

func (e *Engine) entryDeadline(ts int64) time.Time {
	return time.Unix(ts, 0).UTC().Add(e.Cfg.EntryMaxLag)
}

func (e *Engine) entryElapsed(ts int64) (mid, lower, upper time.Duration) {
	mid = e.now().Sub(time.Unix(ts, 0).UTC())
	return mid, mid, mid
}

func (e *Engine) correctedTime(local time.Time) time.Time {
	return local.UTC()
}

func (e *Engine) unix() float64 {
	return float64(e.now().UnixNano()) / 1e9
}
