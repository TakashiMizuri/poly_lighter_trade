package config

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"
)

type Network string

const (
	NetworkTestnet Network = "testnet"
	NetworkMainnet Network = "mainnet"
)

type Config struct {
	Profile string

	Network       Network
	BaseURL       string
	WSURL         string
	ChainID       uint32
	APIPrivateKey string
	AccountIndex  int64
	APIKeyIndex   uint8
	L1Address     string
	Symbol        string

	Threshold       float64
	MaxSpread       float64
	PrewarmSeconds  float64
	EntryMaxLag     time.Duration
	ExitEarly       time.Duration
	Leverage        float64
	EquityFraction  float64
	MarketSlippage  float64
	StopPricePct    float64
	ShadowBinance1s bool

	HaltExitLateStreak    int
	HaltDDpct             float64
	HaltPositionMax       time.Duration
	HaltStopMissingStreak int

	KillSwitch bool
	DryRun     bool

	SQLitePath        string
	HTTPAddr          string
	DashboardPassword string
	CookieSecure      bool
	LogDir            string
	InstanceID        string
	InstanceName      string

	TelegramToken     string
	TelegramChatIDs   []int64
	TelegramHeartbeat time.Duration

	PMSeriesSlug string
	GammaURL     string
	CLOBURL      string
	CLOBWSURL    string
	BinanceURL   string
}

func Load() (*Config, error) {
	net := Network(strings.ToLower(getenv("LIGHTER_NETWORK", "testnet")))
	cfg := &Config{
		Profile:               getenv("POLY_LIGHTER_PROFILE", "POLY_LIGHTER_1"),
		Network:               net,
		APIPrivateKey:         strings.TrimSpace(os.Getenv("LIGHTER_API_PRIVATE_KEY")),
		AccountIndex:          int64(getenvInt("LIGHTER_ACCOUNT_INDEX", 0)),
		APIKeyIndex:           uint8(getenvInt("LIGHTER_API_KEY_INDEX", 4)),
		L1Address:             strings.TrimSpace(os.Getenv("LIGHTER_L1_ADDRESS")),
		Symbol:                strings.ToUpper(getenv("LIGHTER_SYMBOL", "BTC")),
		Threshold:             getenvFloat("PM_THRESHOLD", 0.65),
		MaxSpread:             getenvFloat("PM_MAX_SPREAD", 0.05),
		PrewarmSeconds:        getenvFloat("PM_PREWARM_SECONDS", 15),
		EntryMaxLag:           time.Duration(getenvFloat("ENTRY_MAX_LAG_SECONDS", 2.0) * float64(time.Second)),
		ExitEarly:             time.Duration(getenvFloat("EXIT_EARLY_SECONDS", 0.30) * float64(time.Second)),
		Leverage:              getenvFloat("LIGHTER_LEVERAGE", 1),
		EquityFraction:        getenvFloat("LIGHTER_EQUITY_FRACTION", 1.0),
		MarketSlippage:        getenvFloat("MARKET_SLIPPAGE", 0.02),
		StopPricePct:          getenvFloat("STOP_PRICE_PCT", 0.00125),
		ShadowBinance1s:       getenvBool("SHADOW_BINANCE_1S", true),
		HaltExitLateStreak:    getenvInt("HALT_ON_EXIT_LATE_STREAK", 3),
		HaltStopMissingStreak: getenvInt("HALT_ON_STOP_MISSING_STREAK", 10),
		HaltDDpct:             getenvFloat("HALT_DD_PCT", 0.08),
		HaltPositionMax:       time.Duration(getenvFloat("HALT_POSITION_MAX_SECONDS", 360) * float64(time.Second)),
		KillSwitch:            getenvBool("KILL_SWITCH", false),
		DryRun:                getenvBool("DRY_RUN", false),
		SQLitePath:            getenv("SQLITE_PATH", "./data/bot.db"),
		HTTPAddr:              getenv("HTTP_ADDR", ":8080"),
		DashboardPassword:     os.Getenv("DASHBOARD_PASSWORD"),
		CookieSecure:          getenvBool("COOKIE_SECURE", false),
		LogDir:                getenv("LOG_DIR", "./data/logs"),
		InstanceID:            sanitizeInstanceID(getenv("INSTANCE_ID", "a")),
		InstanceName:          strings.TrimSpace(getenv("INSTANCE_NAME", "")),
		TelegramToken:         strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN")),
		TelegramChatIDs:       parseChatIDs(os.Getenv("TELEGRAM_CHAT_IDS")),
		TelegramHeartbeat:     time.Duration(getenvInt("TELEGRAM_HEARTBEAT_MINUTES", 60)) * time.Minute,
		PMSeriesSlug:          getenv("PM_SERIES_SLUG", "btc-updown-5m"),
		GammaURL:              getenv("PM_GAMMA_URL", "https://gamma-api.polymarket.com"),
		CLOBURL:               getenv("PM_CLOB_URL", "https://clob.polymarket.com"),
		CLOBWSURL:             getenv("PM_CLOB_WS_URL", "wss://ws-subscriptions-clob.polymarket.com/ws/market"),
		BinanceURL:            getenv("BINANCE_URL", "https://api.binance.com"),
	}
	if cfg.InstanceName == "" {
		cfg.InstanceName = cfg.Profile
	}
	if cfg.Profile != "POLY_LIGHTER_1" {
		return nil, fmt.Errorf("unsupported POLY_LIGHTER_PROFILE %q (only POLY_LIGHTER_1 is implemented)", cfg.Profile)
	}
	switch net {
	case NetworkMainnet:
		cfg.BaseURL = getenv("LIGHTER_API_URL", "https://mainnet.zklighter.elliot.ai")
		cfg.WSURL = "wss://mainnet.zklighter.elliot.ai/stream"
		cfg.ChainID = 304
	case NetworkTestnet, "":
		cfg.Network = NetworkTestnet
		cfg.BaseURL = getenv("LIGHTER_API_URL", "https://testnet.zklighter.elliot.ai")
		cfg.WSURL = "wss://testnet.zklighter.elliot.ai/stream"
		cfg.ChainID = 300
	default:
		return nil, fmt.Errorf("unknown LIGHTER_NETWORK %q (use testnet or mainnet)", net)
	}
	if cfg.APIKeyIndex < 4 {
		return nil, fmt.Errorf("LIGHTER_API_KEY_INDEX must be 4-254 (0-3 are reserved for the Lighter UI/app)")
	}
	if cfg.DashboardPassword == "" {
		return nil, fmt.Errorf("DASHBOARD_PASSWORD is required")
	}
	if cfg.Symbol == "" {
		return nil, fmt.Errorf("LIGHTER_SYMBOL is empty")
	}
	if !finite(cfg.Threshold) || cfg.Threshold <= 0.5 || cfg.Threshold >= 1 {
		return nil, fmt.Errorf("PM_THRESHOLD must be in (0.5, 1)")
	}
	if cfg.Symbol != "BTC" {
		return nil, fmt.Errorf("POLY_LIGHTER_1 trades BTC only (LIGHTER_SYMBOL must be BTC)")
	}
	if math.Abs(cfg.Threshold-0.65) > 1e-9 {
		return nil, fmt.Errorf("POLY_LIGHTER_1 requires PM_THRESHOLD=0.65")
	}
	if !finite(cfg.MaxSpread) || math.Abs(cfg.MaxSpread-0.05) > 1e-9 {
		return nil, fmt.Errorf("POLY_LIGHTER_1 requires PM_MAX_SPREAD=0.05")
	}
	if cfg.PMSeriesSlug != "btc-updown-5m" {
		return nil, fmt.Errorf("POLY_LIGHTER_1 requires PM_SERIES_SLUG=btc-updown-5m")
	}
	if !finite(cfg.Leverage) || math.Abs(cfg.Leverage-1) > 1e-9 {
		return nil, fmt.Errorf("POLY_LIGHTER_1 requires LIGHTER_LEVERAGE=1")
	}
	if !finite(cfg.EquityFraction) || math.Abs(cfg.EquityFraction-1) > 1e-9 {
		return nil, fmt.Errorf("POLY_LIGHTER_1 requires LIGHTER_EQUITY_FRACTION=1")
	}
	if cfg.EntryMaxLag <= 0 || cfg.EntryMaxLag > 2*time.Second {
		return nil, fmt.Errorf("ENTRY_MAX_LAG_SECONDS must be in (0, 2] for POLY_LIGHTER_1")
	}
	if cfg.ExitEarly <= 0 || cfg.ExitEarly > 2*time.Second {
		return nil, fmt.Errorf("EXIT_EARLY_SECONDS must be in (0, 2]")
	}
	if !finite(cfg.StopPricePct) || cfg.StopPricePct <= 0 || cfg.StopPricePct > 0.05 {
		return nil, fmt.Errorf("STOP_PRICE_PCT must be in (0, 0.05] (default 0.00125 = 0.125%% of BTC price)")
	}
	if !finite(cfg.MarketSlippage) || cfg.MarketSlippage <= 0 || cfg.MarketSlippage > 0.05 {
		return nil, fmt.Errorf("MARKET_SLIPPAGE must be in (0, 0.05]")
	}
	if !finite(cfg.PrewarmSeconds) || cfg.PrewarmSeconds < 15 || cfg.PrewarmSeconds > 120 {
		return nil, fmt.Errorf("PM_PREWARM_SECONDS must be in [15, 120] for POLY_LIGHTER_1")
	}
	return cfg, nil
}

func (c *Config) SessionCookieName() string {
	return "poly_lighter_session_" + c.InstanceID
}

func (c *Config) EventSlug(ts int64) string {
	return fmt.Sprintf("%s-%d", c.PMSeriesSlug, ts)
}

func getenv(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func getenvInt(k string, def int) int {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		n, err := strconv.Atoi(v)
		if err == nil {
			return n
		}
	}
	return def
}

func getenvFloat(k string, def float64) float64 {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		n, err := strconv.ParseFloat(v, 64)
		if err == nil {
			return n
		}
	}
	return def
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func getenvBool(k string, def bool) bool {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		b, err := strconv.ParseBool(v)
		if err == nil {
			return b
		}
	}
	return def
}

func parseChatIDs(s string) []int64 {
	var out []int64
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		n, err := strconv.ParseInt(p, 10, 64)
		if err == nil {
			out = append(out, n)
		}
	}
	return out
}

func sanitizeInstanceID(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return "a"
	}
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		}
	}
	out := b.String()
	if out == "" {
		return "a"
	}
	if len(out) > 32 {
		out = out[:32]
	}
	return out
}
