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

  return (
    <div className="mx-auto flex min-h-dvh max-w-5xl flex-col">
      <header className="sticky top-0 z-10 flex items-center justify-between gap-3 border-b bg-background/90 px-4 py-3 backdrop-blur">
        <div className="min-w-0">
          <div className="font-heading text-sm font-medium">POLY_LIGHTER_1</div>
          <div className="truncate text-xs text-muted-foreground">
            {status
              ? `${status.instance_name} · ${status.network}${status.dry_run ? " · DRY" : ""} · ${fmt(status.leverage, 1)}x · ${status.state}`
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
          <TabsList className="mx-4 mt-3 grid w-auto grid-cols-5">
            <TabsTrigger value="overview">Обзор</TabsTrigger>
            <TabsTrigger value="windows">Окна</TabsTrigger>
            <TabsTrigger value="trades">Сделки</TabsTrigger>
            <TabsTrigger value="exec">Исполнение</TabsTrigger>
            <TabsTrigger value="health">Health</TabsTrigger>
          </TabsList>

          <TabsContent value="overview" className="space-y-3 p-4">
            <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
              <Stat label="Equity" value={`$${fmt(status.equity)}`} />
              <Stat label="p_up" value={fmt(status.p_up, 3)} />
              <Stat label="Coverage" value={`${fmt((stats?.coverage ?? 0) * 100, 1)}%`} />
              <Stat label="WR live" value={`${fmt((stats?.wr ?? 0) * 100, 1)}%`} />
              <Stat label="mean bps live" value={fmt(stats?.mean_bps, 2)} />
              <Stat label="mean bps тень" value={fmt(stats?.shadow_mean_bps, 2)} />
              <Stat label="gap live − тень" value={fmt(stats?.gap_bps, 2)} />
              <Stat
                label="соответствие"
                value={stats?.match_coef == null ? "—" : fmt(stats.match_coef, 2)}
              />
              <Stat label="p50 lag" value={`${fmt(stats?.p50_fill_lag_ms, 0)} ms`} />
              <Stat label="n trades" value={String(stats?.n_trades ?? 0)} />
            </div>
            <Card>
              <CardHeader>
                <CardTitle className="text-sm">Equity live vs shadow T+1s</CardTitle>
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
              Позиция {status.position}
              {status.entry_px ? ` @ ${fmt(status.entry_px)}` : ""} · quote {status.quote_source || "—"} · clock{" "}
              {fmt(status.clock_offset_ms, 0)} ms · last skip {status.last_skip || "—"}
              {stats?.n_paired ? ` · paired ${stats.n_paired}` : ""}
              {stats?.match_coef == null
                ? " · соответствие = live / тень, если |тень| ≥ 0.05 bps"
                : ""}
            </p>
          </TabsContent>

          <TabsContent value="windows" className="p-4">
            <WindowTable rows={windows} />
          </TabsContent>
          <TabsContent value="trades" className="p-4">
            <WindowTable rows={trades} tradesOnly />
          </TabsContent>
          <TabsContent value="exec" className="space-y-3 p-4">
            <div className="grid grid-cols-2 gap-3">
              <Stat label="gap live − тень" value={fmt(stats?.gap_bps, 2)} />
              <Stat
                label="соответствие live/тень"
                value={stats?.match_coef == null ? "—" : fmt(stats.match_coef, 2)}
              />
              <Stat label="p50 entry lag" value={`${fmt(stats?.p50_lag_ms, 0)} ms`} />
              <Stat label="p90 entry lag" value={`${fmt(stats?.p90_lag_ms, 0)} ms`} />
              <Stat label="p50 fill lag" value={`${fmt(stats?.p50_fill_lag_ms, 0)} ms`} />
              <Stat label="p90 fill lag" value={`${fmt(stats?.p90_fill_lag_ms, 0)} ms`} />
            </div>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>окно</TableHead>
                  <TableHead>kind</TableHead>
                  <TableHead>status</TableHead>
                  <TableHead>qty</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {orders.slice(0, 40).map((o) => (
                  <TableRow key={o.id}>
                    <TableCell className="text-xs">{utc(o.window_ts)}</TableCell>
                    <TableCell>{o.kind}</TableCell>
                    <TableCell>{o.status}</TableCell>
                    <TableCell>{fmt(o.qty, 5)}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </TabsContent>
          <TabsContent value="health" className="space-y-3 p-4">
            <Alert variant={halted ? "destructive" : "default"}>
              <AlertTitle>{halted ? "HALT" : "OK"}</AlertTitle>
              <AlertDescription>
                {status.halt_reason || status.state} · uptime {fmt(status.uptime_sec / 60, 1)} мин
              </AlertDescription>
            </Alert>
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
                    <TableCell className="max-w-[16rem] truncate text-xs">{ev.message}</TableCell>
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

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <Card>
      <CardHeader className="pb-1">
        <CardTitle className="text-xs font-normal text-muted-foreground">{label}</CardTitle>
      </CardHeader>
      <CardContent className="text-lg font-medium">{value}</CardContent>
    </Card>
  );
}

function WindowTable({ rows, tradesOnly }: { rows: WindowRow[]; tradesOnly?: boolean }) {
  return (
    <Table>
      <TableHeader>
        <TableRow>
          <TableHead>окно UTC</TableHead>
          <TableHead>side</TableHead>
          <TableHead>p_up</TableHead>
          <TableHead>skip</TableHead>
          {!tradesOnly ? null : <TableHead>pnl</TableHead>}
          <TableHead>lag</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {rows.slice(0, 80).map((w) => (
          <TableRow key={w.window_ts}>
            <TableCell className="text-xs">{utc(w.window_ts)}</TableCell>
            <TableCell>{w.side}</TableCell>
            <TableCell>{fmt(w.p_up, 3)}</TableCell>
            <TableCell className="text-xs">{w.skip_reason || "—"}</TableCell>
            {tradesOnly ? <TableCell>{fmt(num(w.pnl_usd), 2)}</TableCell> : null}
            <TableCell>{fmt(num(w.fill_lag_ms) ?? num(w.entry_lag_ms), 0)}</TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  );
}
