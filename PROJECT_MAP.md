# PROJECT_MAP — карта репозитория POLY_LIGHTER

Краткая навигация для ИИ и людей. Детали стратегии — в `docs/`, деплой — в `INSTRUCTIONS.md`.  
**Обновляй эту карту**, когда меняется структура, ответственность пакетов или критичные потоки.

Последняя актуализация: 2026-11-22 (лаг входа дописывается в фоне из точного created_at; строка exit-ордера пишется отдельно от дедлайна границы).

---

## 1. Что это

**POLY_LIGHTER_1** — бот: сигнал с Polymarket BTC 5m Up/Down → исполнение на Lighter BTC-perp (mainnet).  
Не ставка на Polymarket. Не fade. Отдельный compose/счёт/Telegram от Donchian (`/opt/donchian`).

| Слой | Технология |
|------|------------|
| Backend | Go (`backend/`), SQLite WAL |
| Frontend | React + TS + Vite + Tailwind (`frontend/`) |
| Deploy | Docker Compose, VPS `/opt/poly-lighter`, dashboard `:8081` |

---

## 2. Дерево (смысловое)

```
poly_lighter_trade/
├── AGENTS.md                 # правила для ИИ
├── PROJECT_MAP.md            # эта карта
├── README.md                 # обзор продукта
├── INSTRUCTIONS.md           # VPS / deploy / диагностика
├── BOTS_CLOCK_AND_LAG_CONTEXT.md  # контекст часов/лага (два бота)
├── docs/                     # startpack + STRATEGY_IMPLEMENTATION_RU.md
├── docker-compose.yml
├── .env.example
├── deploy/                   # Caddyfile (опционально, осторожно с Donchian)
├── scripts/                  # helpers (offset, live_vs_backtest.py)
├── backend/
│   ├── cmd/bot/main.go       # entrypoint процесса
│   └── internal/
│       ├── engine/           # торговый цикл, состояние, flatten/halt
│       ├── strategy/         # gate + stop math (чистая логика)
│       ├── exchange/lighter/ # HTTP + signer + trades/fills
│       ├── polymarket/       # Gamma + CLOB WS/REST
│       ├── binance/          # shadow 1s/5m
│       ├── store/            # SQLite: windows, orders, state
│       ├── api/              # HTTP API дашборда
│       ├── config/           # env → Config
│       ├── clock/            # окна 5m, sleep
│       ├── telegram/ + notify/
│       └── flog/             # JSONL файловые логи
└── frontend/
    └── src/
        ├── Dashboard.tsx     # основной UI
        ├── api.ts            # клиент к backend
        ├── App.tsx, Login.tsx
        └── components/ui/    # UI-kit
```

Не коммитить: `.env`, `data/`, сырые diagnostic-дампы с секретами.

---

## 3. Критичный поток (runtime)

```
main.go
  → config.Load
  → store / lighter HTTP+Signer / polymarket / binance / notify
  → engine.Bootstrap (flat check, leverage, cancel_all)
  → engine.Run loop:
       wait/prewarm → arm(slug, WS book, nonce, ready eq/px)
       → decide @ window open (gate)
       → MarketIOC entry (deadline EntryMaxLag)
       → if submit ack lost (timeout/reset): reconcile, do not assume reject
       → confirm fill + optional stop-loss
       → hold → exit / detect remote close / flatten
       → settle: one reduce; on the boundary, no pre-close tape and cancel+IOC deadline leaves T+2 for decide; the exit order row is written on its own short context; exit px/PnL only from the tape (refine async)
       → decide refreshes a stale equity/price snapshot once inside the entry budget; fill lag is a sub-second created_at written asynchronously after the stop
       → shadow binance (shadow columns only)
  ↔ api.Server (dashboard, kill/resume, status)
```

**Жёсткие места риска:** ambiguous `sendTx` (см. `lighter.IsAmbiguousSubmit` → reconcile через `confirmLiveFill`); выход на границе не считается fill, пока лента не подтвердила цену; один in-flight reduce, пока счёт не flat; flatten/prewarm; stop_missing; entry_unconfirmed; общий VPS clock с Donchian.

---

## 4. Backend packages — куда идти

| Пакет | Ответственность | Ключевые файлы |
|-------|-----------------|----------------|
| `engine` | Оркестрация окна, позиции, halt/kill, timing traces | `engine.go` (types), `lifecycle.go`, `entry.go`, `exit.go` (flatness/settle/watchdog), `risk.go`, `shadow.go`, `persist.go`, `trace.go`, `snapshot.go` |
| `strategy` | Порог/спред → side; stop price; lot floor | `gate.go`, `stop.go` |
| `exchange/lighter` | REST, SendTx, подпись ордеров, fills/tape, ambiguous submit | `http.go`, `signer.go`, `trades.go`, `ambiguous.go` |
| `polymarket` | Событие/slug, book quotes | `client.go` |
| `binance` | Shadow close / stop-hit proxy | `client.go` |
| `store` | Persistence windows/orders/events/global | `store.go`, `timings.go` |
| `config` | Env, валидация, slug helper | `config.go` |
| `clock` | `WindowOf`, sleep until | `clock.go` |
| `api` | Auth cookie, REST для UI | `server.go` |
| `telegram` / `notify` | Алерты | `bot.go`, `notify.go` |
| `flog` | Суточные JSONL | `flog.go` |

Тесты лежат рядом: `*_test.go` в том же пакете.

---

## 5. Frontend — куда идти

| Файл | Роль |
|------|------|
| `frontend/src/Dashboard.tsx` | Статус, окна, skip reasons (в т.ч. `order_failed` → «ордер не ушёл») |
| `frontend/src/TimingTimeline.tsx` | Таймлайн окна вокруг `:00`; кнопка «Гант» раскрывает пропорциональные полосы, `:00` — вертикальная линия |
| `frontend/src/timeline.ts` | Сборка точек из `WindowTimings` (смещения от `:00` и длительности шагов) |
| `frontend/src/api.ts` | Fetch API |
| `frontend/src/Login.tsx` | Пароль дашборда |
| `frontend/src/components/ui/*` | shadcn-подобные примитивы |

Локально Vite обычно на **5174** (Donchian — 5173).

---

## 6. Конфиг и данные

| Что | Где |
|-----|-----|
| Пример env | `.env.example` |
| Live env на VPS | `/opt/poly-lighter/.env` (не в git) |
| SQLite | `SQLITE_PATH` → обычно `/data/bot.db` |
| JSONL | `LOG_DIR` → `/data/logs/bot-YYYYMMDD.jsonl` |
| Compose | `docker-compose.yml` → сервисы `bot`, `web` |

Важные knobs: `PM_THRESHOLD`, `ENTRY_MAX_LAG_SECONDS`, `MARKET_SLIPPAGE`, `STOP_PRICE_PCT`, `LIGHTER_*`, `KILL_SWITCH`, `DRY_RUN`.

---

## 7. Документы — не путать роли

| Файл | Роль |
|------|------|
| `AGENTS.md` | Как ИИ обязан работать и какого качества код |
| `PROJECT_MAP.md` | Индекс структуры и ответственности |
| `README.md` | Человеческий обзор |
| `INSTRUCTIONS.md` | Deploy, wipe, снятие диагностики |
| `docs/POLY_LIGHTER_STARTPACK_1_1.md` | Live-канон стратегии (со стопом) |
| `docs/POLY_LIGHTER_STARTPACK_1.md` | Исторический hold-to-close без стопа |
| `docs/STRATEGY_IMPLEMENTATION_RU.md` | Алгоритм и сценарии как в коде |
| `BOTS_CLOCK_AND_LAG_CONTEXT.md` | Разбор часов/лага двух ботов (контекст) |
| `scripts/live_vs_backtest.py` | Live-сделки дашборда vs канон T+1 / SL 0.125% на наших `p_up`. Правило для ИИ — в `AGENTS.md` |

---

## 8. Чеклист обновления карты

Обнови этот файл, если:

- [ ] добавлен/удалён/переименован пакет или entrypoint;
- [ ] сменилась ответственность слоя (например, fill-confirm уехал из engine в lighter);
- [ ] изменился критичный runtime-поток;
- [ ] появились новые обязательные ops-инварианты, о которых агент должен знать с порога.

Не тащи сюда простыни логов и временные гипотезы инцидентов — для этого диагностика / chat / context-доки.
