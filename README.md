# POLY_LIGHTER_1 — автоторговый бот

Контракт стратегии: [`docs/POLY_LIGHTER_STARTPACK_1.md`](docs/POLY_LIGHTER_STARTPACK_1.md).  
Выкат на VPS рядом с Donchian: [`INSTRUCTIONS.md`](INSTRUCTIONS.md).

На **открытии** каждого BTC 5m Up/Down рынка Polymarket читаем mid Up. Если сторона ≥ **0.65** — открываем **Lighter BTC-perp в ту же сторону** на 100% equity, **1x**, держим до close окна, flatten. Если обе стороны < 0.65 — skip. Это **не** fade и **не** ставка на Polymarket.

| Слой | Стек |
|------|------|
| Сигнал | Polymarket Gamma + CLOB (WS + REST) |
| Исполнение | [Lighter](https://apidocs.lighter.xyz) BTC perp, официальный Go-signer |
| Тень | Binance BTCUSDT 1s close @ T+1s → 5m close |
| Бэкенд | Go: торговый цикл + REST API + Telegram |
| БД | SQLite (WAL), unique `window_ts` |
| Дашборд | React + TypeScript + Tailwind |
| Деплой | Docker Compose |

---

## Два дашборда на одном сервере с donchian_trade

Donchian уже занимает **порт 80** (`WEB_PORT=80`). Этот бот по умолчанию публикует дашборд на **8081**.

| Бот | Каталог на VPS | Дашборд | Compose-проект |
|-----|----------------|---------|----------------|
| Donchian | `/opt/donchian` | `http://IP/` | свой `docker compose` |
| POLY_LIGHTER_1 | `/opt/poly-lighter` | `http://IP:8081/` | свой, `WEB_PORT=8081` |

Это **два независимых compose-проекта**. Не ставить оба `web` на `:80`.

**HTTPS / один домен:** не поднимайте `caddy` из этого репо, если у Donchian уже Caddy на 80/443. Добавьте второй сайт в **его** Caddyfile:

```caddy
poly.example.com {
    reverse_proxy 127.0.0.1:8081
}
```

и в `.env` этого бота: `COOKIE_SECURE=true`. Donchian остаётся на своём домене.

**Обязательно разные счета Lighter.** Этот бот кладёт **весь** equity в одну BTC-сделку и на рестарте flatten'ит позицию. На том же аккаунте, что Donchian, он снимет Donchian-позицию. Нужен отдельный `LIGHTER_ACCOUNT_INDEX` (субаккаунт) и свой API key index 4–254.

**Telegram:** отдельный бот от BotFather. Один token нельзя поллить двум процессам (409 Conflict).

UFW: кроме 80/443 Donchian — `sudo ufw allow 8081/tcp` (или только через Caddy, тогда 8081 слушает localhost: в compose смените publish на `127.0.0.1:8081:80`).

---

## Что делает бот (v1)

1. За 15 с до `ts` (unix, кратно 300) резолвит `btc-updown-5m-{ts}`, подписывается на CLOB book.
2. На `ts` берёт mid Up. Sanity: spread ≤ 5¢, BBO не стены 0.01/0.99, возраст котировки ≤ 1 с.
3. `p_up ≥ 0.65` → LONG; `p_up ≤ 0.35` → SHORT; иначе skip.
4. Hard deadline входа **T+2.0 с**. Рестарт в середине окна **не** входит.
5. Сайз: `qty = equity / px`, leverage 1. Выход за 300 мс до границы, reduce-only.
6. Стоп-кран: 3 окна подряд `exit_late` / unfilled; позиция > 6 мин; DD > 8% от пика.

Прод: **mainnet**, `DRY_RUN=false`. Небольшой депозит на отдельном счёте = полный стек 1x на малых деньгах. Подробности: [`INSTRUCTIONS.md`](INSTRUCTIONS.md).

---

## Конфиг

```bash
cp .env.example .env
mkdir -p data
```

Семантика имён — §14 startpack. `DASHBOARD_PASSWORD` обязателен.

Локально без Docker:

```bash
cd backend
go test ./...
go run ./cmd/bot

# другой терминал
cd frontend
npm install
npm run dev   # http://127.0.0.1:5174  (не 5173 — его может занять Donchian)
```

Docker на VPS:

```bash
sudo mkdir -p /opt/poly-lighter && cd /opt/poly-lighter
git clone <URL> .
cp .env.example .env   # заполнить; WEB_PORT=8081
mkdir -p data
docker compose up -d --build
docker compose logs -f bot
```

Дашборд: `http://IP:8081/` → пароль.

---

## Telegram

`/health` `/status` `/trades` `/kill` `/resume` `/help`

---

## Дисклеймер

Исследовательский порт. 1x на полном стеке: 5m ход BTC ≈ ход депозита. Рискуйте только тем, что готовы потерять.
