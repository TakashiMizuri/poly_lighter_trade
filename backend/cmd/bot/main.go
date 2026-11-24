package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"poly-lighter.trade/bot/internal/api"
	"poly-lighter.trade/bot/internal/binance"
	"poly-lighter.trade/bot/internal/config"
	"poly-lighter.trade/bot/internal/engine"
	"poly-lighter.trade/bot/internal/exchange/lighter"
	"poly-lighter.trade/bot/internal/flog"
	"poly-lighter.trade/bot/internal/notify"
	"poly-lighter.trade/bot/internal/polymarket"
	"poly-lighter.trade/bot/internal/store"
	"poly-lighter.trade/bot/internal/telegram"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	cfg, err := config.Load()
	if err != nil {
		log.Error("config", "err", err)
		os.Exit(1)
	}
	if cfg.APIPrivateKey == "" && !cfg.DryRun {
		log.Error("LIGHTER_API_PRIVATE_KEY is required (or set DRY_RUN=true)")
		os.Exit(1)
	}
	log.Info("profile", "name", cfg.Profile, "th", cfg.Threshold, "symbol", cfg.Symbol, "network", cfg.Network, "dry_run", cfg.DryRun, "leverage", cfg.Leverage, "equity_fraction", cfg.EquityFraction)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	st, err := store.Open(cfg.SQLitePath)
	if err != nil {
		log.Error("sqlite", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	httpClient := lighter.NewHTTP(cfg.BaseURL)
	httpClient.Log = log
	markets := map[string]lighter.MarketMeta{}
	if err := retryTransient(ctx, log, "markets", 2*time.Minute, func() error {
		resolved, err := httpClient.ResolveMarkets(ctx, []string{cfg.Symbol})
		if err != nil {
			return err
		}
		for k, v := range resolved {
			markets[k] = v
			log.Info("market", "symbol", k, "id", v.MarketID, "size_decimals", v.SizeDecimals)
		}
		return nil
	}); err != nil && !cfg.DryRun {
		log.Error("markets", "err", err)
		os.Exit(1)
	}

	var signer *lighter.Signer
	if cfg.APIPrivateKey != "" {
		signer, err = lighter.NewSigner(httpClient, cfg.APIPrivateKey, cfg.AccountIndex, cfg.APIKeyIndex, cfg.ChainID)
		if err != nil {
			log.Error("signer", "err", err)
			os.Exit(1)
		}
		signer.Meta = markets
		signer.Log = log
		httpClient.SetTokenSource(func(context.Context) (string, error) {
			return signer.AuthToken()
		})
		if !cfg.DryRun && cfg.WSURL != "" {
			meta := markets[cfg.Symbol]
			hub := lighter.NewStream(cfg.WSURL, cfg.AccountIndex, meta.MarketID, signer.AuthToken, log)
			signer.Stream = hub
			httpClient.Stream = hub
			go hub.Run(ctx)
			log.Info("lighter stream", "url", cfg.WSURL, "market_id", meta.MarketID)
		}
	} else {
		log.Warn("running without signer (DRY_RUN)")
	}

	pm := polymarket.New(cfg.GammaURL, cfg.CLOBURL, cfg.CLOBWSURL)
	bn := binance.New(cfg.BinanceURL)
	eng := engine.New(cfg, st, httpClient, signer, markets, pm, bn, notify.Nop{}, log)

	fl, err := flog.New(cfg.LogDir)
	if err != nil {
		log.Error("flog", "err", err)
		os.Exit(1)
	}
	defer fl.Close()
	eng.Flog = fl

	var tg *telegram.Bot
	if cfg.TelegramToken != "" {
		tg = telegram.New(cfg.TelegramToken, cfg.TelegramChatIDs, eng, cfg.TelegramHeartbeat, log)
		eng.Notify = notify.Multi{List: []notify.Notifier{tg, notify.FuncNotifier{F: func(level, kind, message string) {
			log.Info("alert", "level", level, "kind", kind, "msg", message)
		}}}}
	} else {
		eng.Notify = notify.FuncNotifier{F: func(level, kind, message string) {
			log.Info("alert", "level", level, "kind", kind, "msg", message)
		}}
	}

	if err := retryTransient(ctx, log, "bootstrap", 2*time.Minute, func() error {
		return eng.Bootstrap(ctx)
	}); err != nil {
		log.Error("bootstrap", "err", err)
		os.Exit(1)
	}

	go eng.Run(ctx)
	if tg != nil {
		go tg.Run(ctx)
	}

	srv := api.NewWithCookie(cfg.HTTPAddr, cfg.DashboardPassword, cfg.CookieSecure, cfg.SessionCookieName(), eng, st)
	go func() {
		log.Info("http listen", "addr", cfg.HTTPAddr, "network", cfg.Network)
		if err := srv.ListenAndServe(); err != nil {
			log.Error("http", "err", err)
			cancel()
		}
	}()

	<-ctx.Done()
	log.Info("shutdown")
}

func retryTransient(ctx context.Context, log *slog.Logger, label string, pause time.Duration, fn func() error) error {
	attempt := 0
	for {
		err := fn()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		attempt++
		log.Error(label, "err", err, "attempt", attempt, "retry_in", pause.String())
		t := time.NewTimer(pause)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
}
