# Публикация POLY_LIGHTER_1 на том же сервере, что Donchian

Контракт стратегии: [`docs/POLY_LIGHTER_STARTPACK_1.md`](docs/POLY_LIGHTER_STARTPACK_1.md).  
Короткий README: [`README.md`](README.md).

Это **отдельный** Docker Compose, **отдельный** каталог, **отдельный** счёт Lighter, **отдельный** Telegram-бот, **отдельный** порт дашборда. Donchian (`/opt/donchian`) не останавливать и его `.env.a` не трогать.

**Уже стоит на VPS и нужно подтянуть код + полный сброс БД/логов:** §11 (один блок на SSH).  
**Снять логи + БД для разбора (бот не останавливать):** §11.1. Не копировать `.env.example` поверх живого `.env`.

| | Donchian | Этот бот |
|--|--|--|
| Каталог | `/opt/donchian` | `/opt/poly-lighter` |
| Compose-сервисы | `bot_a`, `web` | `bot`, `web` |
| Env-файл | `.env.a` | `.env` |
| Данные | `data/a/bot.db` | `data/bot.db` |
| Дашборд | порт **80** (`http://IP/`) | порт **8081** (`http://IP:8081/`) |
| Vite локально | 5173 | 5174 |
| Счёт Lighter | свой (BTC+ETH, 1% risk) | **другой** (BTC, 100% equity, 1x) |
| Telegram token | свой | **другой** (иначе 409 Conflict) |

На рестарте этот бот **flatten'ит** позицию на своём аккаунте. Если дать ему тот же `LIGHTER_ACCOUNT_INDEX`, что у Donchian, он закроет Donchian.

Выкат сразу на **mainnet**, ордера включены (`DRY_RUN=false`). Небольшой депозит и есть «tiny size»: при `LIGHTER_EQUITY_FRACTION=1.0` бот ставит 100% **этого** счёта, 1x. Не копируй ключи Donchian и не путай mainnet-ключ с чем бы то ни было из testnet.

---

## 0. Что должно быть на VPS уже (Donchian жив)

Проверь, не ломая прод:

```bash
cd /opt/donchian
docker compose ps
# bot_a Up (healthy), web Up

ss -lptn | grep -E ':80|:443|:8081'
# :80 и, если HTTPS, :443 — заняты Donchian/Caddy
# :8081 должен быть свободен
```

Нужны: Docker Engine + Compose plugin, git, ufw, chrony/NTP. Бот использует системные UTC-часы VPS; Binance time — только диагностический ориентир и не блокирует вход.

Если Docker ещё не ставили (чистый сервер — маловероятно рядом с Donchian):

```bash
curl -fsSL https://get.docker.com | sudo sh
sudo usermod -aG docker "$USER"
# перелогиниться
docker compose version
```

Часы:

```bash
timedatectl
# NTP synchronized: yes (обязательно)
# Time zone лучше UTC (как у Donchian)

# если есть тот же helper, что у Donchian:
sudo /usr/local/sbin/sync-clock-http
```

Место: свободно **≥ 3G** (`df -h /`). Если мало: `docker builder prune -a -f`.

---

## 1. Биржа: отдельный аккаунт Lighter (mainnet)

Сделай **до** клона, иначе легко вписать ключи Donchian.

1. Зайди в **mainnet** UI: [https://app.lighter.xyz](https://app.lighter.xyz).
2. Аккаунт **не** тот, на котором крутится Donchian. Субаккаунт / отдельный `account_index`.
3. **API keys:** индексы **0–3 заняты приложением**. Для бота создай ключ **4–254 уже в mainnet**. Не копируй testnet-ключ и не бери ключ Donchian.
4. Сохрани **private key** API (hex, с `0x` или без). Это как seed: кто имеет ключ — торгует счётом.
5. Узнай **account index** (не путать с `api_key_index`):

```bash
curl "https://mainnet.zklighter.elliot.ai/api/v1/accountsByL1Address?l1_address=0xВАШ_АДРЕС"
```

Поле `account_index`. Сверь, что это **не** индекс Donchian из `/opt/donchian/.env.a` (`LIGHTER_ACCOUNT_INDEX`).

6. Пополни **Available / Equity** на перпах небольшой суммой. Деньги в Public Pool / LLP — не маржа. Withdraw внутри Lighter на perp-счёт, не гонять через L1. Готово, когда в Perps видно Available ≈ твой депозит.

7. Если депозит совсем крошечный, биржа может отвергнуть ордер (min notional) — бот залогирует `equity_too_small` и пропустит окно. Тогда чуть пополни, не крути порог 0.65.

8. В UI позиция **FLAT**, нет висящих ордеров. На старте бот всё равно попытается flatten.

Mainnet REST: `https://mainnet.zklighter.elliot.ai`, chain id **304**.

---

## 2. Telegram: новый бот

1. `@BotFather` → `/newbot` → токен. **Не** копируй токен Donchian.
2. Напиши боту любое сообщение.
3. Chat id:

```bash
curl "https://api.telegram.org/bot<TOKEN>/getUpdates"
```

`message.chat.id` (у группы часто отрицательный).  
4. В `.env`: `TELEGRAM_BOT_TOKEN` и `TELEGRAM_CHAT_IDS` (несколько через запятую). Чужие чаты бот игнорирует.

Команды: `/health` `/status` `/trades` `/kill` `/resume` `/help`.  
Heartbeat — если молчал `TELEGRAM_HEARTBEAT_MINUTES` минут.

---

## 3. Firewall

Donchian уже открыл 22 / 80 / 443. Для дашборда на 8081:

```bash
sudo ufw allow 8081/tcp
sudo ufw status
```

Потом (когда привыкнешь) лучше не светить 8081 наружу, а проксировать с Caddy Donchian на `127.0.0.1:8081` — см. §9.

Дашборд **без** `DASHBOARD_PASSWORD` не стартует.

---

## 4. Код на сервер

Подставь URL репозитория. Каталог **не** `/opt/donchian`.

```bash
sudo mkdir -p /opt/poly-lighter
sudo chown "$USER:$USER" /opt/poly-lighter
cd /opt/poly-lighter
git clone <URL_ЭТОГО_РЕПО> .
cp .env.example .env
chmod 600 .env
mkdir -p data backups
```

`.env` **не коммитить**. Не `cp .env.example .env` второй раз на живом сервере — затрёшь ключи.

Compose читает **`.env`** (не `.env.a`). Имя сервиса бота — **`bot`**.

---

## 5. Заполнение `.env` (построчно)

Открой:

```bash
cd /opt/poly-lighter
nano .env
```

Ниже — канон v1. То, что «не трогать после настройки», помечено.

### 5.1 Профиль (оставить как в example)

```dotenv
POLY_LIGHTER_PROFILE=POLY_LIGHTER_1
INSTANCE_NAME=POLY_LIGHTER_1
```

Смена порога 0.70 / плеча / ETH = **другой startpack**, не крути здесь.

### 5.2 Lighter (заполнить руками)

Сразу **mainnet**:

```dotenv
LIGHTER_NETWORK=mainnet
LIGHTER_API_PRIVATE_KEY=0x...          # ключ индекса 4–254, mainnet, не Donchian
LIGHTER_ACCOUNT_INDEX=12345            # из curl §1, не Donchian
LIGHTER_API_KEY_INDEX=4
LIGHTER_L1_ADDRESS=0x...               # необязательно, для сверки
```

Проверка «не тот счёт»:

```bash
# на VPS
grep '^LIGHTER_ACCOUNT_INDEX=' /opt/donchian/.env.a
grep '^LIGHTER_ACCOUNT_INDEX=' /opt/poly-lighter/.env
# числа ДОЛЖНЫ отличаться
```

Инструмент и сайз v1:

```dotenv
LIGHTER_SYMBOL=BTC
LIGHTER_LEVERAGE=1
LIGHTER_EQUITY_FRACTION=1.0
MARKET_SLIPPAGE=0.02
STOP_PRICE_PCT=0.00125
```

Малый депозит **не** требует `EQUITY_FRACTION=0.05`: бот и так торгует 100% того, что лежит на **этом** счёте. Дробить 5% от $30 часто упирается в min lot. Оставь `1.0`.

Для `POLY_LIGHTER_1` оставь плечо `1` и долю `1.0`: это полный стек при 1x. Параметр `STOP_PRICE_PCT=0.00125` означает стоп в 0.125% от цены BTC входа (например, LONG: `fill × 0.99875`; SHORT: `fill × 1.00125`). Стоп выставляется reduce-only на Lighter после подтверждения fill. Значение должно быть больше нуля; ноль отключён, чтобы бот не мог работать с позицией без обязательного стопа.

`MARKET_SLIPPAGE` задаёт общий предел цены IOC для входа, стопа и выхода относительно справочной цены. Значение `0.02` допускает отклонение до 2%; это защитный ценовой предел, а не обещанное фактическое проскальзывание. Меняй его только после оценки риска и частоты неисполнения.

### 5.3 Polymarket / тайминг / тень (канон, не менять без нового пакета)

```dotenv
PM_SERIES_SLUG=btc-updown-5m
PM_THRESHOLD=0.65
PM_MAX_SPREAD=0.05
PM_PREWARM_SECONDS=15
ENTRY_MAX_LAG_SECONDS=2.0
EXIT_EARLY_SECONDS=0.30
STOP_PRICE_PCT=0.00125
SHADOW_BINANCE_1S=true
```

`STOP_PRICE_PCT` задаётся десятичной долей цены: `0.00125` = `0.125%`. Если меняешь это значение, пересчитай его в соответствии со своим профилем; не умножай на плечо. Бот остановится при `STOP_PRICE_PCT=0`.

### 5.4 Стоп-кран

```dotenv
HALT_ON_EXIT_LATE_STREAK=3
HALT_DD_PCT=0.08
KILL_SWITCH=false
```

`KILL_SWITCH=true` — старт сразу в halt (flatten, без входов). Для обычного запуска `false`.  
Смена `.env` **не** подхватывается `docker compose restart` — нужен recreate (см. §7).

### 5.5 Ордера

```dotenv
DRY_RUN=false
```

Без ключа процесс **не стартует**. `DRY_RUN=true` оставляй только если сознательно хочешь сутки без ордеров на том же mainnet-ключе.

### 5.6 Дашборд / диск / HTTP внутри контейнера

```dotenv
SQLITE_PATH=/data/bot.db
LOG_DIR=/data/logs
HTTP_ADDR=:8080
DASHBOARD_PASSWORD=длинный-уникальный-пароль
COOKIE_SECURE=false
WEB_PORT=8081
```

- `HTTP_ADDR=:8080` — слушает **внутри** контейнера `bot`. Не менять: healthcheck и nginx стучатся сюда.
- `WEB_PORT=8081` — порт **на хосте**. Donchian = 80. Если 8081 занят: `8082` и ходи на `:8082`.
- `COOKIE_SECURE=true` только когда заходишь по **https://**.

### 5.7 Telegram

```dotenv
TELEGRAM_BOT_TOKEN=123:ABC
TELEGRAM_CHAT_IDS=123456789
TELEGRAM_HEARTBEAT_MINUTES=60
```

Пустой токен = без Telegram, дашборд работает.

### 5.8 Сводка grep после заполнения

```bash
cd /opt/poly-lighter
grep -E '^(LIGHTER_NETWORK|LIGHTER_API_PRIVATE_KEY|LIGHTER_ACCOUNT_INDEX|LIGHTER_API_KEY_INDEX|DASHBOARD_PASSWORD|DRY_RUN|WEB_PORT|KILL_SWITCH|LIGHTER_EQUITY_FRACTION|PM_THRESHOLD)=' .env
```

Ожидание на выкат:

- `LIGHTER_NETWORK=mainnet`
- ключ и account не пустые, **не** как у Donchian
- `LIGHTER_API_KEY_INDEX=4` (или другой 4–254)
- `DRY_RUN=false`
- `WEB_PORT=8081`
- `KILL_SWITCH=false`
- `PM_THRESHOLD=0.65`
- `LIGHTER_EQUITY_FRACTION=1.0`

---

## 6. Сборка и первый запуск

```bash
cd /opt/poly-lighter
docker compose up -d --build
docker compose ps
# bot ... healthy (start_period до 120s), web Up

docker compose logs -f bot
```

В логе жди:

- `"profile" ... POLY_LIGHTER_1`, `network=mainnet`
- `market symbol=BTC id=…` (боевой id, обычно 1; не 4095/4096)
- `http listen`
- `POLY_LIGHTER_1 онлайн` / Telegram boot
- `armed` за ~15 с до границы 5m (unix кратно 300)
- `skip` с `below_th` / `bad_quote` — **норма**, не ошибка

Проверки:

```bash
curl -s http://127.0.0.1:8081/api/health
# {"ok":true,...}

docker compose exec bot wget -qO- http://127.0.0.1:8080/api/health
```

Телефон / браузер: `http://IP_VPS:8081/` → пароль из `DASHBOARD_PASSWORD`.

Donchian по-прежнему `http://IP_VPS/` (порт 80). Два разных логина.

Если `web` не встаёт: `bot` ещё не healthy — смотри `docker compose logs bot`.

---

## 7. Что должно быть на дашборде после старта

- Сеть **mainnet**, без DRY.
- Состояние `IDLE` / `ARMING` / `DECIDE`, не вечный `HALT`.
- Позиция **flat**, пока нет входа.
- Вкладки **Окна**: раз в 5 минут строка (часто `skip` / `below_th`).
- **mean bps live / тень / gap / соответствие** — пока мало сделок, будут нули и «—» у коэффициента (`|тень| < 0.05 bps`).
- Kill / Resume как у Donchian.

Пропуск текущего 5m после рестарта — **так и задумано**. Вход только с **следующей** границы.

---

## 8. Малый депозит = уже «tiny»

`EQUITY_FRACTION=1.0` на счёте с небольшой суммой и есть полный стек стратегии на маленьких деньгах. 5-минутный ход BTC ≈ тот же процент депозита (1x). Не сравнивай PnL с $3479 из research.

После первых сделок смотри lag и gap live−тень. Если p50 fill > 2s или gap хуже −1.5 bps на сотнях окон — `/kill` и разбор, не крути порог.

---

## 9. Два дашборда и HTTPS

### Вариант A (по умолчанию)

- Donchian: `http://IP/`
- POLY: `http://IP:8081/`

Ничего в Caddy Donchian не менять. Не запускай `docker compose --profile tls` **здесь**, если у Donchian уже Caddy на 80/443 — порты столкнутся.

### Вариант B — поддомен на уже существующем Caddy Donchian

1. DNS: `poly.твой-домен` → IP VPS.
2. В `/opt/donchian/deploy/Caddyfile` (или как у тебя заведён Caddy) **добавь сайт**, не выкидывая Donchian:

```caddy
poly.example.com {
    reverse_proxy 127.0.0.1:8081
}
```

3. Чтобы 8081 не торчал в мир, в `/opt/poly-lighter/docker-compose.yml` у `web.ports` можно сменить на `"127.0.0.1:8081:80"` и recreate `web`.
4. В `/opt/poly-lighter/.env`: `COOKIE_SECURE=true`, recreate `bot web`.
5. Перечитай Caddy Donchian: `cd /opt/donchian && docker compose --profile tls up -d` (как вы обычно перезапускаете Caddy). Не поднимай второй Caddy из poly-lighter.

Заход на дашборд POLY: `https://poly.example.com`.

---

## 10. Если в логе вдруг testnet

Этот выкат — только mainnet. Если контейнер поднялся с `network=testnet`, останови, поправь `.env`, **wipe** `data/bot.db` (testnet-журнал не смешивать) и `--force-recreate`.

---

## 11. Pull + полный сброс + recreate (канон на VPS)

Это **единственный** блок «перезапустить poly с нуля». Копируй его каждый раз. Donchian (`/opt/donchian`) не трогать.

Стирает SQLite, WAL, логи приложения, docker-логи контейнеров poly. **Не** трогает `.env` и ключи. HALT живёт в SQLite — после wipe пропадает, `/resume` не нужен. `KILL_SWITCH=false` в файле ≠ в уже бегущем процессе.

Перед блоком в Lighter UI на **аккаунте poly** (`LIGHTER_ACCOUNT_INDEX` из `/opt/poly-lighter/.env`): позиция **FLAT**, нет висящих ордеров. На старте бот всё равно попытается flatten.

На этом хосте **нельзя** второму процессу сидеть на том же аккаунте / `api_key_index`. `donchian-bot_b` вырезан из compose Donchian — если контейнер снова Up, он flatten'ит poly. `bot_a` Donchian — **другой** `LIGHTER_ACCOUNT_INDEX`.

Менял `.env` или образ → всегда этот блок (`--force-recreate`), не голый `restart`. Не `cp .env.example .env`. `STOP_PRICE_PCT=0.00125`, `MARKET_SLIPPAGE=0.02`, `PM_PREWARM_SECONDS=15`.

Вход подтверждается до ~5 с (trades + позиция). Если стоп после fill не встал — flatten и HALT.

### Один блок (SSH, root, bash)

```bash
cd /opt/poly-lighter || exit 1
set -euo pipefail

echo "poly account: $(grep '^LIGHTER_ACCOUNT_INDEX=' .env)"
echo "donchian a:   $(grep '^LIGHTER_ACCOUNT_INDEX=' /opt/donchian/.env.a || true)"
# числа ДОЛЖНЫ отличаться

docker ps -a --format 'table {{.Names}}\t{{.Status}}'
# donchian-bot_b-1 быть не должно. Если есть:
#   docker stop donchian-bot_b-1 && docker rm -f donchian-bot_b-1
#   cd /opt/donchian && docker compose rm -f bot_b 2>/dev/null || true
#   cd /opt/poly-lighter

docker compose down

mkdir -p backups
tar czf "backups/data-$(date -u +%Y%m%dT%H%M%SZ).tgz" data .env
rm -rf data
mkdir -p data

git fetch origin
git checkout main
git pull --ff-only
git log -1 --oneline

docker compose up -d --build --force-recreate --remove-orphans bot web
docker compose ps
docker compose logs --tail=80 bot

curl -s http://127.0.0.1:8081/api/health
docker compose exec bot printenv LIGHTER_ACCOUNT_INDEX LIGHTER_API_KEY_INDEX KILL_SWITCH DRY_RUN
```

Ожидай `POLY_LIGHTER_1 онлайн`, health без `HALT`. Дашборд `:8081` — пустая история. Текущее 5m-окно **не** догоняет; вход со следующей границы UTC. Telegram `/status`.

Откат кода: `git log -5 --oneline`, `git checkout <коммит>`, снова `up -d --build --force-recreate`. Данные — из `backups/data-*.tgz`.

### 11.1 Выгрузка логов + БД для анализа (SSH)

Бота **не** останавливать. Ключи в архив не попадают.

На VPS:

```bash
cd /opt/poly-lighter || exit 1
set -euo pipefail
OUT=/tmp/poly-lighter-diagnostic
rm -rf "$OUT"
mkdir -p "$OUT/data"

date -u > "$OUT/date.txt"
uptime >> "$OUT/date.txt"
hostname >> "$OUT/date.txt"
uname -a >> "$OUT/date.txt"

git rev-parse HEAD > "$OUT/git.txt"
git log -8 --oneline >> "$OUT/git.txt"
git status --short >> "$OUT/git.txt"

docker compose ps -a > "$OUT/compose-ps.txt" 2>&1
docker ps -a --format 'table {{.Names}}\t{{.Status}}\t{{.Image}}' > "$OUT/docker-ps.txt" 2>&1
docker inspect poly-lighter-bot-1 --format \
  '{{.Name}} image={{.Config.Image}} started={{.State.StartedAt}} status={{.State.Status}}' \
  > "$OUT/inspect.txt" 2>&1
docker compose exec -T bot printenv LIGHTER_NETWORK LIGHTER_ACCOUNT_INDEX LIGHTER_API_KEY_INDEX \
  KILL_SWITCH DRY_RUN PM_THRESHOLD STOP_PRICE_PCT INSTANCE_NAME 2>/dev/null > "$OUT/env_runtime.txt"

sed -E 's/^([A-Za-z0-9_]*(KEY|SECRET|TOKEN|PASS|PRIVATE|ADDRESS|CHAT)[A-Za-z0-9_]*=).*/\1<redacted>/' .env \
  > "$OUT/env.redacted.txt"

docker compose logs --no-color --timestamps --since 168h bot > "$OUT/bot.logs.txt" 2>&1
docker compose logs --no-color --timestamps --since 24h web > "$OUT/web.logs.txt" 2>&1

docker compose exec -T bot sqlite3 /data/bot.db ".backup /data/bot-snap.db" \
  && cp -a data/bot-snap.db "$OUT/data/" \
  || sqlite3 data/bot.db ".backup $OUT/data/bot-snap.db" \
  || cp -a data/bot.db "$OUT/data/" || true
cp -a data/logs "$OUT/data/" 2>/dev/null || true

tar czf /tmp/poly-lighter-diagnostic.tar.gz -C /tmp poly-lighter-diagnostic
ls -lh /tmp/poly-lighter-diagnostic.tar.gz
```

На Windows (PowerShell):

```powershell
scp root@45.134.218.19:/tmp/poly-lighter-diagnostic.tar.gz C:\All\Develop\poly_lighter_trade\
```

В чат: «архив готов».

---

## 12. Бэкапы SQLite

Файл: `data/bot.db` (+ `-wal`/`-shm` пока бот запущен).

Онлайн:

```bash
cd /opt/poly-lighter
mkdir -p backups
docker compose exec -T bot sqlite3 /data/bot.db ".backup /data/bot-snap.db"
cp data/bot-snap.db backups/bot-$(date -u +%Y%m%dT%H%M%SZ).db
```

Офлайн:

```bash
docker compose stop bot
cp -a data backups/data-$(date -u +%Y%m%dT%H%M%SZ)
docker compose start bot
```

Cron (root, не пересекать с Donchian в 03:15 — у него своё время):

```cron
25 3 * * * cd /opt/poly-lighter && docker compose exec -T bot sqlite3 /data/bot.db ".backup /data/bot-cron.db" && cp data/bot-cron.db backups/bot-$(date -u +\%Y\%m\%d).db
```

Копии **вне VPS**. В БД нет ключей, есть история окон/сделок.

---

## 13. Повседневные операции

```bash
cd /opt/poly-lighter
docker compose logs -f --tail=200 bot
docker compose logs -f web
```

| Задача | Как |
|--------|-----|
| Рестарт процесса | `docker compose restart bot` — подхватит SQLite; **текущее** 5m-окно не догонит входом |
| Kill | дашборд или Telegram `/kill` — flatten, входы запрещены |
| Resume | `/resume` или кнопка; сбрасывает и авто-HALT |
| Смена пароля / ключа | `.env`, затем `up -d --force-recreate bot web` |
| Место | `df -h`, `docker system df`, `backups/` |

После рестарта: если на бирже была позиция — flatten сразу; следующее решение только на границе окна.

Полный сброс + pull — **только §11**. Дашборд :8081 — вкладки **Часы** (UTC/Binance vs исполнение Lighter) и широкая таблица окон. Telegram `/status` показывает `clock_binance`.

`sync-clock-http.timer` должен быть **disabled**. Chrony: `Leap status: Normal`. Helper не включать.

---

Стоп-кран сам: 3 окна `exit_late` / unfilled подряд; позиция > 6 мин; equity −8% от пика. Снимается `/resume`.

---

## 14. Локально (Windows), без Docker

Не мешает VPS. Порты: бот `:8080`, Vite **5174**.

```powershell
cd C:\All\Develop\poly_lighter_trade
copy .env.example .env
# SQLITE_PATH=./data/bot.db
# HTTP_ADDR=:8080
# LIGHTER_NETWORK=mainnet
# DRY_RUN=false
# DASHBOARD_PASSWORD=dev
mkdir data

cd backend
go test ./...
go run ./cmd/bot
```

Второй терминал:

```powershell
cd C:\All\Develop\poly_lighter_trade\frontend
npm install
npm run dev
```

Открыть `http://127.0.0.1:5174`. Если Donchian Vite уже на 5173 — конфликта нет.

---

## 15. Troubleshooting

| Симптом | Что проверить |
|--------|----------------|
| `LIGHTER_API_PRIVATE_KEY is required` | `.env`, compose `env_file: .env`, `DRY_RUN=false` |
| `API_KEY_INDEX must be 4-254` | не 0–3 |
| `market BTC not found` | сеть testnet/mainnet, символ `BTC` |
| `LIGHTER_LEVERAGE must be in (0, 50]` | плечо 0 или > 50 |
| `DASHBOARD_PASSWORD is required` | пустой пароль |
| Порт занят / web не мапится | Donchian на 80; этот бот только 8081; не `--profile tls` рядом с Caddy Donchian |
| Telegram `409 Conflict` | тот же token, что у Donchian, или второй контейнер |
| Дашборд 401 после HTTPS | `COOKIE_SECURE=true`, заход по `https://` |
| Ошибка времени / неверные UTC-границы | вкладка **Часы** — диагностический offset Binance; на VPS проверь `chronyc tracking` и `chronyc sources -v`. Должны быть `Leap status: Normal` и выбранный источник `^*`. Binance API таймаут сам по себе вход не блокирует |
| skip `no_market` | slug `btc-updown-5m-{ts}`, Gamma 404 — окно без рынка, не подставлять соседнее |
| skip `below_th` | **норма**, большинство окон |
| `sendTx` / nonce / `closed_away` через ~11 с | второй процесс на том же `LIGHTER_ACCOUNT_INDEX` (часто зомби `donchian-bot_b`); один бот на аккаунт и на `api_key_index` |
| `stop_missing` / HALT после входа | проверь доступность Lighter и reduce-only stop; не снимай HALT, пока позиция flat и в `/api/orders` нет лишнего условного ордера |
| Donchian позиции закрылись | **тот же account_index** — останови POLY, разберись, больше никогда не шарь счёт |
| web unhealthy / не стартует | `bot` не healthy, смотри `logs bot` |
| `restart` не подхватил `.env` | `up -d --force-recreate bot web` |

Полезное:

```bash
curl -s http://127.0.0.1:8081/api/health
docker compose exec bot wget -qO- http://127.0.0.1:8080/api/health
docker compose exec bot printenv LIGHTER_NETWORK DRY_RUN KILL_SWITCH PM_THRESHOLD LIGHTER_EQUITY_FRACTION STOP_PRICE_PCT
```

---

## 16. Что смотреть, когда уже торгует

Не сравнивать депозит с $3479 из research.

- coverage валидных котировок на границе;
- p50 / p90 fill lag (цель p50 < 1s, p90 < 2s);
- **mean bps live**, **mean bps тень**, **gap live − тень**, **соответствие** (live/тень, «—» если тень ~0);
- gap хуже ≈ **−1.5 bps** на большой выборке → исполнение, не «стратегия сдохла»;
- коэффициент **~0.96** при плюсовой тени — норма; **> 1** — live лучше тени; **0.6** — утечка, ещё не обязательно красный −1.5 bps.

n маленькое (десятки сделок) — не крутить порог 0.65.

---

## 17. Дисклеймер

1x на полном стеке: 5-минутный ход BTC ≈ ход депозита. Research не гарантия. Рискуй только тем, что готов потерять.
