# POLY_LIGHTER_1 — стратегия и текущая реализация

Документ описывает **задумку** и **как бот работает сейчас в коде**.  
Контракт research / закон параметров: [`POLY_LIGHTER_STARTPACK_1_1.md`](POLY_LIGHTER_STARTPACK_1_1.md) (live-канон со стопом).  
Исторический пакет без стопа: [`POLY_LIGHTER_STARTPACK_1.md`](POLY_LIGHTER_STARTPACK_1.md).

Если этот файл и код расходятся — приоритет у кода; расхождение нужно либо исправить в коде, либо явно пометить здесь как известный gap.

---

## 1. Задумка

### 1.1 Одна фраза

На **открытии** каждого BTC 5m Up/Down рынка Polymarket смотрим живую цену Up/Down.  
Если одна сторона уже **≥ 0.65**, открываем на **Lighter** BTC-perp **в ту же сторону** на **100% equity, 1x**.  
Стоп **0.125% от цены BTC входа**. Если стоп не сработал — выход на **закрытии этого 5m-окна**.  
Если обе стороны &lt; 0.65 — **skip** (это норма).

Это **не** fade, **не** ставка на Polymarket, **не** Donchian.

### 1.2 Откуда идея

В `poly-trader` стратегия fade часто **не входила**, потому что на open токен уже был дорогим (0.58–0.65).  
На пропущенных из‑за цены барах WR оказался **выше**, чем на вошедших.  
Вывод: дорогая сторона на open — сигнал направления толпы. Бинарный payoff на PM плох для покупки дорогого токена, но **линейный BTC-perp** на Lighter до close окна даёт другую экономику (маленький средний ход, WR &gt; 50%, compounding при ~0 fee).

### 1.3 Две вселенные цен (не путать)

| Слой | Роль |
|------|------|
| **A. Polymarket Up/Down** | Только сигнал/гейт на open |
| **B. Lighter BTC perp** | Только исполнение и live PnL |
| **C. Binance 1s/5m** | Тень и telemetry часов, **не** live-venue |
| **D. Chainlink / PM resolve** | Не наш settlement |

Нет котировки PM → нет сделки. **Не** входить «по направлению Binance».

### 1.4 Что запрещено в v1

Торговля на PM; fade; mid-window chase; patience до 0.65; TP/trail; стоп % депозита или × leverage; default 5x/10x; сигнал Binance/Donchian/RSI; пирамида; ETH/SOL в этом боте.

---

## 2. Жёсткий контракт v1.1 (live)

| Параметр | Значение |
|----------|----------|
| Рынок сигнала | Polymarket `btc-updown-5m-{ts}` |
| Окно | Unix `ts` кратно 300; close = `ts+300` |
| Порог | `≥ 0.65` inclusive → LONG; `≤ 0.35` → SHORT |
| Обе &lt; 0.65 | skip `below_th` |
| Обе ≥ 0.65 | skip `both_ge_th` + alert |
| Deadline входа | **T+2.0s** (`ENTRY_MAX_LAG_SECONDS`) |
| Prewarm | ≥ 15s до open |
| Сайз | `qty = floor_lot(equity / px)`, leverage **1**, fraction **1.0** |
| Стоп | `STOP_PRICE_PCT=0.00125` от **fill**; LONG `fill×0.99875`, SHORT `fill×1.00125` |
| Выход A | стоп → flatten сразу, `exit_reason=sl` |
| Выход B | иначе flatten на `ts+300 − EXIT_EARLY` (~0.3s) |
| После стопа | **нет** re-entry в том же окне |
| Рестарт mid-window | **не** входить; только следующая граница |
| Рестарт с позицией | немедленный reduce-only flatten |

---

## 3. State machine

Состояния (строки в `engine`):

`IDLE` → `ARMING` → `DECIDE` → `IN_POSITION` → `FLATTENING` → `IDLE`, плюс `HALT`.

| Состояние | Смысл |
|-----------|--------|
| IDLE | Ждём prewarm следующего окна |
| ARMING | Resolve slug, WS book, nonce, ready equity/px |
| DECIDE | Гейт + entry (wall ≤ EntryMaxLag) |
| IN_POSITION | Hold; опрос remote exit / возраст / exitAt |
| FLATTENING | Cancel stop + market close / settle |
| HALT | Входы запрещены; flatten first |

### Инварианты

1. Не больше одной позиции.
2. Не держать позицию через границу окна.
3. Не входить если `now − ts ≥ EntryMaxLag`.
4. Не входить без валидной PM-котировки.
5. Пока позиция жива — должен быть рабочий reduce-only стоп (иначе immediate flatten + HALT `stop_missing`).
6. Unique lock `window_ts` в SQLite.

---

## 4. Жизненный цикл одного окна (как в коде)

Точки входа: `engine.Bootstrap` → `engine.Run` → `tick` / `waitNextOpen` / `decide` / `settleWindow`.

### 4.1 Timeline

```text
ts - PrewarmSeconds   waitNextOpen → arm(slug, WS, nonce) + refreshReady
ts - ~1.2s            sleep до open; ready equity/px/flat
ts + 0ms              decide: SnapshotUp, QuoteOK, DecideSide
ts + 0..200ms         при LONG/SHORT → MarketIOC (deadline ts+EntryMaxLag)
после send            confirmLiveFill (до ~5s): trades VWAP и/или account position
после fill            StopLoss reduce-only на fill ± 0.125%
hold                  tick ~1s: detectExit / age halt / exitAt
ts+300-ExitEarly      settleWindow: cancel stop → market close
ts+300                must be flat; иначе exit_late (+ streak → HALT)
после settle          shadow Binance async; waitNextOpen(next)
```

### 4.2 ARM (`arm`)

1. `PM.Resolve(EventSlug(ts))` через Gamma.
2. Fail → ошибка вверх / skip `no_market` на decide.
3. `PM.Arm` (WS) + параллельно `WarmREST` и `PrepareNonce` (live).
4. Ошибка arm в prewarm — Warn; decide всё равно попробует arm снова.

### 4.3 DECIDE — порядок гейтов (`decide`)

1. Уже `consumedTS` / `HasWindow` → sleep next (идемпотентность окна).
2. Halted → skip `halt`.
3. `earliest < 0` → skip `clock` (system UTC ещё до open).
4. `latest ≥ EntryMaxLag` → skip `late`.
5. Не armed → `arm`; fail → `no_market`.
6. Пустые tokens → `no_market`.
7. `SnapshotUp` fail + past lag → `late`; иначе `bad_quote`.
8. `QuoteOK` false → `bad_quote` / `stale_quote` / `quote_incoherent`.
9. both ≥ th → `both_ge_th` + alert.
10. side skip → `below_th`.
11. late after quote → `late`.
12. ready stale (&gt;5s) / eq/px bad → `ready`.
13. qty &lt; min / 0 → `equity_too_small`.
14. DryRun → paper position (см. gaps).
15. late before send → `late`.
16. `MarketIOC` → дальше entry path.

Часы торговли: **host UTC** (`Engine.now`). Offset Binance — только telemetry/dashboard (`_1_1`).

### 4.4 ENTRY / FILL

1. `Signer.MarketIOC` с deadline `ts+EntryMaxLag`.
2. Persist window `state=sent` + order row.
3. Чистый reject → skip `order_failed`.
4. `IsAmbiguousSubmit` → **не** skip; reconcile через `confirmLiveFill`.
5. Oversize позиции → flatten + halt `unexpected_entry_size`.
6. `fillQty≤0` → flatten + CancelAll (если flat) + halt `entry_unconfirmed`, skip reason `entry_unfilled_after_send`.
7. Partial fill → держать фактический size, не добирать после deadline.

### 4.5 STOP

1. `strategy.StopPx(fill, side, StopPricePct)`.
2. `Signer.StopLoss` ≤ 1.5s.
3. Fail → flatten + CancelAll + halt `stop_missing`.
4. Telemetry `OrderByClient` **после** постановки стопа.

### 4.6 HOLD (`tick` при `pos != nil`)

Каждую ~1s:

- `detectExit`: grace 5s после `ArmedAt`; нужен `SeenAcctOpen`; flat → VWAP closing fills → `stop` или `closed_away`.
- Age &gt; `HaltPositionMax` (default 6m) → halt + settle `halt_age`.
- `now ≥ ts+300−ExitEarly` → settle window close.
- Prewarm следующего окна, если позиция ещё жива.

### 4.7 EXIT / SETTLE (`settleWindow`)

1. State `FLATTENING`.
2. Если уже flat → CancelAll.
3. Иначе cancel stop + `sendMarketClose` reduce-only.
4. Retry close; не flat к границе → `exit_late` + `bumpLateStreak`.
5. PnL, `ExitReason`: `sl` | `closed_away` | `halt` | `window_close` | …
6. `pos=nil`, shadow async, `waitNextOpen(next)`.

---

## 5. Деревья всех сценариев

### A. Решение на open

```text
есть рынок? ──нет──► skip no_market
quote valid? ──нет──► bad_quote / stale_quote / quote_incoherent
system UTC до open? ──да──► skip clock
now-ts ≥ EntryMaxLag? ──да──► skip late
p_up ≥ th? ──да──► LONG
p_up ≤ 1-th? ──да──► SHORT
обе ≥ th? ──да──► both_ge_th + alert
иначе ──────────────► below_th
ready/equity/qty ok? ──нет──► ready / equity_too_small
→ send entry
```

### B. После send

```text
clean reject ──────────────► order_failed
ambiguous ack ─────────────► reconcile confirmLiveFill
oversize position ─────────► flatten + HALT unexpected_entry_size
no fill ~5s ───────────────► flatten + HALT entry_unconfirmed
fill OK ───────────────────► place stop
  stop fail ───────────────► flatten + HALT stop_missing
  stop OK ─────────────────► IN_POSITION
```

### C. В позиции

```text
stop fill / stop-like close ──► settle sl
remote close не стопом ───────► closed_away (часто чужой процесс на аккаунте)
до exitAt без стопа ──────────► cancel stop → window_close
не flat к ts+300 ─────────────► exit_late (+ streak→HALT)
age > HaltPositionMax ────────► halt_age
kill / halt / disconnect ─────► flatten first
```

### D. Bootstrap / рестарт

```text
KILL_SWITCH / persisted halt ──► без входов до /resume
позиция на бирже? ──да──► ensureFlat(boot); fail → HALT boot_flatten
bootWindow mid-window ──► ждать следующего open (не догонять вход)
```

### E. Dry-run

Решения и paper-позиция без ордеров на биржу.  
Стоп на dry-run должен симулироваться / явно маркироваться paper-only (см. §8).

---

## 6. Skip / exit reasons

### Skip (persist в `windows.skip_reason`)

| Reason | Смысл |
|--------|--------|
| `below_th` | fav &lt; threshold — норма |
| `both_ge_th` | обе стороны ≥ th |
| `bad_quote` / `stale_quote` / `quote_incoherent` | sanity |
| `late` | опоздание к T+EntryMaxLag |
| `clock` | UTC ещё до open |
| `no_market` | Gamma/tokens fail |
| `ready` | equity/px/flat не свежие с prewarm |
| `equity_too_small` | qty &lt; min |
| `order_failed` | чистый reject входа |
| `entry_unfilled_after_send` | sent, fill не подтверждён |
| `unexpected_entry_size` | oversize |
| `stop_missing` | стоп не встал |
| `halt` | входы запрещены |

### Exit (`windows.exit_reason`, иногда join в skip)

| Reason | Смысл |
|--------|--------|
| `sl` | стоп |
| `window_close` | выход на границе |
| `closed_away` | закрыто не стопом |
| `halt` / `halt_age` | стоп-кран / возраст |
| `skip` | окно без позиции |
| `exit_late` | не успели flat к границе (часто join) |

---

## 7. Halt / Kill

### Авто-HALT (код)

- streak `exit_late` / unfilled ≥ `HALT_ON_EXIT_LATE_STREAK` (3)
- позиция старше `HaltPositionMax` (~6 мин)
- equity drop &gt; `HALT_DD_PCT` (8%) от пика
- `boot_flatten`, `entry_unconfirmed`, `unexpected_entry_size`, `stop_missing`

**Отличие от startpack `_1_1`:** вместо «10 окон подряд без стопа» код делает **immediate** flatten+HALT на `stop_missing` (строже).

### Оператор

- `KILL_SWITCH=true` — старт в halt
- Dashboard / Telegram `/kill` — flatten + запрет входов
- `/resume` — сброс Kill + Halted + LateStreak

### Ops рядом с Donchian

- Отдельный Lighter account_index (иначе flatten чужой позиции)
- Отдельный Telegram bot token
- Dashboard **8081** (Donchian = 80)
- Host clock общий — sync времени двигает оба бота

---

## 8. Параметры (env)

| Env | Дефолт / канон | Смысл |
|-----|----------------|--------|
| `PM_THRESHOLD` | 0.65 | порог mid |
| `PM_MAX_SPREAD` | 0.05 | max BBO spread |
| `PM_PREWARM_SECONDS` | 15 | prewarm |
| `ENTRY_MAX_LAG_SECONDS` | 2.0 | hard deadline входа |
| `EXIT_EARLY_SECONDS` | 0.30 | flatten до границы |
| `STOP_PRICE_PCT` | 0.00125 | 0.125% цены BTC |
| `MARKET_SLIPPAGE` | 0.02 | IOC price guard ±2% |
| `LIGHTER_LEVERAGE` | 1 | locked |
| `LIGHTER_EQUITY_FRACTION` | 1.0 | 100% equity |
| `LIGHTER_SYMBOL` | BTC | only |
| `HALT_ON_EXIT_LATE_STREAK` | 3 | |
| `HALT_DD_PCT` | 0.08 | |
| `KILL_SWITCH` | false | boot halt |
| `DRY_RUN` | false | paper |
| `SHADOW_BINANCE_1S` | true | тень |
| `WEB_PORT` | 8081 | dashboard |

Полный список: `.env.example`, валидация — `backend/internal/config`.

---

## 9. Карта код ↔ стратегия

| Слой | Пакет / файлы |
|------|----------------|
| Оркестрация окна | `backend/internal/engine/` (`lifecycle`, `entry`, `exit`, `risk`, …) |
| Гейт / стоп math | `backend/internal/strategy/gate.go`, `stop.go` |
| Исполнение Lighter | `backend/internal/exchange/lighter/` |
| Сигнал PM | `backend/internal/polymarket/` |
| Тень Binance | `backend/internal/binance/` |
| Окна 5m / sleep | `backend/internal/clock/` |
| Persist | `backend/internal/store/` |
| Dashboard API | `backend/internal/api/` |
| Alerts | `telegram/`, `notify/`, `flog/` |
| Entrypoint | `backend/cmd/bot/main.go` |
| UI | `frontend/src/Dashboard.tsx` |

Критичный поток:

```text
main → Bootstrap → Run
  wait/prewarm → arm → decide → MarketIOC
  → confirm fill → stop → hold → exit/flatten → settle + shadow
```

---

## 10. Известные расхождения / принятые решения

| Тема | Контракт / docs | Код / решение |
|------|-----------------|---------------|
| Канон | `_1_1` со стопом | Live следует `_1_1` |
| README historically | one-liner без стопа | должен ссылаться на `_1_1` + этот файл |
| Clock gate `_1` | skip если Binance offset &gt;1s | код: host UTC; Binance не блокирует вход (`_1_1`) |
| Local stop watchdog | SLA &lt;200ms | exchange stop + `detectExit` + local `StopHit` на last/mark в `tick` |
| HALT 10 окон без стопа | в `_1_1` | заменено на immediate `stop_missing` |
| Gamma synthetic book | CLOB канон | live gate: только WS/REST CLOB; Gamma mid не подставляет synthetic book |
| Dry-run stop | risk model со стопом | paper path ставит `stop_loss` dry_run + `StopPx` в позиции |
| Ambiguous submit | reconcile | entry + stop + exit reconcile через `IsAmbiguousSubmit` + `ConfirmFill` |

---

## 11. Как понять, что бот «тот»

Хорошо: coverage &gt;95%; p50 fill lag &lt;1s, p90 &lt;2s; mean bps порядка research (~+1…+2); доля SL порядка ~12% (не ≪5%).  
Красный: mid-window entries; mean≤0 на ≥400–500 сделках; coverage ~70%; DD 8–10% за неделю без выноса BTC.  
На 200 сделках WR 50–65% — шум, **не крутить порог**.
