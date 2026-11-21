# POLY_LIGHTER_STARTPACK_1_1

**Роль:** **live-канон** стратегии POLY_LIGHTER_1 **со стопом 0.125% цены**.  
**Назначение:** самодостаточная спецификация, чтобы **с нуля** в другом репозитории поднять автоторговый бот и повторить эту стратегию 1:1. Этот файл — контракт. Если live расходится с файлом, виноват бот, не «рынок изменился», пока не доказано обратное тенью.

Как сейчас реализовано в этом репозитории (алгоритм, сценарии, gaps): [`STRATEGY_IMPLEMENTATION_RU.md`](STRATEGY_IMPLEMENTATION_RU.md).  
Исторический пакет **без** стопа (hold-to-close): [`POLY_LIGHTER_STARTPACK_1.md`](POLY_LIGHTER_STARTPACK_1.md).

**Коднейм профиля:** `POLY_LIGHTER_1`  
**Дата заморозки research:** 2026-11-19  
**Research-репо:** `new-algotrading-adventure`  
**Живой прецедент котировки на open:** `poly-trader` (май 2026, skip `entry_price_out_of_range`)

---

## 0. Одна фраза

На **открытии** каждого BTC 5m Up/Down рынка Polymarket смотрим **живую** цену Up/Down. Если одна сторона уже **≥ 0.65**, открываем на **Lighter** BTC-perp **в ту же сторону** на **100% equity, 1x**. Стоп **0.125% от цены BTC входа** (не от депозита). Если стоп не сработал — выход на **закрытии этого 5m-окна**. Комиссия в модели **0%**. Если обе стороны < 0.65 — **skip**, это норма, не ошибка.

Это **не** fade. Это **не** blend_fade2. Это **не** Donchian. Это **не** ставка на Polymarket.

---

## 1. Зачем эта стратегия существует

### 1.1 Откуда идея

В `poly-trader` стратегия `blend_fade2` на закрытии Binance 5m даёт сигнал на **следующую** свечу и пытается купить **дешёвый** токен на Polymarket. Live часто **не входил**: на открытии окна bid уже **0.58–0.65** (рынок «опередил» fade). Это писалось как баг исполнения. На самом деле это **информация**.

Факты live май 2026 (`IDEA3.md`, `docs/BACKTEST_VS_LIVE_GAP.ru.md`):

| | |
|--|--|
| Сигнальных баров | 451 |
| Исполнено | 288 |
| Skip `entry_price_out_of_range` | **140** (плюс ещё ~21 `order_failed`) |
| WR вошедших | 46.5% |
| WR **пропущенных из‑за цены** | **62.9%** (88/140) |
| Типичная цена на skip | **~0.565**, часто уже **0.58–0.65 на open** |

Вывод, который **переворачивает** сторону сделки:

- На **T+0** 5m-окна цена Polymarket **уже известна**. Это не гипотеза: бот её читал и гейтил вход.
- Когда токен **дорогой**, направление толпы по этому окну часто **правильное** (отсюда 62.9% WR на skip-барах).
- Покупать этот токен на Polymarket **экономически плохо** (payoff бинарный, breakeven ≈ цене).  
- Взять **то же направление** линейным BTC-perp до close окна — другая экономика: средний ход маленький, но WR > 50% при **нулевой** комиссии даёт compounding.

### 1.2 Что мы **не** делаем

Запрещено в v1, даже «на всякий случай»:

| Запрет | Почему |
|--------|--------|
| Торговать **на Polymarket** | Бинарный payoff + комиссия PM убивают дорогой вход; весь смысл — уйти на Lighter |
| **Fade** (покупать дешёвую сторону) | Это старый poly-trader. Здесь мы **следуем** дорогой стороне |
| Ждать первый **trade** в CLOB history / `t_first` | В jsonl `t_first` медиана ~18 с — дырка **архива**, не рынка. Live book есть сразу |
| Входить «когда успеем» в середине окна | Edge живёт в **первых секундах**. T+5s в research уже сильно хуже T+1s |
| **Нет стопа** / стоп 0.5% / 1% / «% депозита» | v1 стоп = **0.125% цены BTC**. Шире — хуже hold-to-close; «× плечо» — другая стратегия |
| Тейк / трейл внутри 5m | Кроме стопа 0.125% выход только close окна |
| Умножать стоп на плечо (3x → 0.375% цены) | Плечо само бьёт по equity. Price stop **не** масштабировать |
| Плечо 5x / 10x как default | Отдельный стресс-тест, не этот пакет |
| Сигнал с Binance OHLC / Donchian / RSI | Единственный сигнал — **цена PM на open** |
| Patience «подождём, вдруг станет 0.65 позже» | Поздний вход ≠ тот же edge. Либо гейт на open, либо skip |
| Несколько позиций, пирамида, усреднение | Один слот, одно окно |
| Торговать ETH/SOL «по аналогии» в v1 | Research только **BTC** 5m Up/Down |

---

## 2. Жёсткая спецификация v1 (таблица — закон)

| Параметр | Значение | Комментарий |
|----------|----------|-------------|
| Сигнальный рынок | Polymarket **BTC Up or Down 5m** | Серия slug `btc-up-or-down-5m`, события `btc-updown-5m-{ts}` |
| `ts` окна | Unix **секунды**, кратно **300** | Open окна = `ts`, close = `ts+300` |
| Исполнение | **Lighter** BTC/USD perpetual | Isolated (или cross — но риск считаем как isolated 1x) |
| Сторона | PM Up ≥ 0.65 → **LONG BTC**; PM Down ≥ 0.65 → **SHORT BTC** | Down ≥ 0.65 ⇔ `p_up ≤ 0.35` |
| Порог | **0.65 включительно** | `>=`, не `>` |
| Если обе < 0.65 | **Skip** | Самый частый исход |
| Если обе ≥ 0.65 | **Skip + alert** | При нормальном стакане невозможно (`p_up+p_down≈1`) |
| Момент решения | **Открытие окна** (`t = ts`) | Цель: решение и ордер **до T+1s** |
| Hard deadline входа | **T+2.0s** | Если к `ts+2` нет валидной котировки или fill не отправлен — **skip окна** |
| Цена входа (live) | Fill Lighter (taker / aggressive limit) | Research-прокси: Binance BTCUSDT **1s close на `ts+1`** |
| Стоп | **0.125% от fill-цены BTC** | LONG: `fill × 0.99875`; SHORT: `fill × 1.00125`. Reduce-only. **Не** `% equity` |
| Выход A | Стоп сработал → flatten **сразу** | Fill моделируется точно −0.125% цены; live — exchange stop / trigger |
| Выход B | Стоп не сработал → **flat на `ts+300`** | Как раньше, чуть раньше границы (−0.3s) |
| Тейк-профит | **Нет** | |
| Notional | **100% текущей equity** | `qty = equity / entry_price` (1x) |
| Leverage | **1.0** | Не больше. Если когда-нибудь 3x — **тот же** price stop 0.125% (удар по депозиту станет 0.375% сам) |
| Комиссия модели | **0%** | Lighter standard account на момент research: 0% maker/taker. Live всё равно логировать факт |
| Funding | Игнорировать в v1 | 5 минут, 1x — шум относительно 1–2 bps edge |
| Старт equity (research) | **$1000** | Live — фактический баланс Lighter (collateral) |
| Compounding | **Да** | Каждая сделка от **текущей** equity после предыдущей |
| Инструментов | Только **BTC** | |
| Макс позиций | **1** | |
| Вход в уже идущее окно после рестарта | **Нет** | Только на границе следующего |

Пороги 0.70 / 0.75 **не** этот пакет. Они уже посчитаны отдельно (меньше сделок, выше WR, меньше compounding). Не смешивать в одном боте без нового startpack.

---

## 3. Две вселенные цен (не перепутать)

| Слой | Что это | Роль в боте |
|------|---------|-------------|
| A. Polymarket Up/Down | Вероятностная цена исхода 5m-окна, ~0.01–0.99 | **Только сигнал / гейт** |
| B. Lighter BTC perp | Линейная цена BTC | **Только исполнение и PnL** |
| C. Binance BTCUSDT 1s/5m | Research-прокси fill и close | **Тень / сверка**, не обязательный live-venue |
| D. Chainlink (резолв PM) | Официальный Up/Down Polymarket | **Не** наш settlement. Мы не редеемим PM |

Мы **не** пытаемся угадать резолв Polymarket. Мы используем то, что стакан PM на open уже «кричит» направление, и снимаем линейный ход BTC до конца тех же 5 минут.

**Oracle mismatch сознательный.** PM резолвится по Chainlink stream; Lighter — по своему mark/last. На горизонте 5m это почти всегда тот же знак крупного хода. Расхождения «PM Up, а Lighter close < open на 1 тик» возможны — это стоимость модели, не баг. Не «хеджировать» их сделкой на PM.

---

## 4. Часы и идентификатор окна

### 4.1 Сетка

Все окна UTC, без TZ-сдвигов:

```text
ts = floor(unix_now / 300) * 300
current_window = [ts, ts+300)
next_window    = [ts+300, ts+600)
```

Примеры slug:

```text
btc-updown-5m-1795090500   → 2026-11-19 12:15:00Z … 12:20:00Z
```

Суффикс slug **и есть** `ts` открытия. Не брать `endDate` из Gamma как id.

### 4.2 Синхронизация времени

Бот обязан:

1. Использовать системные UTC-часы машины, синхронизированные через NTP / chrony. При деплое подтвердить `Leap status: Normal` и доступный источник синхронизации.
2. Offset к Binance можно измерять как независимую справочную диагностику. Сам Binance time endpoint не является runtime-зависимостью: его timeout или большое расхождение не блокирует торговлю и не вызывает alert.
3. Планировать wake-up на `ts - 15s` (прогрев) и `ts` (решение), не «опрос раз в 5 секунд».

### 4.3 Прогрев (обязателен)

Рынки следующих окон **существуют заранее** (минуты–часы) и уже имеют книгу, часто около 0.50/0.50. Poly-trader это знал: warm-up подписки, чтобы к open не было cold-start.

За **15–30 с до `ts`**:

1. Резолв события `btc-updown-5m-{ts}` (Gamma).
2. Достать `clobTokenIds` Up и Down.
3. Подписаться на CLOB market websocket **и** REST fallback.
4. Держать last bid/ask Up в памяти.

На `ts` вы **не начинаете искать рынок**. Вы уже в стакане.

За 30 с до open цена **будущего** окна часто ещё ~0.50. Гейт **0.65 применяется только к котировке в момент `ts` (и первые сотни мс)**, не к pre-open 0.50.

---

## 5. Как получить цену Polymarket (сигнал)

### 5.1 Резолв рынка

REST:

```text
GET https://gamma-api.polymarket.com/events/slug/btc-updown-5m-{ts}
```

Из `markets[0]`:

- `outcomes` — `["Up","Down"]` (проверять регистр, искать `"up"` case-insensitive)
- `clobTokenIds` — JSON-строка или массив; индекс Up/Down совпадает с `outcomes`
- `outcomePrices`, `bestBid`, `bestAsk` — Gamma иногда отдаёт BBO, но **канон для гейта — CLOB book**, Gamma как fallback/сверка

Серия: `series_id=10684` (исторически). Не скрейпить UI.

Если slug 404 — **skip окна**, alert `no_market`. Не подставлять соседнее окно.

### 5.2 Каноническая котировка для гейта

Считать по **Up-токену**:

```text
bid_up = best bid (highest bid price)
ask_up = best ask (lowest ask price)
p_up   = (bid_up + ask_up) / 2          # mid
p_down = 1 - p_up                       # если нет отдельной книги Down
spread = ask_up - bid_up
```

Допустимо вместо mid взять Gamma `outcomePrices[up]` **только если** CLOB mid отсутствует, и залогировать `quote_source=gamma`.

**Нельзя** гейтить по:

- первому print из `clob.polymarket.com/prices-history` (это research-кэш, не live);
- `last_trade_price` без BBO;
- одной стороне книги (bid=0.01, ask=0.99 — это стены, не рынок).

### 5.3 Sanity котировки (без этого вход запрещён)

Котировка **валидна**, только если все пункты true:

1. `bid_up` и `ask_up` есть, числа в `(0, 1)`.
2. `ask_up >= bid_up`.
3. `spread <= 0.05` (5¢). Если шире — рынок пустой/сломан, **skip**.
4. `0.05 <= bid_up` и `ask_up <= 0.95` (отсекает мусорные стены 0.01/0.99 как «BBO»).
5. Возраст котировки: `now - quote_recv_ts <= 1.0s`. Старше — перезапросить, не решать по stale.
6. `abs((bid_up + (1 - bid_down)) / 2 - something)` — если подписаны оба токена: `| (bid_up + bid_down) - 1 |` не обязан быть 0, но если `bid_up + ask_down` совсем разъехались (> 0.08) — **skip** `quote_incoherent`.

### 5.4 Правило стороны

После валидного `p_up`:

```text
if p_up >= 0.65:           side = LONG
elif p_up <= 0.35:         side = SHORT    # Down >= 0.65
else:                      side = FLAT     # skip
```

Эквивалент: `fav = max(p_up, 1-p_up)`; если `fav < 0.65` skip; иначе сторона любимца.

Логировать оба: `p_up`, `p_down`, `fav`, `side`.

### 5.5 Почему мы верим, что на T+0 цена уже есть

Не потому что «так кажется». Потому что `poly-trader` на открытии окна:

- читал bid/ask;
- если цена **уже выше коридора** → `ENTRY_GATE_OUTSIDE` / patience;
- 140 раз за ~4 дня так и не дождался дешёвого входа → `entry_price_out_of_range`.

Значит **мы можем читать ту же книгу в ту же миллисекунду** и вместо skip на PM слать ордер на Lighter.

Исторический `t_first` в `windows.jsonl` (медиана 18 с, ≤1 с только 0.04%) **не использовать** как SLA. Это первый записанный trade в prices-history (часто fidelity/сэмпл), не first book.

---

## 6. Исполнение на Lighter

### 6.1 Инструмент

- Рынок: **BTC perpetual vs USD** (точное имя символа взять из Lighter API на момент внедрения: обычно BTC).
- Маржа: **1x**. Если биржа требует указать leverage — выставить **1**.
- Reduce-only на выходе: **да**.
- Post-only на входе: **нет** (нам нужна скорость, не maker rebate; fees и так 0).
- Тип входа: **taker market** или aggressive limit через спред (limit на `best_ask` для long / `best_bid` для short, IOC). Если за 300 ms не fill — добрать market. Не ждать 5 секунд.

Документация: https://apidocs.lighter.xyz/docs/get-started

### 6.2 Сайзинг

Пусть `E` — equity в USD **после** всех закрытий, **до** нового входа (collateral + UPNL=0, мы всегда flat между окнами).

```text
leverage     = 1.0
notional_usd = E * 1.0 * leverage      # = E
qty_btc      = notional_usd / px_entry
```

Округлить `qty` вниз до шага лота Lighter. Если после округления notional < 95% от `E` — alert `lot_rounding`, всё равно входить (не докидывать второй ордер).

**100% equity** значит: весь стек в одной сделке. Нет «риск 1% от депозита как сайз». Риск одной сделки при сработавшем стопе ≈ **0.125% × leverage** (на 1x это 0.125% стека), плюс проскальзывание стопа. Без стопа 5m-вынос мог быть любым; стоп это режет, **гэп через 0.125%** всё ещё возможен.

В research T+1s + стоп 0.125% max DD ≈ **2.2%** на 1x (без стопа было 2.9%). Это не гарантия live.

Кап notional в v1: **нет** (research так и считал). Если оператор хочет cap (например $10k) — это уже другой профиль, ломать compounding.

Минимум биржи: если `E` меньше min notional Lighter — не входить, alert `equity_too_small`.

### 6.3 Стоп 0.125% от цены BTC (обязателен)

Это **ценовой** стоп на Lighter, привязанный к **fill входа**, не к mid PM и не к % депозита.

```text
STOP_PRICE_PCT = 0.00125          # 0.125%

LONG:  stop_px = fill_px * (1 - STOP_PRICE_PCT)
SHORT: stop_px = fill_px * (1 + STOP_PRICE_PCT)

урон equity при точном fill стопа:
  equity_ret = -STOP_PRICE_PCT * leverage
  на v1 (1x) = -0.125% стека
```

**Как ставить live (порядок предпочтения):**

1. Сразу после входа — **native stop / trigger reduce-only** на Lighter на `stop_px` (один ордер, биржа держит).  
2. Если биржа не умеет stop на perp — локальный watchdog по last/mark **каждый тик**; при касании — market reduce-only. SLA: от касания до send **< 200 ms**.  
3. Не «проверить стоп раз в 5 секунд». На 5m 0.125% это весь стоп.

**Правила:**

- Стоп ставится **только после известного fill_px**. Не от pre-trade mid.  
- Один стоп на сделку. Не двигать (нет трейла).  
- Если стоп fill: **не перевходить** в том же окне. Ждать `ts+300` как flat.  
- Если к `ts+300-0.3s` стоп не сработал — снять стоп-ордер и flatten по рынку (выход B).  
- Гэп через стоп: принять фактический fill (может быть хуже −0.125%). Залогировать `sl_slip_bps`.  
- **Не умножать 0.125% на плечо.** 3x → тот же `stop_px`, удар по депозиту станет 0.375% сам. Research 3x + 0.125% цены: те же 868 стопов, DD ~6.5%. Research 3x + 0.375% цены: хуже ($38k vs $44k, DD 10.3%).

**Почему 0.125%, не 0.25% и не «без стопа»:**

Тот же T+1s / 0.65 / $1000 / 1x / 0% fee, стоп по Binance 1s high/low:

| Price stop | Стопы | WR | End | Max DD |
|------------|------:|---:|----:|-------:|
| нет | 0 | 57.73% | $3,479 | 2.9% |
| 0.25% | 216 (3.0%) | 57.58% | $3,566 | 3.0% |
| **0.125%** | **868 (11.9%)** | **56.25%** | **$3,557** | **2.2%** |
| 0.5% | 30 | 57.72% | $3,432 | 2.9% |
| 1% | 7 | 57.73% | $3,415 | 2.9% |

0.125% почти те же деньги, что 0.25%, при **меньшей** пиковой просадке. Шире 0.5%/1% уже хуже hold-to-close. v1 берёт **0.125% цены** (ровность стека, не максимум $).

Дашборд: `pm-expensive-spot-delay-1s-sl0125.canvas.tsx`. Скрипт: `scripts/analyze_pm_expensive_spot_delay1s_sl1.py --sl 0.00125`.

### 6.4 Вход: таймлайн одного окна

```text
ts-30s   resolve market, WS subscribe, cache book
ts-5s    health: clock, Lighter ping, position MUST be flat
ts+0ms   snapshot p_up (WS). если stale — REST book сразу
ts+0..200ms  sanity + side
         if FLAT: log skip, sleep until ts+300-2s (flatten noop)
         if LONG/SHORT: place Lighter order NOW
ts+2s    если ордер не sent/не fill → cancel, skip, NEVER chase later in the window
         после fill: place reduce-only stop at fill ± 0.125%
         if stop fills: flatten done, stay FLAT until next window (no re-entry)
ts+300-250ms  если ещё в позиции: cancel stop, begin flatten
ts+300   must be flat. if not: emergency market close, alert `exit_late`
ts+300+  next window is a NEW decision; не переносить сторону
```

**Не держать позицию через границу.** Окно N+1 имеет свой гейт. Если в N были LONG, а в N+1 skip — мы уже flat. Если в N+1 снова LONG — открыть заново (даже если «логически та же сторона»): это новая сделка, новый сайз от новой equity.

Практический выход чуть **раньше** `:00` (200–300 ms), чтобы не столкнуться с входом следующего окна. Research close = Binance 5m close, который совпадает с границей. Смещение выхода на 300 ms для 5m — шум vs опоздание входа на 5 с.

### 6.5 PnL

```text
ret = side_sign * (px_exit / px_entry - 1)
      side_sign = +1 LONG, -1 SHORT
pnl = notional_usd * ret          # при 1x
E   = E + pnl - fees_actual
```

Комиссии live записать факт, даже если 0. Не подмешивать PM fees.

### 6.6 Что если Lighter лагает / reject

| Ситуация | Действие |
|----------|----------|
| Reject входа | skip окна, не ретраить после T+2s |
| Частичный fill | оставить частичный размер, не добирать после T+2s; выйти на close тем что есть |
| Нет flatten | retry market каждые 200 ms до flat; пейджер |
| Рестарт с открытой позицией | **немедленно flatten** по reduce-only, не анализировать PM; следующее окно — заново |
| Рестарт без позиции в середине окна | ждать следующей границы, **не** входить «опоздавшим» |
| Стоп не встал после fill | **немедленно** flatten, alert `stop_missing`; не сидеть голым 5m |
| Стоп fill хуже 0.125% | принять, записать slip; не «догонять» |

---

## 7. State machine (единственный источник правды)

Состояния: `IDLE`, `ARMING`, `DECIDE`, `IN_POSITION`, `FLATTENING`, `HALT`.

```text
IDLE
  on (now >= next_ts - 30s) → ARMING

ARMING
  resolve slug, subscribe, wait
  on (now >= ts) → DECIDE
  on (error no_market / clock_bad) → skip, IDLE until next_ts+300

DECIDE  (max wall 2s)
  snapshot quote
  invalid → skip, IDLE (ждать flatten-noop к концу, можно сразу IDLE до ts+300-0.3s)
  fav < 0.65 → skip, IDLE
  fav ≥ 0.65 → send Lighter → IN_POSITION
  timeout 2s → skip, IDLE

IN_POSITION
  stop working on exchange / local trigger
  on stop fill → FLATTENING (exit_reason=sl), no re-entry this window
  on (now >= ts+300-0.3s) → cancel stop, FLATTENING (exit_reason=window_close)
  on disconnect/halt → FLATTENING first

FLATTENING
  reduce-only until qty=0
  update equity
  → IDLE (next window)

HALT
  flatten if needed, stop entries
  ручной выход
```

Инварианты (нарушение = баг, стоп-кран):

1. Никогда две позиции.
2. Никогда позиция с `entry_window_ts != current_or_last_open_window`.
3. Никогда вход, если `now - ts >= 2s`.
4. Никогда вход без валидного PM quote.
5. Между окнами `abs(position_qty) == 0`.
6. Пока есть позиция — есть рабочий reduce-only стоп на `fill ± 0.125%` (или halt/flatten).

---

## 8. Псевдокод цикла (вставить в бота почти как есть)

```python
TH = 0.65
MAX_ENTRY_LAG_S = 2.0
PREWARM_S = 15.0
EXIT_EARLY_S = 0.30
LEVERAGE = 1.0
MAX_SPREAD = 0.05
STOP_PRICE_PCT = 0.00125   # 0.125% of BTC fill; do NOT * leverage

def window_ts(t):
    return int(t) - (int(t) % 300)

while running:
    now = time.time()
    ts = window_ts(now)
    if now < ts + 300 - PREWARM_S and not armed_for(ts + 300):
        arm(ts + 300)          # следующий, если текущее уже идёт
        sleep_until(ts + 300 - PREWARM_S)

    ts = window_ts(time.time())
    if time.time() - ts >= MAX_ENTRY_LAG_S:
        # опоздали на это окно
        sleep_until(ts + 300 - EXIT_EARLY_S)
        ensure_flat()
        continue

    sleep_until(ts)            # если ещё не ts
    q = snapshot_pm_up(ts)     # WS cache, else REST
    if not quote_ok(q, max_spread=MAX_SPREAD, max_age_s=1.0):
        log_skip("bad_quote", q)
        sleep_until(ts + 300 - EXIT_EARLY_S)
        ensure_flat()
        continue

    p_up = (q.bid + q.ask) / 2
    if p_up >= TH:
        side = +1
    elif p_up <= 1 - TH:
        side = -1
    else:
        log_skip("below_th", p_up)
        sleep_until(ts + 300 - EXIT_EARLY_S)
        ensure_flat()
        continue

    if time.time() - ts >= MAX_ENTRY_LAG_S:
        log_skip("late")
        continue

    E = lighter.equity()
    px = lighter.btc_px()
    qty = floor_lot((E * LEVERAGE) / px)
    fill = lighter.market_open(side, qty)
    stop_px = fill * (1 - STOP_PRICE_PCT) if side > 0 else fill * (1 + STOP_PRICE_PCT)
    lighter.place_reduce_only_stop(stop_px)
    log_entry(...)

    while time.time() < ts + 300 - EXIT_EARLY_S:
        if not lighter.has_position():
            log_exit("sl")
            break
        time.sleep(0.05)
    else:
        lighter.cancel_stop()
        lighter.market_close_all()
        log_exit("window_close")
```

---

## 9. Логи (без них бот нельзя считать запущенным)

Одна строка JSONL на окно, плюс отдельный JSONL на ордер-события.

Минимум полей на окно:

```text
window_ts                 int
decision_ts               float   unix
quote_ts                  float
p_up p_down spread        float
quote_source              ws_clob | rest_clob | gamma
side                      long | short | skip
skip_reason               below_th | bad_quote | late | no_market | clock | flatten_busy | ...
lighter_entry_ts          float | null
lighter_entry_px          float | null
stop_px                   float | null
exit_reason               sl | window_close | skip | halt | stop_missing
lighter_exit_ts           float | null
lighter_exit_px           float | null
qty notional equity_before equity_after
pnl_usd ret_bps
sl_slip_bps               насколько fill стопа хуже −12.5 bps цены; null если не sl
fees_usd
entry_lag_ms              decision_ts - window_ts, и fill_ts - window_ts
shadow_binance_px_t1      optional, 1s close at ts+1
shadow_ret_bps            optional, тот же side, binance t+1 → 5m close
pm_fav                    max(p_up, p_down)
```

Метрики, которые оператор смотрит **вместо** красивого equity (см. §12):

1. coverage: доля окон с валидной котировкой на T+0..1s  
2. p50 `entry_lag_ms`  
3. p50 `fill_px vs binance_1s_close(ts+1)` в bps  
4. live mean bps vs shadow T+1s mean bps **на тех же окнах**  
5. WR live vs WR shadow  
6. n  
7. доля `exit_reason=sl` vs research **11.9%**; live << 5% при том же BTC = стоп не стоит / не триггерится  
8. p50 `sl_slip_bps` — если стопы систематически хуже −0.125% на 1–2 bps, edge стопа съеден

---

## 10. Research-якорь (ожидания, не KPI «догнать $»)

Период: **2026-02-12 → 2026-11-16**.  
Правило: Up≥0.65 / Down≥0.65, вход = Binance **1s close на T+1s**, fee 0%, start $1000, 100% equity, 1x.

**Канон v1 (этот пакет):** выход = стоп **0.125% цены** по 1s high/low, иначе 5m close.

| | T+1s, без стопа | **T+1s, стоп 0.125% (v1)** |
|--|--:|--:|
| n | 7,296 | **7,296** |
| Стопы | 0 | **868 (11.9%)** |
| WR | 57.73% | **56.25%** |
| End equity | $3,479 | **$3,557** |
| Return | +248% | **+256%** |
| Max DD | 2.9% | **2.2% ($52.48)** |
| Худшая сделка | −$27.64 | **−$4.44** |

Без стопа, для справки (не v1): T+0 open $5,785; T+1s $3,479; T+3s $2,690; T+5s $2,378.

Пери-сделка T+1s 0.65 **без стопа** (масштаб хода; v1 со стопом те же входы, 12% из них режутся на −12.5 bps):

| | |
|--|--|
| mean | **+1.72 bps** |
| median | **+1.07 bps** |
| mean win | +8.0 bps |
| mean loss | −6.9 bps |
| 95% CI WR | ≈ 56.6–58.9% (n=7296) |

По месяцам T+1s 0.65 все зелёные; почти весь compounding в **окт–ноя 2026**. Не сравнивать неделю live с +256% за 9 месяцев.

Соседние пороги (тот же T+1s, 1x, 0%):

| Порог | n | WR | End |
|------|--:|--:|--:|
| 0.65 | 7296 | 57.7% | $3479 |
| 0.70 | 3321 | 59.6% | $2393 |
| 0.75 | 1424 | 62.3% | $1677 |

v1 = **0.65**. Выше порог — не «безопаснее стратегия», а **другая частота**.

Дашборды research (этот репо, canvases):

- `pm-expensive-spot-delay-1s-sl0125.canvas.tsx` — **канон v1 (стоп 0.125%)**
- `pm-expensive-spot-delay-1s.canvas.tsx` — T+1s без стопа (справка)
- `pm-expensive-spot-delay-1s-sl025.canvas.tsx` / `sl05` / `sl1` — другие стопы, не v1
- `pm-expensive-spot-delay-1s-p070.canvas.tsx` / `p075` — не v1
- open-entry без задержки: `pm-expensive-spot-5m.canvas.tsx` (верхняя оценка, T+0 по 5m open)

Данные PM: `data/polymarket/btc_updown_5m/windows.jsonl` + `windows_backfill.jsonl`. Поле `p_up_open` — **первый history print**, не live book. Для бота это **не** источник сигнала.

---

## 11. Связь с poly-trader (что копировать, что нет)

| Копировать | Не копировать |
|------------|----------------|
| Умение резолвить `btc-updown-5m-{ts}` | `blend_fade2` |
| CLOB WS + sanity котировок | Коридор `(0, 0.57]`, patience, IDEA3 scale |
| Прогрев подписки до open | Покупка шаров PM, redeem, merge |
| Понимание, что на open уже 0.58–0.65 | Stake 1.5% депозита как бинарная ставка |
| Логи skip-причин | Fade (покупка дешёвой ноги) |

Poly-trader на дорогом open **не торговал**. Мы на дорогом open **торгуем Lighter**. Это сознательная инверсия продукта, не баг относительно IDEA3.

---

## 12. Как понять, что бот работает (не equity)

Живой депозит **не** сравнивать с $3,557.

Хороший бот:

1. На аптайме coverage валидных котировок на границе **> 95%** окон.  
2. p50 `fill_ts - window_ts` **< 1000 ms**, p90 **< 2000 ms**.  
3. Сторона совпадает с тем, что дал бы mid PM на первом валидном снапшоте после `ts`.  
4. Mean bps live на горизонте **≥ 500–1000 сделок** порядка **+1…+2 bps**, и не хуже shadow T+1s на тех же окнах больше чем на ~1.5 bps.  
5. Max DD 1x не «8–10% за неделю без выноса BTC» — иначе вы не тот процесс (опоздания / двойные позиции / не тот символ / стоп не работает). Research со стопом 0.125%: **~2.2%**.

Красный:

- входы в середине окна;
- mean bps ≤ 0 на ≥ 400–500 сделках при нормальном BTC;
- live mean хуже shadow на ≥ 1.5 bps при том же side;
- coverage 70% «счастливых» окон.

На 200 сделках WR 50–65% — шум (SE ≈ 3.5 п.п.). Не крутить порог.

---

## 13. Риски, которые этот пакет **не** отменяет

1. **Линейный хвост / гэп.** Стоп 0.125% режет обычный 5m-вынос, но не гэп через уровень. 1x: стоп ≈ −0.125% стека **если fill на уровне**; иначе хуже. Без рабочего стопа снова −4% за 5m = −4% депозита.  
2. **Исполнение Lighter.** 0% fee ≠ 0 spread. 1–2 bps проскальзывания ≈ весь mean edge.  
3. **Задержка.** Каждая лишняя секунда после open в research съедала compounding (T+1 → T+5: $3479 → $2378 на той же выборке).  
4. **Режим.** Окт–ноя 2026 разогнали результат. Тихие месяцы почти плоские.  
5. **Гейт 0.65 редкий в «скучных» часах, частый когда толпа уже уверена.** n по месяцам в research очень неровный.  
6. **Операционка.** Рестарт на границе окна = пропуск или двойной вход. Нужен lock на `window_ts` в БД (unique).  
7. **Не тот BTC.** PM Chainlink vs Lighter mark: редкие разногласия знака.  
8. **API Gamma/CLOB down.** Нет котировки — нет сделки, не «войти по Binance направлению».

Стоп-кран оператора:

- `HALT` если 3 окна подряд `exit_late` или `entry_unfilled_after_send`;
- `HALT` если позиция жива > 6 минут;
- `HALT` если equity drop > 8% от пика (ручной разбор; в research со стопом DD ~2.2%, 8% = не модель);
- `HALT` если 10 окон подряд в позиции без рабочего стоп-ордера.

---

## 14. Рекомендуемые env (новый репо)

Имена можно сменить, семантика нет.

```text
POLY_LIGHTER_PROFILE=POLY_LIGHTER_1
PM_SERIES_SLUG=btc-updown-5m
PM_THRESHOLD=0.65
PM_MAX_SPREAD=0.05
PM_PREWARM_SECONDS=15
ENTRY_MAX_LAG_SECONDS=2.0
EXIT_EARLY_SECONDS=0.30
STOP_PRICE_PCT=0.00125
LIGHTER_SYMBOL=BTC
LIGHTER_LEVERAGE=1
LIGHTER_EQUITY_FRACTION=1.0
LIGHTER_API_URL=...
LIGHTER_API_KEY=...
LIGHTER_API_SECRET=...
SHADOW_BINANCE_1S=1
HALT_ON_EXIT_LATE_STREAK=3
HALT_DD_PCT=0.08
```

Секреты не коммитить. Paper-режим: те же решения, ордера Lighter не слать (или testnet, если есть).

---

## 15. Порядок внедрения (не пропускать ступени)

1. **Clock + PM only.** 24 часа: прогрев, снапшот на каждой границе, JSONL без Lighter. Проверить: котировка на `ts` есть, `t_first` вам не нужен, будущие окна резолвятся заранее.  
2. **Shadow fill.** На тех же снапшотах виртуальный PnL: Binance 1s T+1 вход, стоп 0.125% по 1s high/low, иначе 5m close. Сверить с §10 (~11.9% стопов, не «почти никогда»).  
3. **Lighter paper / tiny size.** 1–5% equity, не 100%. Сверить lag, slippage входа и **факт fill стопа**.  
4. **1x 100%** только если p50 lag < 1s, стоп реально стоит после каждого fill, и mean slippage vs T+1s не съедает 1.7 bps.  
5. Не включать 5x, ETH, 0.70, patience, стоп 0.5%/1%, умножение стопа на плечо.

---

## 16. Чеклист приёмки (можно ли включать деньги)

- [ ] Вход **только** если `p_up>=0.65` или `p_up<=0.35` на **валидном mid**, inclusive  
- [ ] Skip логируется, а не молчит  
- [ ] Нет входа после T+2s  
- [ ] Нет входа в уже открытое окно после рестарта  
- [ ] Выход до следующей границы, всегда flat  
- [ ] После fill сразу reduce-only стоп **0.125% цены BTC** (не % депозита, не × leverage)  
- [ ] Стоп fill → нет re-entry в том же окне  
- [ ] Сайз = 100% equity 1x, compound  
- [ ] Не шлём ордера на Polymarket  
- [ ] Unique `window_ts` в БД  
- [ ] Рестарт flatten  
- [ ] JSONL полей §9  
- [ ] Тени T+1s пишутся  
- [ ] Стоп-кран §13  

Если любой пункт «потом» — это **не** `POLY_LIGHTER_1`.

---

## 17. Формула, которую нельзя «улучшить» в v1

```text
на каждом ts кратном 300:
  p = mid(Up на Polymarket, stale < 1s, spread ≤ 5¢)
  если p ≥ 0.65:  Lighter LONG  100% equity 1x
  если p ≤ 0.35:  Lighter SHORT 100% equity 1x
  иначе: skip
  стоп: fill × 0.99875 (long) / fill × 1.00125 (short)  — 0.125% ЦЕНЫ BTC
  если стоп: flatten, ждать следующего окна
  иначе в ts+300: flatten
  fee: 0 (факт логировать)
```

Всё, что не в этой формуле (фильтры волатильности, «не входить в пятницу», SL 0.5%/1%, стоп × плечо, ждать 0.70 если было 0.64), есть **другая стратегия** и требует нового startpack.

---

## 18. Источники

| Документ | Зачем |
|----------|--------|
| `poly-trader/IDEA3.md` | Дорогая цена **на open**, 140 skip, WR 62.9% |
| `poly-trader/docs/BACKTEST_VS_LIVE_GAP.ru.md` | Live читает bid/ask сразу после сигнала/open |
| `poly-trader/docs/ENTRY_MECHANISM.ru.md` | `ENTRY_IMMEDIATE` vs patience на open |
| `poly-trader/IDEA2.md` | Lighter как 0% fee perp, не binary |
| Этот репо: `scripts/analyze_pm_expensive_spot.py` | T+0 5m open follow |
| Этот репо: `scripts/analyze_pm_expensive_spot_delay5s.py` | T+Ns 1s-close follow (`--delay 1`) |
| Этот репо: `scripts/analyze_pm_expensive_spot_delay1s_sl1.py --sl 0.00125` | T+1s + стоп 0.125% |
| Canvases `pm-expensive-spot-delay-1s-sl0125.canvas.tsx` | Канон v1 §10 |

Конец `POLY_LIGHTER_STARTPACK_1`.
