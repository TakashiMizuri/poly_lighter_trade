import { useEffect, useMemo, useState } from "react";
import { LogOutIcon } from "lucide-react";
import { CartesianGrid, Line, LineChart, ResponsiveContainer, Tooltip as RTooltip, XAxis, YAxis } from "recharts";
import {
  EventRow,
  EquityPoint,
  Forecast,
  OrderRow,
  Stats,
  Status,
  WindowRow,
  api,
  num,
} from "./api";
import { TimingsView } from "./TimingTimeline";
import { exitLabel, skipLabel } from "./labels";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
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

/** Лаг только у входов: open 5m UTC → биржевой created_at ордера Lighter. */
function tradeLag(w: WindowRow): number | null {
  if (!isTrade(w) || num(w.lighter_entry_px) == null) return null;
  return num(w.fill_lag_ms);
}

function sideBadge(side: string) {
  if (side === "long") return <Badge>LONG</Badge>;
  if (side === "short") return <Badge variant="outline">SHORT</Badge>;
  return <Badge variant="secondary">skip</Badge>;
}

type CurveRow = { ts: number; live: number | null; shadow: number | null };

function equityCurve(points: EquityPoint[]): CurveRow[] {
  const sorted = points.filter((p) => p.ts > 0).sort((a, b) => a.ts - b.ts);
  let live: number | null = null;
  let shadow: number | null = null;
  const out: CurveRow[] = [];
  for (const p of sorted) {
    if (p.live > 0) live = p.live;
    if (p.shadow > 0) shadow = p.shadow;
    if (live == null && shadow == null) continue;
    out.push({ ts: p.ts, live, shadow });
  }
  return out;
}

function yDomain(values: number[]): [number, number] {
  if (values.length === 0) return [0, 1];
  const min = Math.min(...values);
  const max = Math.max(...values);
  const span = max - min;
  const pad = span > 0 ? span * 0.16 : Math.max(Math.abs(max) * 0.01, 1);
  return [min - pad, max + pad];
}

const axisTick = { fontSize: 11, fill: "currentColor" };

function ChartTip({
  active,
  payload,
  label,
  money,
}: {
  active?: boolean;
  payload?: ReadonlyArray<{ name?: string; value?: unknown; color?: string }>;
  label?: unknown;
  money?: boolean;
}) {
  if (!active || !payload?.length) return null;
  const rows = payload.flatMap((p) => {
    const n = typeof p.value === "number" ? p.value : Number(p.value);
    if (!Number.isFinite(n)) return [];
    return [{ name: p.name || "", n, color: p.color }];
  });
  if (rows.length === 0) return null;
  return (
    <div className="rounded-md bg-popover px-2.5 py-1.5 text-xs ring-1 ring-foreground/10">
      <div className="mb-1 text-muted-foreground">{utc(Number(label))}</div>
      {rows.map((p) => (
        <div key={p.name} className="flex items-center gap-2 tabular-nums">
          <span className="h-1.5 w-1.5 rounded-full" style={{ background: p.color || "currentColor" }} />
          <span className="text-muted-foreground">{p.name}</span>
          <span className="text-foreground">{money ? `$${fmt(p.n)}` : `${fmt(p.n, 0)} мс`}</span>
        </div>
      ))}
    </div>
  );
}

function EquityChart({ points }: { points: EquityPoint[] }) {
  const data = equityCurve(points);
  const values = data.flatMap((r) => [r.live, r.shadow].filter((v): v is number => v != null));
  return (
    <section>
      <div className="mb-2 flex items-center gap-4 text-[11px] text-muted-foreground">
        <span className="text-foreground">Equity</span>
        <span className="inline-flex items-center gap-1.5">
          <i className="h-px w-3 bg-foreground" /> Lighter
        </span>
        <span className="inline-flex items-center gap-1.5">
          <i className="h-px w-3 bg-foreground/35" /> тень
        </span>
      </div>
      {data.length < 2 ? (
        <p className="text-sm text-muted-foreground">Кривая появится после пары окон</p>
      ) : (
        <div className="h-56 text-muted-foreground">
          <ResponsiveContainer width="100%" height="100%">
            <LineChart data={data} margin={{ top: 8, right: 8, left: 0, bottom: 0 }}>
              <CartesianGrid vertical={false} stroke="currentColor" strokeOpacity={0.12} />
              <XAxis dataKey="ts" tickFormatter={(v) => hhmm(Number(v))} minTickGap={32} tick={axisTick} tickLine={false} axisLine={false} />
              <YAxis domain={yDomain(values)} width={48} tickFormatter={(v) => fmt(Number(v), 0)} tick={axisTick} tickLine={false} axisLine={false} />
              <RTooltip content={<ChartTip money />} />
              <Line type="linear" dataKey="live" name="Lighter" stroke="var(--color-foreground)" strokeWidth={1.5} dot={false} connectNulls isAnimationActive={false} />
              <Line type="linear" dataKey="shadow" name="тень" stroke="var(--color-foreground)" strokeOpacity={0.35} strokeWidth={1.25} dot={false} connectNulls isAnimationActive={false} />
            </LineChart>
          </ResponsiveContainer>
        </div>
      )}
    </section>
  );
}

function LagChart({ series }: { series: { ts: number; lag: number }[] }) {
  return (
    <section>
      <div className="mb-2 text-[11px] text-muted-foreground">Лаг входов, мс</div>
      {series.length < 2 ? (
        <p className="text-sm text-muted-foreground">Нужны две сделки с временем ордера</p>
      ) : (
        <div className="h-40 text-muted-foreground">
          <ResponsiveContainer width="100%" height="100%">
            <LineChart data={series} margin={{ top: 8, right: 8, left: 0, bottom: 0 }}>
              <CartesianGrid vertical={false} stroke="currentColor" strokeOpacity={0.12} />
              <XAxis dataKey="ts" tickFormatter={(v) => hhmm(Number(v))} minTickGap={28} tick={axisTick} tickLine={false} axisLine={false} />
              <YAxis domain={yDomain(series.map((p) => p.lag))} width={40} tickFormatter={(v) => fmt(Number(v), 0)} tick={axisTick} tickLine={false} axisLine={false} />
              <RTooltip content={<ChartTip />} />
              <Line type="linear" dataKey="lag" name="лаг" stroke="var(--color-foreground)" strokeWidth={1.5} dot={{ r: 2, strokeWidth: 0, fill: "var(--color-foreground)" }} isAnimationActive={false} />
            </LineChart>
          </ResponsiveContainer>
        </div>
      )}
    </section>
  );
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
    let cancelled = false;
    let inFlight = false;
    const tick = async () => {
      if (cancelled || inFlight) return;
      inFlight = true;
      try {
        await refresh();
      } finally {
        inFlight = false;
      }
    };
    void tick();
    const id = setInterval(() => void tick(), 5000);
    return () => {
      cancelled = true;
      clearInterval(id);
    };
  }, []);

  async function kill() {
    setBusy(true);
    try {
      await api.kill();
      await refresh();
    } catch (e) {
      setErr(`Kill failed: ${String(e)}`);
    } finally {
      setBusy(false);
    }
  }
  async function resume() {
    setBusy(true);
    try {
      await api.resume();
      await refresh();
    } catch (e) {
      setErr(`Resume failed: ${String(e)}`);
    } finally {
      setBusy(false);
    }
  }

  const halted = Boolean(status?.kill_switch || status?.halted);
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
  const th = status?.threshold ?? 0.65;
  const signal =
    status && status.p_up >= th ? "вход LONG" : status && 1 - status.p_up >= th ? "вход SHORT" : "обычно skip";

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
          <TabsList className="mx-4 mt-4 w-fit">
            <TabsTrigger value="overview">Обзор</TabsTrigger>
            <TabsTrigger value="trades">Сделки</TabsTrigger>
            <TabsTrigger value="windows">Окна</TabsTrigger>
            <TabsTrigger value="exec">Ордера</TabsTrigger>
            <TabsTrigger value="clock">Часы</TabsTrigger>
            <TabsTrigger value="health">Журнал</TabsTrigger>
          </TabsList>

          <TabsContent value="overview" className="space-y-8 p-4">
            <div className="grid gap-6 sm:grid-cols-3">
              <div>
                <div className="text-[11px] text-muted-foreground">Окно</div>
                <div className="mt-1 text-base font-medium tabular-nums">{hhmm(status.window_ts)}</div>
                <div className="mt-0.5 text-xs text-muted-foreground">
                  {fmt(status.secs_into_window, 0)} с от :00 · до конца {fmt(status.secs_to_close, 0)} с
                </div>
                <div className="mt-0.5 text-xs">
                  {status.position}
                  {status.entry_px ? ` @ ${fmt(status.entry_px)}` : ""}
                  {status.stop_px ? ` · стоп ${fmt(status.stop_px)}` : ""}
                </div>
              </div>
              <div>
                <div className="text-[11px] text-muted-foreground">Polymarket</div>
                <div className="mt-1 text-base font-medium tabular-nums">
                  {fmt(status.p_up, 3)} <span className="font-normal text-muted-foreground">/</span> {fmt(status.p_down, 3)}
                </div>
                <div className="mt-0.5 text-xs text-muted-foreground">
                  {signal} · спред {fmt(status.spread, 3)} · {status.quote_source || "нет книги"}
                </div>
              </div>
              <div>
                <div className="text-[11px] text-muted-foreground">Лаг входа</div>
                <div className="mt-1 text-base font-medium tabular-nums">{stats?.n_fill_lag ? ms(stats.mean_fill_lag_ms) : "—"}</div>
                <div className="mt-0.5 text-xs text-muted-foreground">
                  p50 {ms(stats?.p50_fill_lag_ms)} · p90 {ms(stats?.p90_fill_lag_ms)} · n {stats?.n_fill_lag ?? 0}
                </div>
              </div>
            </div>

            <section>
              <div className="mb-2 text-[11px] text-muted-foreground">Последнее окно</div>
              <TimingsView t={windows[0]?.timings || status.last_timings} />
            </section>

            <EquityChart points={equity} />

            <div className="grid items-start gap-8 lg:grid-cols-2">
              <ForecastCard forecast={stats?.forecast} liveEquity={status.equity} />
              <LagChart series={lagSeries} />
            </div>

            <div className="grid grid-cols-2 gap-x-6 gap-y-4 sm:grid-cols-3">
              <Metric label="Equity" value={`$${fmt(status.equity)}`} sub={`пик $${fmt(status.peak_equity)}`} />
              <Metric label="WR" value={`${fmt((stats?.wr ?? 0) * 100, 1)}%`} sub={`mean ${fmt(stats?.mean_bps, 1)} bps · SL ${stats?.n_sl ?? 0}`} />
              <Metric label="Сделки" value={`${stats?.n_trades ?? 0}`} sub={`skip ${stats?.n_skip ?? 0} · окон ${stats?.n_windows ?? 0}`} />
              <Metric label="Тень" value={`${fmt(stats?.shadow_mean_bps, 1)} bps`} sub={`зазор ${fmt(stats?.gap_bps, 1)}`} />
              <Metric
                label="Последний вход"
                value={lastTrade ? `${lastTrade.side} ${ms(tradeLag(lastTrade))}` : "—"}
                sub={lastTrade ? hhmm(lastTrade.window_ts) : undefined}
              />
              <Metric label="Покрытие" value={`${fmt((stats?.coverage ?? 0) * 100, 0)}%`} sub="котировка не старше 1 с" />
            </div>

            <div className="grid gap-8 lg:grid-cols-2">
              <section>
                <div className="mb-2 text-[11px] text-muted-foreground">Почему skip</div>
                {skipMix.length === 0 ? (
                  <p className="text-sm text-muted-foreground">Пока нет</p>
                ) : (
                  <ul className="space-y-1 text-sm">
                    {skipMix.map(([k, n]) => (
                      <li key={k} className="flex justify-between gap-3">
                        <span>{skipLabel(k, th)}</span>
                        <span className="tabular-nums text-muted-foreground">{n}</span>
                      </li>
                    ))}
                  </ul>
                )}
              </section>
              <section>
                <div className="mb-2 text-[11px] text-muted-foreground">Последние окна</div>
                <Table>
                  <TableBody>
                    {windows.slice(0, 6).map((w) => (
                      <TableRow key={w.window_ts}>
                        <TableCell className="text-xs tabular-nums">{hhmm(w.window_ts)}</TableCell>
                        <TableCell>{sideBadge(w.side)}</TableCell>
                        <TableCell className="text-xs tabular-nums">{fmt(w.p_up, 3)}</TableCell>
                        <TableCell className={`text-xs ${pnlClass(num(w.pnl_usd))}`}>
                          {isTrade(w) ? (num(w.pnl_usd) != null ? `$${fmt(num(w.pnl_usd), 2)}` : exitLabel(w.exit_reason) || "open") : skipLabel(w.skip_reason, th)}
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </section>
            </div>

            <div className="flex gap-2">
              <Button variant="destructive" size="sm" disabled={busy} onClick={() => void kill()}>
                Kill / flatten
              </Button>
              <Button variant="outline" size="sm" disabled={busy} onClick={() => void resume()}>
                Resume
              </Button>
            </div>
          </TabsContent>

          <TabsContent value="trades" className="space-y-3 p-4">
            <p className="text-xs text-muted-foreground">Лаг — created_at ордера на Lighter минус :00.</p>
            <div className="grid grid-cols-2 gap-x-6 gap-y-4 sm:grid-cols-4">
              <Metric label="Средний лаг" value={stats?.n_fill_lag ? ms(stats.mean_fill_lag_ms) : "—"} />
              <Metric label="p50 / p90" value={`${ms(stats?.p50_fill_lag_ms)} / ${ms(stats?.p90_fill_lag_ms)}`} />
              <Metric label="С лагом" value={String(stats?.n_fill_lag ?? 0)} />
              <Metric label="WR" value={`${fmt((stats?.wr ?? 0) * 100, 1)}%`} sub={`mean ${fmt(stats?.mean_bps, 1)} bps`} />
            </div>
            <WindowTable rows={trades} tradesOnly threshold={th} />
          </TabsContent>

          <TabsContent value="windows" className="p-4">
            <p className="mb-3 text-xs text-muted-foreground">Ромб на шкале — :00. Числа над линией — миллисекунды.</p>
            <WindowTable rows={windows} threshold={th} />
          </TabsContent>

          <TabsContent value="exec" className="space-y-3 p-4">
            <div className="grid grid-cols-2 gap-x-6 gap-y-4 sm:grid-cols-4">
              <Metric label="Средний лаг" value={stats?.n_fill_lag ? ms(stats.mean_fill_lag_ms) : "—"} />
              <Metric label="p50 / p90" value={`${ms(stats?.p50_fill_lag_ms)} / ${ms(stats?.p90_fill_lag_ms)}`} />
              <Metric label="Пары с тенью" value={String(stats?.n_paired ?? 0)} />
              <Metric label="Выходы по SL" value={String(stats?.n_sl ?? 0)} />
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
            <p className="text-xs text-muted-foreground">
              Дедлайн входа — системный UTC. Замер Binance на него не влияет.
            </p>
            <div className="grid grid-cols-2 gap-x-6 gap-y-4 sm:grid-cols-4">
              <Metric label="Binance − host" value={status.clock_ok ? signedMs(status.clock_offset_ms) : "—"} />
              <Metric label="RTT" value={ms(status.clock_rtt_ms)} />
              <Metric label="Замер" value={status.clock_ok ? ms(status.clock_age_ms) : "нет"} sub={status.clock_ok ? "назад" : "устарел"} />
              <Metric label="Дедлайн" value="T+2 с" />
            </div>
          </TabsContent>

          <TabsContent value="health" className="space-y-3 p-4">
            <p className="text-sm">
              {halted ? `HALT${status.halt_reason ? ` · ${status.halt_reason}` : ""}` : status.state}
              <span className="text-muted-foreground"> · {fmt(status.uptime_sec / 60, 0)} мин · порог {fmt(status.threshold, 2)}</span>
            </p>
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

function Metric({ label, value, sub }: { label: string; value: string; sub?: string }) {
  return (
    <div className="min-w-0">
      <div className="text-[11px] text-muted-foreground">{label}</div>
      <div className="mt-0.5 truncate text-base font-medium tabular-nums">{value}</div>
      {sub ? <div className="truncate text-[11px] text-muted-foreground">{sub}</div> : null}
    </div>
  );
}

function confidenceLabel(c: string | undefined) {
  if (c === "high") return "высокая";
  if (c === "medium") return "средняя";
  return "низкая";
}

function ForecastCard({ forecast, liveEquity }: { forecast?: Forecast; liveEquity?: number }) {
  const f = forecast;
  const base = liveEquity && liveEquity > 0 ? liveEquity : f?.equity_now;
  const scale = f && f.equity_now > 0 && base && base > 0 ? base / f.equity_now : 1;
  return (
    <section>
      <div className="mb-2 text-[11px] text-muted-foreground">Прогноз</div>
      {!f || !f.ready ? (
        <p className="text-sm text-muted-foreground">{f?.note || "Нужны закрытые сделки"}</p>
      ) : (
        <>
          <div className="grid grid-cols-3 gap-3">
            <ForecastHorizon label="Неделя" equity={f.equity_1w * scale} retPct={f.ret_1w_pct} />
            <ForecastHorizon label="Месяц" equity={f.equity_1m * scale} retPct={f.ret_1m_pct} />
            <ForecastHorizon label="Год" equity={f.equity_1y * scale} retPct={f.ret_1y_pct} />
          </div>
          <p className="mt-2 text-[11px] text-muted-foreground">
            {f.n_trades} сделок · mean {fmt(f.mean_bps, 1)} bps · {confidenceLabel(f.confidence)}
          </p>
        </>
      )}
    </section>
  );
}

function ForecastHorizon({ label, equity, retPct }: { label: string; equity: number; retPct: number }) {
  const tone =
    retPct > 0 ? "text-emerald-700" : retPct < 0 ? "text-red-700" : "text-foreground";
  const sign = retPct > 0 ? "+" : "";
  return (
    <div>
      <div className="text-xs text-muted-foreground">{label}</div>
      <div className={`text-lg font-medium tabular-nums ${tone}`}>${fmt(equity)}</div>
      <div className={`text-xs tabular-nums ${tone}`}>
        {sign}
        {fmt(retPct, 1)}%
      </div>
    </div>
  );
}

function WindowTable({ rows, tradesOnly, threshold = 0.65 }: { rows: WindowRow[]; tradesOnly?: boolean; threshold?: number }) {
  if (rows.length === 0) {
    return <p className="text-sm text-muted-foreground">{tradesOnly ? "Сделок ещё нет" : "Окон ещё нет"}</p>;
  }
  return (
    <>
      <div className="grid gap-2 xl:hidden">
        {rows.slice(0, 80).map((w) => (
          <Card key={w.window_ts}>
            <CardContent className="space-y-3 p-3">
              <div className="flex items-start justify-between gap-3">
                <div className="min-w-0">
                  <div className="text-xs font-medium tabular-nums">{utc(w.window_ts)}</div>
                  <div className="mt-1 text-xs text-muted-foreground">
                    {w.exit_reason ? exitLabel(w.exit_reason) : w.skip_reason ? skipLabel(w.skip_reason, threshold) : "окно"}
                  </div>
                </div>
                {sideBadge(w.side)}
              </div>
              <div className="grid grid-cols-2 gap-2 sm:grid-cols-3">
                <MiniMetric label="PM Up" value={fmt(w.p_up, 3)} />
                <MiniMetric label="Спред" value={fmt(w.spread, 3)} />
                {!tradesOnly ? <MiniMetric label="Источник" value={w.quote_source || "—"} /> : null}
                <MiniMetric label="Лаг :00 → ордер" value={ms(tradeLag(w))} />
                <MiniMetric label="Lighter in" value={fmt(num(w.lighter_entry_px), 1)} />
                <MiniMetric label="Lighter out" value={fmt(num(w.lighter_exit_px), 1)} />
                <MiniMetric label="Стоп" value={fmt(num(w.stop_px), 1)} />
                {tradesOnly ? <MiniMetric label="PnL" value={fmt(num(w.pnl_usd), 2)} /> : null}
                {tradesOnly ? <MiniMetric label="Return bps" value={fmt(num(w.ret_bps), 1)} /> : null}
                {tradesOnly ? <MiniMetric label="Тень bps" value={fmt(num(w.shadow_ret_bps), 1)} /> : null}
                {tradesOnly ? <MiniMetric label="SL slip bps" value={fmt(num(w.sl_slip_bps), 1)} /> : null}
              </div>
              <div className="min-w-0 border-t pt-2">
                <div className="mb-1 text-[10px] font-medium text-muted-foreground">Таймлайн от :00</div>
                <TimingsView t={w.timings} compact />
              </div>
            </CardContent>
          </Card>
        ))}
      </div>
      <div className="hidden overflow-x-auto xl:block">
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
            <TableHead>таймлайн, мс</TableHead>
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
              <TableCell className="text-xs">{w.exit_reason ? exitLabel(w.exit_reason) : "—"}</TableCell>
              <TableCell>{fmt(w.p_up, 3)}</TableCell>
              <TableCell>{fmt(w.spread, 3)}</TableCell>
              <TableCell className="text-xs">{w.quote_source || "—"}</TableCell>
              {!tradesOnly ? <TableCell className="text-xs">{w.skip_reason ? skipLabel(w.skip_reason, threshold) : "—"}</TableCell> : null}
              <TableCell>{ms(tradeLag(w))}</TableCell>
              <TableCell className="max-w-[40rem] min-w-[22rem] whitespace-normal">
                <TimingsView t={w.timings} compact />
              </TableCell>
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
    </>
  );
}

function MiniMetric({ label, value }: { label: string; value: string }) {
  return (
    <div className="min-w-0 rounded-md bg-muted/60 px-2 py-1.5">
      <div className="text-[10px] leading-tight text-muted-foreground">{label}</div>
      <div className="truncate text-xs font-medium tabular-nums">{value}</div>
    </div>
  );
}
