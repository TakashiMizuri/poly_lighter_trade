import { useEffect, useMemo, useState } from "react";
import { LogOutIcon } from "lucide-react";
import { Line, LineChart, ResponsiveContainer, Tooltip as RTooltip, XAxis, YAxis } from "recharts";
import {
  EventRow,
  EquityPoint,
  OrderRow,
  Stats,
  Status,
  WindowRow,
  api,
  num,
} from "./api";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";

function fmt(n: number | null | undefined, d = 2) {
  if (n == null || Number.isNaN(n)) return "—";
  return n.toFixed(d);
}

function utc(ts: number) {
  if (!ts) return "—";
  return new Date(ts * 1000).toISOString().replace(".000Z", "Z");
}

function hhmm(ts: number) {
  if (!ts) return "—";
  return new Date(ts * 1000).toISOString().slice(11, 16) + "Z";
}

function signedMs(n: number | null | undefined) {
  if (n == null || Number.isNaN(n)) return "—";
  return `${n >= 0 ? "+" : ""}${n.toFixed(0)} ms`;
}

function ms(n: number | null | undefined) {
  if (n == null || Number.isNaN(n)) return "—";
  return `${n.toFixed(0)} ms`;
}

function isTrade(w: WindowRow) {
  return w.side === "long" || w.side === "short";
}

/** Лаг только у входов: open 5m UTC → timestamp ордера/fill на Lighter. */
function tradeLag(w: WindowRow): number | null {
  if (!isTrade(w)) return null;
  return num(w.fill_lag_ms);
}

function skipLabel(reason: string) {
  const map: Record<string, string> = {
    below_th: "ниже порога 0.65",
    both_ge_th: "обе стороны ≥ 0.65",
    late: "позже T+2 с",
    clock: "часы хоста",
    no_market: "нет рынка PM",
    bad_quote: "нет живой котировки",
    stale_quote: "котировка устарела",
    quote_incoherent: "котировка Up/Down не сходится",
    halt: "HALT",
    equity_too_small: "мало equity / лот",
    flatten_busy: "не flat",
    order_failed: "ордер не ушёл",
    entry_unfilled_after_send: "ордер без fill",
    stop_missing: "стоп не встал",
    equity: "нет equity",
    no_px: "нет цены BTC",
  };
  return map[reason] || reason;
}

function sideBadge(side: string) {
  if (side === "long") return <Badge>LONG</Badge>;
  if (side === "short") return <Badge variant="outline">SHORT</Badge>;
  return <Badge variant="secondary">skip</Badge>;
}

function pnlClass(n: number | null) {
  if (n == null) return "";
  if (n > 0) return "text-emerald-700";
  if (n < 0) return "text-red-700";
  return "";
}

export default function Dashboard({ onLogout }: { onLogout: () => void }) {
  const [tab, setTab] = useState("overview");
  const [status, setStatus] = useState<Status | null>(null);
  const [stats, setStats] = useState<Stats | null>(null);
  const [windows, setWindows] = useState<WindowRow[]>([]);
  const [trades, setTrades] = useState<WindowRow[]>([]);
  const [orders, setOrders] = useState<OrderRow[]>([]);
  const [equity, setEquity] = useState<EquityPoint[]>([]);
  const [events, setEvents] = useState<EventRow[]>([]);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);

  async function refresh() {
    try {
      const [s, st, w, t, o, e, ev] = await Promise.all([
        api.status(),
        api.stats(),
        api.windows(),
        api.trades(),
        api.orders(),
        api.equity(),
        api.events(),
      ]);
      setStatus(s);
      setStats(st);
      setWindows(Array.isArray(w) ? w : []);
      setTrades(Array.isArray(t) ? t : []);
      setOrders(Array.isArray(o) ? o : []);
      setEquity(Array.isArray(e) ? e : []);
      setEvents(Array.isArray(ev) ? ev : []);
      setErr("");
    } catch (e) {
      if (String(e) === "Error: auth") onLogout();
      else setErr(String(e));
    }
  }

  useEffect(() => {
    void refresh();
    const id = setInterval(() => void refresh(), 5000);
    return () => clearInterval(id);
  }, []);

  async function kill() {
    setBusy(true);
    try {
      await api.kill();
      await refresh();
    } finally {
      setBusy(false);
    }
  }
  async function resume() {
    setBusy(true);
    try {
      await api.resume();
      await refresh();
    } finally {
      setBusy(false);
    }
  }

  const halted = Boolean(status?.kill_switch || status?.halted);
  const clockBad = Boolean(status && status.clock_ok && Math.abs(status.clock_offset_ms) > 1000);
  const skipMix = useMemo(() => {
    const m = new Map<string, number>();
    for (const w of windows) {
      if (w.side !== "skip") continue;
      const k = w.skip_reason || "skip";
      m.set(k, (m.get(k) || 0) + 1);
    }
    return [...m.entries()].sort((a, b) => b[1] - a[1]).slice(0, 6);
  }, [windows]);
  const lagSeries = useMemo(
    () =>
      [...trades]
        .filter((w) => tradeLag(w) != null)
        .slice(0, 40)
        .reverse()
        .map((w) => ({ ts: w.window_ts, lag: tradeLag(w) as number })),
    [trades],
  );
  const lastTrade = trades[0];
  const signal =
    status && status.p_up >= 0.65 ? "вход LONG" : status && 1 - status.p_up >= 0.65 ? "вход SHORT" : "обычно skip";

  return (
    <div className="mx-auto flex min-h-dvh max-w-7xl flex-col">
      <header className="sticky top-0 z-10 flex items-center justify-between gap-3 border-b bg-background/90 px-4 py-3 backdrop-blur">
        <div className="min-w-0">
          <div className="font-heading text-sm font-medium">POLY_LIGHTER_1</div>
          <div className="truncate text-xs text-muted-foreground">
            {status
              ? `${status.instance_name} · ${status.network}${status.dry_run ? " · DRY" : ""} · BTC perp ${fmt(status.leverage, 1)}× · ${status.state}`
              : "загрузка…"}
          </div>
        </div>
        <div className="flex items-center gap-2">
          <Badge variant={halted ? "destructive" : "secondary"}>{halted ? "HALT" : status?.state || "…"}</Badge>
          <Button variant="ghost" size="icon" onClick={() => void api.logout().then(onLogout)} aria-label="Выйти">
            <LogOutIcon />
          </Button>
        </div>
      </header>

      {err ? (
        <Alert className="m-4" variant="destructive">
          <AlertTitle>Ошибка</AlertTitle>
          <AlertDescription>{err}</AlertDescription>
        </Alert>
      ) : null}

      {!status ? (
        <div className="space-y-3 p-4">
          <Skeleton className="h-24" />
          <Skeleton className="h-48" />
        </div>
      ) : (
        <Tabs value={tab} onValueChange={setTab} className="flex-1">
          <TabsList className="mx-4 mt-3 grid w-auto grid-cols-6">
            <TabsTrigger value="overview">Обзор</TabsTrigger>
            <TabsTrigger value="trades">Сделки</TabsTrigger>
            <TabsTrigger value="windows">Окна</TabsTrigger>
            <TabsTrigger value="exec">Ордера</TabsTrigger>
            <TabsTrigger value="clock">Часы</TabsTrigger>
            <TabsTrigger value="health">Журнал</TabsTrigger>
          </TabsList>

          <TabsContent value="overview" className="space-y-3 p-4">
            <div className="grid gap-3 lg:grid-cols-3">
              <Card>
                <CardHeader className="pb-1">
                  <CardTitle className="text-xs font-normal text-muted-foreground">Текущее 5m окно UTC</CardTitle>
                </CardHeader>
                <CardContent className="space-y-1">
                  <div className="text-lg font-medium">{utc(status.window_ts)}</div>
                  <p className="text-xs text-muted-foreground">
                    {fmt(status.secs_into_window, 0)} с от open · до close {fmt(status.secs_to_close, 0)} с
                  </p>
                  <p className="text-xs">
                    Позиция Lighter: <b>{status.position}</b>
                    {status.entry_px ? ` @ ${fmt(status.entry_px)}` : ""}
                    {status.stop_px ? ` · стоп ${fmt(status.stop_px)}` : ""}
                  </p>
                </CardContent>
              </Card>
              <Card>
                <CardHeader className="pb-1">
                  <CardTitle className="text-xs font-normal text-muted-foreground">Сигнал Polymarket (не торгуем здесь)</CardTitle>
                </CardHeader>
                <CardContent className="space-y-1">
                  <div className="text-lg font-medium">
                    Up {fmt(status.p_up, 3)} · Down {fmt(status.p_down, 3)}
                  </div>
                  <p className="text-xs text-muted-foreground">
                    {signal} · порог 0.65 · спред {fmt(status.spread, 3)} · {status.quote_source || "нет котировки"} · age{" "}
                    {fmt(status.quote_age_ms, 0)} ms
                  </p>
                  <p className="text-xs text-muted-foreground">Последний skip: {status.last_skip ? skipLabel(status.last_skip) : "—"}</p>
                </CardContent>
              </Card>
              <Card>
                <CardHeader className="pb-1">
                  <CardTitle className="text-xs font-normal text-muted-foreground">Лаг входа :00 → ордер Lighter</CardTitle>
                </CardHeader>
                <CardContent className="space-y-1">
                  <div className="text-lg font-medium">{stats?.n_fill_lag ? ms(stats.mean_fill_lag_ms) : "—"}</div>
                  <p className="text-xs text-muted-foreground">
                    Среднее по {stats?.n_fill_lag ?? 0} входам. Skip без ордера не входят.
                  </p>
                  <p className="text-xs text-muted-foreground">
                    p50 {ms(stats?.p50_fill_lag_ms)} · p90 {ms(stats?.p90_fill_lag_ms)} · timestamp ордера на бирже минус
                    open окна UTC
                  </p>
                </CardContent>
              </Card>
            </div>

            <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
              <Stat label="Equity Lighter" value={`$${fmt(status.equity)}`} hint="Живой счёт" />
              <Stat label="Пик / тень Binance" value={`$${fmt(status.peak_equity)} / $${fmt(status.shadow_equity)}`} hint="Тень — research T+1s, не Lighter" />
              <Stat label="Сделки / skip" value={`${stats?.n_trades ?? 0} / ${stats?.n_skip ?? 0}`} hint={`окон ${stats?.n_windows ?? 0} · покрытие котировкой ${fmt((stats?.coverage ?? 0) * 100, 1)}%`} />
              <Stat label="WR live" value={`${fmt((stats?.wr ?? 0) * 100, 1)}%`} hint={`mean ${fmt(stats?.mean_bps, 1)} bps · SL ${stats?.n_sl ?? 0}`} />
              <Stat label="Тень mean bps" value={fmt(stats?.shadow_mean_bps, 1)} hint="Binance 1s T+1 → 5m close" />
              <Stat label="gap live − тень" value={fmt(stats?.gap_bps, 1)} hint={stats?.match_coef == null ? "мало пар" : `live/тень ${fmt(stats.match_coef, 2)}`} />
              <Stat
                label="Последний вход"
                value={lastTrade ? `${lastTrade.side.toUpperCase()} ${ms(tradeLag(lastTrade))}` : "пока нет"}
                hint={lastTrade ? `${utc(lastTrade.window_ts)} · ${fmt(num(lastTrade.lighter_entry_px), 1)}` : "лаг появится после первой сделки"}
              />
              <Stat label="Стоп" value={`${fmt(status.stop_price_pct * 100, 3)}% цены BTC`} hint="Reduce-only, не % депозита" />
            </div>

            <div className="grid gap-3 lg:grid-cols-2">
              <Card>
                <CardHeader>
                  <CardTitle className="text-sm">Equity live vs тень Binance</CardTitle>
                </CardHeader>
                <CardContent className="h-48">
                  {equity.length < 2 ? (
                    <div className="text-sm text-muted-foreground">Мало точек — кривая появится после окон</div>
                  ) : (
                    <ResponsiveContainer width="100%" height="100%">
                      <LineChart data={equity}>
                        <XAxis dataKey="ts" hide />
                        <YAxis domain={["auto", "auto"]} width={48} />
                        <RTooltip />
                        <Line type="monotone" dataKey="live" stroke="#111" dot={false} strokeWidth={1.5} name="Lighter" />
                        <Line type="monotone" dataKey="shadow" stroke="#888" dot={false} strokeWidth={1} name="тень" />
                      </LineChart>
                    </ResponsiveContainer>
                  )}
                </CardContent>
              </Card>
              <Card>
                <CardHeader>
                  <CardTitle className="text-sm">Лаг входов, ms (только сделки)</CardTitle>
                </CardHeader>
                <CardContent className="h-48">
                  {lagSeries.length < 2 ? (
                    <div className="text-sm text-muted-foreground">Нужны минимум две сделки с timestamp ордера</div>
                  ) : (
                    <ResponsiveContainer width="100%" height="100%">
                      <LineChart data={lagSeries}>
                        <XAxis dataKey="ts" tickFormatter={(v) => hhmm(Number(v))} minTickGap={24} />
                        <YAxis domain={["auto", "auto"]} width={40} />
                        <RTooltip formatter={(v) => [`${Number(v).toFixed(0)} ms`, "лаг"]} labelFormatter={(l) => utc(Number(l))} />
                        <Line type="monotone" dataKey="lag" stroke="#111" dot={false} strokeWidth={1.5} />
                      </LineChart>
                    </ResponsiveContainer>
                  )}
                </CardContent>
              </Card>
            </div>

            <div className="grid gap-3 lg:grid-cols-2">
              <Card>
                <CardHeader>
                  <CardTitle className="text-sm">Почему skip</CardTitle>
                </CardHeader>
                <CardContent>
                  {skipMix.length === 0 ? (
                    <p className="text-sm text-muted-foreground">Пока нет пропусков</p>
                  ) : (
                    <ul className="space-y-1 text-sm">
                      {skipMix.map(([k, n]) => (
                        <li key={k} className="flex justify-between gap-3">
                          <span>{skipLabel(k)}</span>
                          <span className="text-muted-foreground">{n}</span>
                        </li>
                      ))}
                    </ul>
                  )}
                </CardContent>
              </Card>
              <Card>
                <CardHeader>
                  <CardTitle className="text-sm">Последние окна</CardTitle>
                </CardHeader>
                <CardContent className="overflow-x-auto">
                  <Table>
                    <TableHeader>
                      <TableRow>
                        <TableHead>open</TableHead>
                        <TableHead>сторона</TableHead>
                        <TableHead>p_up</TableHead>
                        <TableHead>лаг</TableHead>
                        <TableHead>итог</TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {windows.slice(0, 8).map((w) => (
                        <TableRow key={w.window_ts}>
                          <TableCell className="text-xs">{hhmm(w.window_ts)}</TableCell>
                          <TableCell>{sideBadge(w.side)}</TableCell>
                          <TableCell>{fmt(w.p_up, 3)}</TableCell>
                          <TableCell>{ms(tradeLag(w))}</TableCell>
                          <TableCell className={`text-xs ${pnlClass(num(w.pnl_usd))}`}>
                            {isTrade(w) ? (num(w.pnl_usd) != null ? `$${fmt(num(w.pnl_usd), 2)}` : w.exit_reason || "open") : skipLabel(w.skip_reason)}
                          </TableCell>
                        </TableRow>
                      ))}
                    </TableBody>
                  </Table>
                </CardContent>
              </Card>
            </div>

            <div className="flex flex-wrap items-center gap-2">
              <Button variant="destructive" disabled={busy} onClick={() => void kill()}>
                Kill / flatten
              </Button>
              <Button variant="outline" disabled={busy} onClick={() => void resume()}>
                Resume
              </Button>
              <span className="text-xs text-muted-foreground">
                Три площадки: PM = сигнал, Lighter = сделки, Binance = UTC и тень research.
              </span>
            </div>
          </TabsContent>

          <TabsContent value="trades" className="space-y-3 p-4">
            <p className="text-xs text-muted-foreground">
              Только окна, в которые бот вошёл. Лаг = timestamp ордера на Lighter минус :00 окна UTC. Skip здесь нет.
            </p>
            <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
              <Stat label="Средний лаг" value={stats?.n_fill_lag ? ms(stats.mean_fill_lag_ms) : "—"} />
              <Stat label="p50 / p90" value={`${ms(stats?.p50_fill_lag_ms)} / ${ms(stats?.p90_fill_lag_ms)}`} />
              <Stat label="n с лагом" value={String(stats?.n_fill_lag ?? 0)} />
              <Stat label="WR / mean bps" value={`${fmt((stats?.wr ?? 0) * 100, 1)}% / ${fmt(stats?.mean_bps, 1)}`} />
            </div>
            <WindowTable rows={trades} tradesOnly />
          </TabsContent>

          <TabsContent value="windows" className="p-4">
            <p className="mb-2 text-xs text-muted-foreground">
              Каждое 5m окно. Колонка лага пустая на skip — ордера не было, это не «медленный вход».
            </p>
            <WindowTable rows={windows} />
          </TabsContent>

          <TabsContent value="exec" className="space-y-3 p-4">
            <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
              <Stat label="Средний лаг входов" value={stats?.n_fill_lag ? ms(stats.mean_fill_lag_ms) : "—"} hint="Только long/short с timestamp биржи" />
              <Stat label="p50 / p90 лаг" value={`${ms(stats?.p50_fill_lag_ms)} / ${ms(stats?.p90_fill_lag_ms)}`} />
              <Stat label="paired live/тень" value={String(stats?.n_paired ?? 0)} />
              <Stat label="выходы по SL" value={String(stats?.n_sl ?? 0)} />
            </div>
            {orders.length === 0 ? (
              <p className="text-sm text-muted-foreground">Ордеров ещё нет</p>
            ) : (
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>окно UTC</TableHead>
                    <TableHead>kind</TableHead>
                    <TableHead>status</TableHead>
                    <TableHead>qty</TableHead>
                    <TableHead>px</TableHead>
                    <TableHead>client idx</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {orders.slice(0, 60).map((o) => (
                    <TableRow key={o.id}>
                      <TableCell className="text-xs">{utc(o.window_ts)}</TableCell>
                      <TableCell>{o.kind}</TableCell>
                      <TableCell>{o.status}</TableCell>
                      <TableCell>{fmt(o.qty, 5)}</TableCell>
                      <TableCell>{fmt(o.price, 1)}</TableCell>
                      <TableCell className="text-xs">{o.client_order_index}</TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            )}
          </TabsContent>

          <TabsContent value="clock" className="space-y-3 p-4">
            <Alert variant={clockBad ? "destructive" : "default"}>
              <AlertTitle>Сетка окон = UTC. Лаг входа — это не эти часы.</AlertTitle>
              <AlertDescription>
                Binance time только проверяет, что VPS не уехал от UTC (slug btc-updown-5m-ts). Лаг сделки считается отдельно:
                время ордера на Lighter минус open окна.
              </AlertDescription>
            </Alert>
            <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
              <Stat
                label="offset Binance − host"
                value={status.clock_ok ? signedMs(status.clock_offset_ms) : "нет замера"}
                hint="Плюс = Binance впереди VPS"
              />
              <Stat label="RTT замера" value={ms(status.clock_rtt_ms)} hint=">800 ms — сеть, замер выкидывается" />
              <Stat label="замер доверенный" value={status.clock_ok ? "да" : "нет"} />
              <Stat label="дедлайн входа" value="T+2.0 с" hint="Если к ts+2 нет ордера — skip late" />
            </div>
          </TabsContent>

          <TabsContent value="health" className="space-y-3 p-4">
            <Alert variant={halted ? "destructive" : "default"}>
              <AlertTitle>{halted ? "HALT" : "OK"}</AlertTitle>
              <AlertDescription>
                kill={String(status.kill_switch)} · halt={status.halt_reason || "—"} · {status.state} · uptime{" "}
                {fmt(status.uptime_sec / 60, 1)} мин · dry={String(status.dry_run)}
              </AlertDescription>
            </Alert>
            <div className="grid grid-cols-2 gap-3 sm:grid-cols-3">
              <Stat label="symbol" value={status.symbol} />
              <Stat label="порог PM" value={fmt(status.threshold, 2)} />
              <Stat label="leverage × fraction" value={`${fmt(status.leverage, 1)} × ${fmt(status.equity_fraction, 2)}`} />
            </div>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>время</TableHead>
                  <TableHead>lvl</TableHead>
                  <TableHead>kind</TableHead>
                  <TableHead>msg</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {events.map((ev, i) => (
                  <TableRow key={i}>
                    <TableCell className="text-xs">{utc(ev.ts)}</TableCell>
                    <TableCell>{ev.level}</TableCell>
                    <TableCell>{ev.kind}</TableCell>
                    <TableCell className="max-w-[28rem] text-xs">{ev.message}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </TabsContent>
        </Tabs>
      )}
    </div>
  );
}

function Stat({ label, value, hint }: { label: string; value: string; hint?: string }) {
  return (
    <Card>
      <CardHeader className="pb-1">
        <CardTitle className="text-xs font-normal text-muted-foreground">{label}</CardTitle>
      </CardHeader>
      <CardContent>
        <div className="text-lg font-medium">{value}</div>
        {hint ? <div className="mt-1 text-[11px] leading-snug text-muted-foreground">{hint}</div> : null}
      </CardContent>
    </Card>
  );
}

function WindowTable({ rows, tradesOnly }: { rows: WindowRow[]; tradesOnly?: boolean }) {
  if (rows.length === 0) {
    return <p className="text-sm text-muted-foreground">{tradesOnly ? "Сделок ещё нет" : "Окон ещё нет"}</p>;
  }
  return (
    <div className="overflow-x-auto">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>окно UTC</TableHead>
            <TableHead>сторона</TableHead>
            <TableHead>выход</TableHead>
            <TableHead>p_up</TableHead>
            <TableHead>спред</TableHead>
            <TableHead>котировка</TableHead>
            {!tradesOnly ? <TableHead>skip</TableHead> : null}
            <TableHead>лаг :00→ордер</TableHead>
            <TableHead>Lighter in</TableHead>
            <TableHead>Lighter out</TableHead>
            <TableHead>стоп</TableHead>
            {tradesOnly ? <TableHead>pnl</TableHead> : null}
            {tradesOnly ? <TableHead>ret bps</TableHead> : null}
            {tradesOnly ? <TableHead>тень bps</TableHead> : null}
            {tradesOnly ? <TableHead>sl slip</TableHead> : null}
          </TableRow>
        </TableHeader>
        <TableBody>
          {rows.slice(0, 80).map((w) => (
            <TableRow key={w.window_ts}>
              <TableCell className="whitespace-nowrap text-xs">{utc(w.window_ts)}</TableCell>
              <TableCell>{sideBadge(w.side)}</TableCell>
              <TableCell className="text-xs">{w.exit_reason || "—"}</TableCell>
              <TableCell>{fmt(w.p_up, 3)}</TableCell>
              <TableCell>{fmt(w.spread, 3)}</TableCell>
              <TableCell className="text-xs">{w.quote_source || "—"}</TableCell>
              {!tradesOnly ? <TableCell className="text-xs">{w.skip_reason ? skipLabel(w.skip_reason) : "—"}</TableCell> : null}
              <TableCell>{ms(tradeLag(w))}</TableCell>
              <TableCell className="text-xs">{fmt(num(w.lighter_entry_px), 1)}</TableCell>
              <TableCell className="text-xs">{fmt(num(w.lighter_exit_px), 1)}</TableCell>
              <TableCell className="text-xs">{fmt(num(w.stop_px), 1)}</TableCell>
              {tradesOnly ? <TableCell className={pnlClass(num(w.pnl_usd))}>{fmt(num(w.pnl_usd), 2)}</TableCell> : null}
              {tradesOnly ? <TableCell>{fmt(num(w.ret_bps), 1)}</TableCell> : null}
              {tradesOnly ? <TableCell>{fmt(num(w.shadow_ret_bps), 1)}</TableCell> : null}
              {tradesOnly ? <TableCell>{fmt(num(w.sl_slip_bps), 1)}</TableCell> : null}
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  );
}
