# POLY_LIGHTER_1 — стратегия и текущая реализация

Документ описывает **задумку**, **алгоритм простыми словами** (§4) и **как это отражено в коде** (§5+).  
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

## 4. Алгоритм простыми словами

Ниже — **что бот делает и зачем**, без привязки к именам функций.  
Техническая карта тех же шагов — в §5.

### 4.1 Картина мира

Время нарезано на **пятиминутки по UTC** (…:00, :05, :10, …).  
Каждая пятиминутка — отдельное решение: либо входим в BTC на Lighter, либо пропускаем окно.

На Polymarket в эти же пять минут идёт рынок «BTC вверх или вниз».  
Мы **не** ставим там. Мы только **смотрим цену** токена Up/Down в момент открытия окна: насколько рынок уже «уверен» в направлении.  
Если уверенность высокая (≥ 65¢ на одной стороне) — берём **то же направление** линейным фьючерсом на Lighter до конца этих пяти минут (или раньше, если сработал стоп).

Почему так: edge живёт в **первых секундах** после открытия. Опоздал на пару секунд — это уже другая сделка. Поэтому жёсткий дедлайн входа: **не позже чем через 2 секунды** после открытия окна. Догнать вход посередине окна **запрещено**.

### 4.2 Подготовка до открытия (примерно за 15–30 секунд)

Бот заранее:

1. Находит рынок Polymarket на эту пятиминутку.
2. Подписывается на живой стакан (чтобы к :00 уже была свежая котировка, а не «сходить в API в панике»).
3. Смотрит баланс и цену BTC на Lighter, проверяет что счёт **пустой** (нет чужой/застрявшей позиции).
4. Готовит подпись/nonce для ордера, чтобы в момент открытия не тратить драгоценные миллисекунды.

Если подготовка не удалась — это ещё не трагедия: в момент открытия бот попробует ещё раз. Но чем лучше прогрев, тем выше шанс уложиться в 2 секунды.

### 4.3 Момент открытия — решение «входить или нет»

Ровно на границе окна бот спрашивает себя по порядку:

1. **Уже решали это окно?** (рестарт / повтор) — тогда ничего не делаем второй раз.
2. **Стоп-кран включён?** — только skip, без сделок.
3. **Часы хоста ещё «до» открытия?** — странная ситуация; пропускаем.
4. **Уже позже 2 секунд после открытия?** — опоздали; окно пропускаем. **Никогда не догоняем.**
5. **Есть ли рынок и живая котировка Polymarket?** Спред нормальный, стакан не мусорный, котировка не старше ~1 с, Up и Down не противоречат друг другу. Нет — skip.
6. **Кто дороже?**
   - Up ≥ 0.65 → хотим **лонг BTC** на Lighter;
   - Down ≥ 0.65 (то есть Up ≤ 0.35) → хотим **шорт BTC**;
   - обе стороны «дорогие» сразу — невозможный нормальный стакан → skip + тревога;
   - никто не дотянул до 0.65 → **skip**. Это самый частый и нормальный исход, не ошибка.
7. **Готовы ли баланс и лот?** Есть свежий equity и цена, размер ордера не меньше минимума биржи. Иначе skip.
8. Если всё ок и дедлайн ещё не прошёл — **отправляем рыночный (агрессивный) вход на Lighter** на почти весь депозит при плече 1x.

Смысл гейта: мы не «угадываем» направление сами. Мы **следуем** за уже дорогой стороной Polymarket и переносим это направление на линейный BTC.

### 4.4 Вход и подтверждение сделки

Ордер ушёл — дальше важно **не обмануться ответом сети**:

- Биржа явно отказала → окно пропущено, повторной попытки после дедлайна нет.
- Ответ «потерялся» (таймаут, обрыв связи) → **нельзя** считать, что ордера не было. Бот проверяет ленту сделок и реальную позицию на счёте.
- Позиция странно большая → аварийно закрываем и стоп-кран (что-то пошло не так).
- Филла нет → пытаемся убедиться, что счёт плоский, стоп-кран: состояние биржи было неоднозначным.
- Филл частичный → **живём с тем размером, что получили**; не добираем «до полного» после дедлайна.

Размер сделки: весь доступный equity (1x), округлённый до лота биржи. Если из‑за округления получилось заметно меньше 100% — всё равно входим одним ордером, без «добивки».

### 4.5 Сразу после входа — защитный стоп

Как только известна цена входа, бот ставит **reduce-only стоп на 0.125% цены BTC** от филла:

- лонг: чуть ниже входа;
- шорт: чуть выше входа.

Это **не** «0.125% депозита» и **не** стоп, умноженный на плечо. Плечо 1x — удар по депозиту примерно равен ходу цены.

Если стоп **не встал** — позицию немедленно закрываем и включаем стоп-кран. Сидеть «голым» пять минут нельзя: один резкий ход съест смысл всей стратегии.

После удачной постановки стопа бот просто **держит** позицию до одного из исходов ниже. Повторно входить в том же окне нельзя.

### 4.6 Пока сидим в позиции

Примерно раз в секунду бот смотрит:

- **Сработал ли стоп на бирже** (или цена уже пробила уровень — локальная подстраховка)? Тогда фиксируем выход по стопу и до конца окна больше ничего не открываем.
- **Закрыл ли кто-то позицию не нами** (другой бот на том же счёте, ручное закрытие)? Это тревожный сигнал `closed_away`.
- **Не висит ли позиция дольше ~6 минут** (дольше одного окна)? Стоп-кран.
- **Не пора ли выходить к концу пятиминутки?** За ~0.3 с до границы снимаем стоп-ордер и закрываем рыноком, чтобы к :00/:05/… уже быть плоскими.

Параллельно, ближе к концу окна, бот может тихо прогреть **следующую** пятиминутку — но текущую позицию это не меняет.

### 4.7 Выход и «между окнами»

Выходы ровно двух типов по задумке:

1. **Стоп** — рынок пошёл против нас на 0.125% цены; выходим сразу.
2. **Конец окна** — стоп не сработал; закрываемся около границы пятиминутки.

Должны стать **полностью плоскими** к границе. Если не успели — это `exit_late`: плохо для соседних окон и для учёта; несколько раз подряд → стоп-кран.

После закрытия:

- считаем PnL по факту Lighter (комиссии логируем);
- для контроля качества асинхронно считаем «тень» на Binance (как будто вошли по 1s-цене) — это **не** торговля, а сравнение;
- переходим к ожиданию / прогреву **следующего** окна. Каждое окно — **новое** решение и новый размер от текущего equity (compounding). Сторону «с прошлого раза» не переносим автоматически.

### 4.8 Старт бота и аварии — тоже часть алгоритма

При включении:

- если на счёте уже есть позиция — **сразу закрываем**, Polymarket не анализируем (мы не знаем, чья это позиция и от какого окна);
- если поднялись посередине окна — **не** входим «как успеем»; ждём следующей границы;
- если включён kill-switch / ранее был halt — только ждём ручного `/resume`.

Любая неоднозначность с биржей (ордер мог пройти, а мы не уверены) трактуется осторожно: сначала выяснить состояние счёта, при сомнении — закрыть и остановить входы, а не «сделать вид, что ничего не было».

### 4.9 Что сознательно не делаем (чтобы не размыть edge)

Не торгуем на самом Polymarket. Не fade (не берём дешёвую сторону). Не ждём, пока цена «дойдёт» до 0.65 внутри окна. Не ставим тейк и трейл. Не усредняем и не пирамидим. Не берём сигнал с Binance/Donchian. Не держим позицию через границу пятиминутки «потому что ещё выглядит хорошо».

---

## 5. Жизненный цикл одного окна (как в коде)

То же самое, что в §4, но в терминах текущего кода.  
Точки входа: `engine.Bootstrap` → `engine.Run` → `tick` / `waitNextOpen` / `decide` / `settleWindow`.

### 5.1 Timeline

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

### 5.2 ARM (`arm`)

1. `PM.Resolve(EventSlug(ts))` через Gamma.
2. Fail → ошибка вверх / skip `no_market` на decide.
3. `PM.Arm` (WS) + параллельно `WarmREST` и `PrepareNonce` (live).
4. Ошибка arm в prewarm — Warn; decide всё равно попробует arm снова.

### 5.3 DECIDE — порядок гейтов (`decide`)

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

### 5.4 ENTRY / FILL

1. `Signer.MarketIOC` с deadline `ts+EntryMaxLag`.
2. Persist window `state=sent` + order row.
3. Чистый reject → skip `order_failed`.
4. `IsAmbiguousSubmit` → **не** skip; reconcile через `confirmLiveFill`.
5. Oversize позиции → flatten + halt `unexpected_entry_size`.
6. `fillQty≤0` → flatten + CancelAll (если flat) + halt `entry_unconfirmed`, skip reason `entry_unfilled_after_send`.
7. Partial fill → держать фактический size, не добирать после deadline.

### 5.5 STOP

1. `strategy.StopPx(fill, side, StopPricePct)`.
2. `Signer.StopLoss` ≤ 1.5s.
3. Fail → flatten + CancelAll + halt `stop_missing`.
4. Telemetry `OrderByClient` **после** постановки стопа.

### 5.6 HOLD (`tick` при `pos != nil`)

Каждую ~1s:

- `detectExit`: grace 2s после `ArmedAt`; `SeenAcctOpen` выставляется при первом confirmed open; flat без ever-open допускается только после age≥5s (DNS мог скрыть open). Flat → VWAP closing fills → `stop` / `closed_away`.
- `resolveStopWatchdog` (live): last/mark `StopHit` → poll account ~350ms. Flat → settle remote (tape VWAP, без market close). Open → settle residual (CancelAll + market). Unknown → settle keep-stop (market **без** CancelAll, чтобы не убить live SL). Grace 2s.
- Age &gt; `HaltPositionMax` (default 6m) → halt + settle `halt_age`.
- `now ≥ ts+300−ExitEarly` → settle window close.
- Prewarm следующего окна, если позиция ещё жива.

### 5.7 EXIT / SETTLE (`settleWindow` → `finalizePosition`)

Flatness **трёхзначная**: `flat` / `open` / `unknown` (сеть). Ошибка DNS ≠ «ещё в позиции».

1. State `FLATTENING`.
2. Для `stop`/`closed_away` — сначала tape VWAP (предпочтительнее last/mark).
3. План close (`planSettleClose`):
   - confirmed flat → CancelAll residual, без market;
   - stop/remote + confirmed open → CancelAll + market;
   - stop/remote + unknown + есть stop tape → как flat (не дублировать close);
   - stop/remote + unknown без tape → market **без** CancelAll;
   - window/halt → CancelAll + market.
4. `exit_late` + `bumpLateStreak` **только** если финальный poll = confirmed open. Unknown → alert `exit_flat_unknown`, streak не трогаем.
5. PnL, `ExitReason`: `sl` | `closed_away` | `halt` | `window_close` | …
6. `pos=nil`, shadow async, `waitNextOpen(next)` → `decide(next)`.

**Инвариант границы:** `window_close` не должен съедать T+2 следующего окна. На `nextSoon` — cancel+IOC без длинного ConfirmFill/tape wait; exit px/equity дожимает `refineExitAsync`. Arm next — до `exitAt` (не отрезать за 2s).

---

## 6. Деревья всех сценариев

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
confirmed open к ts+300 ──────► exit_late (+ streak→HALT)
flatness unknown ─────────────► warn exit_flat_unknown (не streak)
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
Стоп на dry-run симулируется (paper stop) и явно маркируется.

---

## 7. Skip / exit reasons

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
| `venue` | Lighter недоступен до send (DNS/dial) или preflight fail — skip без HALT |
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
| `exit_late` | confirmed open к границе (не DNS/unknown) |

---

## 8. Halt / Kill

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

## 9. Параметры (env)

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

## 10. Карта код ↔ стратегия

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

## 11. Известные расхождения / принятые решения

| Тема | Контракт / docs | Код / решение |
|------|-----------------|---------------|
| Канон | `_1_1` со стопом | Live следует `_1_1` |
| README historically | one-liner без стопа | должен ссылаться на `_1_1` + этот файл |
| Clock gate `_1` | skip если Binance offset &gt;1s | код: host UTC; Binance не блокирует вход (`_1_1`) |
| Local stop watchdog | SLA &lt;200ms | exchange stop + `detectExit` + `resolveStopWatchdog` (poll flat → remote/residual/keep-stop); `exit_late` только на confirmed open |
| window_close vs T+2 | flatten ≤300ms до :00 | sync path on boundary must not ConfirmFill 800ms / tape 1.5s; else next window `late` |
| HALT 10 окон без стопа | в `_1_1` | заменено на immediate `stop_missing` |
| Gamma synthetic book | CLOB канон | live gate: только WS/REST CLOB; Gamma mid не подставляет synthetic book |
| Dry-run stop | risk model со стопом | paper path ставит `stop_loss` dry_run + `StopPx` в позиции |
| Ambiguous submit | reconcile | entry + stop + exit reconcile через `IsAmbiguousSubmit` + `ConfirmFill`; DNS/dial (`IsPreConnectFail`) → skip `venue`, не HALT |

---

## 12. Как понять, что бот «тот»

Хорошо: coverage &gt;95%; p50 fill lag &lt;1s, p90 &lt;2s; mean bps порядка research (~+1…+2); доля SL порядка ~12% (не ≪5%).  
Красный: mid-window entries; mean≤0 на ≥400–500 сделках; coverage ~70%; DD 8–10% за неделю без выноса BTC.  
На 200 сделках WR 50–65% — шум, **не крутить порог**.
