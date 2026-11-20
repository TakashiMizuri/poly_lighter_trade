import { useEffect, useState } from "react";
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

function signedMs(n: number | null | undefined) {
  if (n == null || Number.isNaN(n)) return "—";
  return `${n >= 0 ? "+" : ""}${n.toFixed(0)} ms`;
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

  return (
    <div className="mx-auto flex min-h-dvh max-w-7xl flex-col">
      <header className="sticky top-0 z-10 flex items-center justify-between gap-3 border-b bg-background/90 px-4 py-3 backdrop-blur">
        <div className="min-w-0">
          <div className="font-heading text-sm font-medium">POLY_LIGHTER_1</div>
          <div className="truncate text-xs text-muted-foreground">
            {status
              ? `${status.instance_name} · ${status.network}${status.dry_run ? " · DRY" : ""} · ${fmt(status.leverage, 1)}x · frac ${fmt(status.equity_fraction, 2)} · ${status.state}`
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
            <TabsTrigger value="clock">Часы</TabsTrigger>
            <TabsTrigger value="windows">Окна</TabsTrigger>
            <TabsTrigger value="trades">Сделки</TabsTrigger>
            <TabsTrigger value="exec">Исполнение</TabsTrigger>
            <TabsTrigger value="health">Health</TabsTrigger>
          </TabsList>

          <TabsContent value="overview" className="space-y-3 p-4">
            <Venues />
            <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
              <Stat label="Equity Lighter" value={`$${fmt(status.equity)}`} hint="Живой счёт, не тень" />
              <Stat label="Peak / shadow" value={`$${fmt(status.peak_equity)} / $${fmt(status.shadow_equity)}`} />
              <Stat label="PM p_up / p_down" value={`${fmt(status.p_up, 3)} / ${fmt(status.p_down, 3)}`} hint="Сигнал Polymarket. Вход если ≥0.65 или ≤0.35" />
              <Stat label="PM spread" value={fmt(status.spread, 3)} />
              <Stat label="Coverage" value={`${fmt((stats?.coverage ?? 0) * 100, 1)}%`} />
              <Stat label="WR live" value={`${fmt((stats?.wr ?? 0) * 100, 1)}%`} />
              <Stat label="mean bps live" value={fmt(stats?.mean_bps, 2)} hint="Lighter fill → exit" />
              <Stat label="mean bps тень Binance" value={fmt(stats?.shadow_mean_bps, 2)} hint="T+1s 1s-close → 5m close, не Lighter" />
              <Stat label="gap live − тень" value={fmt(stats?.gap_bps, 2)} />
              <Stat label="соответствие" value={stats?.match_coef == null ? "—" : fmt(stats.match_coef, 2)} />
              <Stat label="n trades / skip clock / SL" value={`${stats?.n_trades ?? 0} / ${stats?.n_clock_skip ?? 0} / ${stats?.n_sl ?? 0}`} />
              <Stat label="p50 fill lag" value={`${fmt(stats?.p50_fill_lag_ms, 0)} ms`} hint="От open окна UTC до fill Lighter" />
            </div>
            <Card>
              <CardHeader>
                <CardTitle className="text-sm">Equity live (Lighter) vs shadow (Binance T+1s)</CardTitle>
              </CardHeader>
              <CardContent className="h-48">
                {equity.length < 2 ? (
                  <div className="text-sm text-muted-foreground">Мало точек</div>
                ) : (
                  <ResponsiveContainer width="100%" height="100%">
                    <LineChart data={equity}>
                      <XAxis dataKey="ts" hide />
                      <YAxis domain={["auto", "auto"]} width={48} />
                      <RTooltip />
                      <Line type="monotone" dataKey="live" stroke="#111" dot={false} strokeWidth={1.5} />
                      <Line type="monotone" dataKey="shadow" stroke="#888" dot={false} strokeWidth={1} />
                    </LineChart>
                  </ResponsiveContainer>
                )}
              </CardContent>
            </Card>
            <div className="flex gap-2">
              <Button variant="destructive" disabled={busy} onClick={() => void kill()}>
                Kill / flatten
              </Button>
              <Button variant="outline" disabled={busy} onClick={() => void resume()}>
                Resume
              </Button>
            </div>
            <p className="text-xs text-muted-foreground">
              Позиция Lighter: {status.position}
              {status.entry_px ? ` @ ${fmt(status.entry_px)}` : ""}
              {status.stop_px ? ` · stop ${fmt(status.stop_px)} (${fmt(status.stop_price_pct * 100, 3)}% цены BTC)` : ""}
              · PM quote {status.quote_source || "—"} age {fmt(status.quote_age_ms, 0)} ms · last skip {status.last_skip || "—"}
            </p>
          </TabsContent>

          <TabsContent value="clock" className="space-y-3 p-4">
            <Alert variant={clockBad ? "destructive" : "default"}>
              <AlertTitle>Сетка окна = UTC, не Lighter</AlertTitle>
              <AlertDescription>
                Сделка на Lighter, сигнал на Polymarket. Binance /api/v3/time — только проверка, что хост не уехал от UTC (slug окна
                btc-updown-5m-{"{ts}"}). Это не лаг исполнения на Lighter.
              </AlertDescription>
            </Alert>
            <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
              <Stat
                label="offset Binance − host"
                value={status.clock_ok ? signedMs(status.clock_offset_ms) : "нет доверенного замера"}
                hint="Плюс = Binance впереди VPS. Skip clock если |offset| > 1000 ms и RTT нормальный"
              />
              <Stat label="RTT замера Binance" value={`${fmt(status.clock_rtt_ms, 0)} ms`} hint="Если >800 ms замер выкидывается — это сеть, не часы" />
              <Stat label="замер доверенный" value={status.clock_ok ? "да" : "нет"} />
              <Stat label="окно UTC" value={utc(status.window_ts)} />
              <Stat label="секунда в окне" value={`${fmt(status.secs_into_window, 1)} / 300`} />
              <Stat label="до close окна" value={`${fmt(status.secs_to_close, 1)} s`} />
              <Stat label="порог входа T+2" value="2000 ms" hint="entry_lag = decision − window_ts" />
              <Stat label="стоп % цены BTC" value={`${fmt(status.stop_price_pct * 100, 3)}%`} hint="Не % депозита, не × leverage" />
            </div>
            <Card>
              <CardHeader>
                <CardTitle className="text-sm">Три площадки — не путать лаги</CardTitle>
              </CardHeader>
              <CardContent className="space-y-2 text-sm text-muted-foreground">
                <p>
                  <b className="text-foreground">Polymarket</b> — только сигнал: mid Up на open 5m. p_up / spread / quote_source / quote_age.
                </p>
                <p>
                  <b className="text-foreground">Lighter</b> — только исполнение: entry/exit px, qty, стоп, PnL, fill_lag (отправка→fill).
                </p>
                <p>
                  <b className="text-foreground">Binance</b> — часы UTC (serverTime) и тень research (1s T+1 → 5m close). Не место сделки.
                </p>
                <p>
                  Skip <code>clock</code> = бот решил, что хост слишком далеко от Binance time. Не значит «Lighter тормозит».
                  Колонка lag в окнах: entry_lag или fill_lag от <b>open UTC</b>, не detect_lag Donchian.
                </p>
              </CardContent>
            </Card>
          </TabsContent>

          <TabsContent value="windows" className="p-4">
            <p className="mb-2 text-xs text-muted-foreground">
              Горизонтальный скролл. clock_ms — замер Binance в момент окна. entry/fill lag — от ts UTC.
            </p>
            <WindowTable rows={windows} />
          </TabsContent>
          <TabsContent value="trades" className="p-4">
            <WindowTable rows={trades} tradesOnly />
          </TabsContent>
          <TabsContent value="exec" className="space-y-3 p-4">
            <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
              <Stat label="gap live − тень bps" value={fmt(stats?.gap_bps, 2)} />
              <Stat label="live/тень" value={stats?.match_coef == null ? "—" : fmt(stats.match_coef, 2)} />
              <Stat label="p50 / p90 entry lag" value={`${fmt(stats?.p50_lag_ms, 0)} / ${fmt(stats?.p90_lag_ms, 0)}`} />
              <Stat label="p50 / p90 fill lag" value={`${fmt(stats?.p50_fill_lag_ms, 0)} / ${fmt(stats?.p90_fill_lag_ms, 0)}`} />
              <Stat label="n paired" value={String(stats?.n_paired ?? 0)} />
              <Stat label="n SL" value={String(stats?.n_sl ?? 0)} />
            </div>
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
              <Stat label="threshold PM" value={fmt(status.threshold, 2)} />
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

function Venues() {
  return (
    <div className="grid gap-2 text-xs sm:grid-cols-3">
      <div className="rounded-md border p-2">
        <div className="font-medium">1. Polymarket</div>
        <div className="text-muted-foreground">Сигнал 5m Up/Down. Мы здесь не торгуем.</div>
      </div>
      <div className="rounded-md border p-2">
        <div className="font-medium">2. Lighter</div>
        <div className="text-muted-foreground">BTC perp: вход, стоп 0.125% цены, выход. Живой PnL.</div>
      </div>
      <div className="rounded-md border p-2">
        <div className="font-medium">3. Binance</div>
        <div className="text-muted-foreground">Прокси UTC + тень T+1s. Не биржа бота.</div>
      </div>
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
  return (
    <div className="overflow-x-auto">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>окно UTC</TableHead>
            <TableHead>side</TableHead>
            <TableHead>exit</TableHead>
            <TableHead>p_up</TableHead>
            <TableHead>spread</TableHead>
            <TableHead>src</TableHead>
            <TableHead>skip</TableHead>
            <TableHead>entry lag</TableHead>
            <TableHead>fill lag</TableHead>
            <TableHead>clock ms</TableHead>
            <TableHead>clock rtt</TableHead>
            <TableHead>Lighter in</TableHead>
            <TableHead>Lighter out</TableHead>
            <TableHead>stop</TableHead>
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
              <TableCell>{w.side}</TableCell>
              <TableCell className="text-xs">{w.exit_reason || "—"}</TableCell>
              <TableCell>{fmt(w.p_up, 3)}</TableCell>
              <TableCell>{fmt(w.spread, 3)}</TableCell>
              <TableCell className="text-xs">{w.quote_source || "—"}</TableCell>
              <TableCell className="text-xs">{w.skip_reason || "—"}</TableCell>
              <TableCell>{fmt(num(w.entry_lag_ms), 0)}</TableCell>
              <TableCell>{fmt(num(w.fill_lag_ms), 0)}</TableCell>
              <TableCell>{fmt(num(w.clock_offset_ms), 0)}</TableCell>
              <TableCell>{fmt(num(w.clock_rtt_ms), 0)}</TableCell>
              <TableCell className="text-xs">{fmt(num(w.lighter_entry_px), 1)}</TableCell>
              <TableCell className="text-xs">{fmt(num(w.lighter_exit_px), 1)}</TableCell>
              <TableCell className="text-xs">{fmt(num(w.stop_px), 1)}</TableCell>
              {tradesOnly ? <TableCell>{fmt(num(w.pnl_usd), 2)}</TableCell> : null}
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
