package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"poly-lighter.trade/bot/internal/engine"
)

type Bot struct {
	Token    string
	Allowed  map[int64]bool
	Engine   *engine.Engine
	Log      *slog.Logger
	HTTP     *http.Client
	offset   int
	mu       sync.Mutex
	lastSend time.Time
	hbEvery  time.Duration
	alertAt  map[string]time.Time
}

func New(token string, chatIDs []int64, eng *engine.Engine, hb time.Duration, log *slog.Logger) *Bot {
	allow := map[int64]bool{}
	for _, id := range chatIDs {
		allow[id] = true
	}
	if log == nil {
		log = slog.Default()
	}
	return &Bot{
		Token: token, Allowed: allow, Engine: eng, Log: log,
		HTTP: &http.Client{Timeout: 45 * time.Second}, hbEvery: hb, alertAt: map[string]time.Time{},
	}
}

func (b *Bot) Alert(_ context.Context, level, kind, message string) {
	if b.Token == "" {
		return
	}
	b.mu.Lock()
	skip := engine.SuppressRepeat(b.alertAt, time.Now(), time.Hour, level, kind, message)
	b.mu.Unlock()
	if skip {
		return
	}
	title := kind
	head := "<b>" + html.EscapeString(title) + "</b>"
	if level == "error" {
		head += " · ошибка"
	} else if level == "warn" {
		head += " · внимание"
	}
	b.broadcastHTML(head + "\n" + html.EscapeString(message))
}

func (b *Bot) broadcastHTML(text string) {
	for id := range b.Allowed {
		if err := b.sendHTML(id, text); err != nil {
			b.Log.Warn("telegram send", "err", err)
		}
	}
}

func (b *Bot) Run(ctx context.Context) {
	if b.Token == "" {
		return
	}
	b.Alert(ctx, "info", "boot", "POLY_LIGHTER_1 онлайн")
	go b.heartbeat(ctx)
	for {
		if ctx.Err() != nil {
			return
		}
		if err := b.poll(ctx); err != nil && ctx.Err() == nil {
			b.Log.Warn("telegram poll", "err", b.redact(err))
			time.Sleep(3 * time.Second)
		}
	}
}

func (b *Bot) heartbeat(ctx context.Context) {
	if b.hbEvery <= 0 {
		return
	}
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			b.mu.Lock()
			silent := time.Since(b.lastSend)
			b.mu.Unlock()
			if silent >= b.hbEvery {
				ok, detail := b.Engine.Health()
				b.Alert(ctx, "info", "heartbeat", fmt.Sprintf("жив, здоровье=%v (%s)", ok, detail))
			}
		}
	}
}

type tgResp struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
	Desc   string          `json:"description"`
}

type update struct {
	UpdateID int `json:"update_id"`
	Message  *struct {
		Chat struct {
			ID int64 `json:"id"`
		} `json:"chat"`
		Text string `json:"text"`
		From struct {
			ID int64 `json:"id"`
		} `json:"from"`
	} `json:"message"`
}

func (b *Bot) redact(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if b != nil && b.Token != "" {
		s = strings.ReplaceAll(s, b.Token, "[redacted]")
	}
	return s
}

func (b *Bot) poll(ctx context.Context) error {
	u := fmt.Sprintf("https://api.telegram.org/bot%s/getUpdates?timeout=25&offset=%d", b.Token, b.offset)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := b.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("%s", b.redact(err))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var wrap tgResp
	if err := json.Unmarshal(body, &wrap); err != nil {
		return err
	}
	if !wrap.OK {
		return fmt.Errorf("telegram: %s", wrap.Desc)
	}
	var ups []update
	if err := json.Unmarshal(wrap.Result, &ups); err != nil {
		return err
	}
	for _, up := range ups {
		b.offset = up.UpdateID + 1
		if up.Message == nil {
			continue
		}
		chat := up.Message.Chat.ID
		if !b.Allowed[chat] && !b.Allowed[up.Message.From.ID] {
			_ = b.sendHTML(chat, "Этот чат не в списке TELEGRAM_CHAT_IDS.")
			continue
		}
		b.handle(ctx, chat, strings.TrimSpace(up.Message.Text))
	}
	return nil
}

func (b *Bot) handle(ctx context.Context, chat int64, text string) {
	cmd := strings.ToLower(strings.TrimSpace(strings.Split(text, "@")[0]))
	switch cmd {
	case "/start", "/help":
		_ = b.sendHTML(chat, strings.Join([]string{
			"<b>POLY_LIGHTER_1</b>",
			"",
			"/status — снимок",
			"/trades — последние сделки",
			"/health — здоровье",
			"/kill — flatten, запрет входов",
			"/resume — снять halt/kill",
		}, "\n"))
	case "/health":
		ok, detail := b.Engine.Health()
		state := "ок"
		if !ok {
			state = "не готов"
		}
		_ = b.sendHTML(chat, "<b>Здоровье</b> · "+state+"\n"+html.EscapeString(detail))
	case "/status":
		sn := b.Engine.Snapshot(ctx)
		_ = b.sendHTML(chat, fmt.Sprintf(
			"<b>Status</b>\n%s · %s\nstate=%s pos=%s\nequity=%.2f p_up=%.3f spread=%.3f\nkill=%v halt=%s\nclock_binance=%+.0f ms ok=%v rtt=%.0f ms\nwindow=%d +%.0fs / 300\nlev=%.1fx stop=%.4f%%",
			sn.Profile, sn.Network, sn.State, sn.Position, sn.Equity, sn.PUp, sn.Spread, sn.KillSwitch, sn.HaltReason,
			sn.ClockOffsetMS, sn.ClockOK, sn.ClockRTTMS, sn.WindowTS, sn.SecsIntoWindow, sn.Leverage, sn.StopPricePct*100,
		))
	case "/trades":
		rows, err := b.Engine.Store.ListWindows(ctx, 40)
		if err != nil {
			_ = b.sendHTML(chat, html.EscapeString(err.Error()))
			return
		}
		var bld strings.Builder
		bld.WriteString("<b>Окна</b>\n")
		n := 0
		for _, w := range rows {
			if n >= 10 {
				break
			}
			n++
			fmt.Fprintf(&bld, "%d %s %s pnl=%v\n", w.WindowTS, w.Side, w.SkipReason, w.PnLUSD.Float64)
		}
		_ = b.sendHTML(chat, bld.String())
	case "/kill":
		if err := b.Engine.KillAll(ctx); err != nil {
			_ = b.sendHTML(chat, html.EscapeString(err.Error()))
			return
		}
		_ = b.sendHTML(chat, "<b>Kill-switch</b>\nПозиция закрыта, входы запрещены.")
	case "/resume":
		b.Engine.Resume(ctx)
		_ = b.sendHTML(chat, "<b>Kill-switch снят</b>")
	default:
		_ = b.sendHTML(chat, "Не знаю. /help")
	}
}

func (b *Bot) sendHTML(chat int64, text string) error {
	payload, _ := json.Marshal(map[string]any{
		"chat_id": chat, "text": text, "parse_mode": "HTML", "disable_web_page_preview": true,
	})
	u := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", b.Token)
	resp, err := b.HTTP.Post(u, "application/json", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var wrap tgResp
	_ = json.Unmarshal(body, &wrap)
	if !wrap.OK {
		return fmt.Errorf("sendMessage: %s", wrap.Desc)
	}
	b.mu.Lock()
	b.lastSend = time.Now()
	b.mu.Unlock()
	return nil
}
