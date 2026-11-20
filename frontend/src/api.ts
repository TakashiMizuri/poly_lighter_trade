export type Status = {
  profile: string;
  instance_id: string;
  instance_name: string;
  network: string;
  dry_run: boolean;
  state: string;
  kill_switch: boolean;
  halted: boolean;
  halt_reason: string;
  clock_offset_ms: number;
  window_ts: number;
  next_ts: number;
  p_up: number;
  p_down: number;
  spread: number;
  quote_source: string;
  quote_age_ms: number;
  position: string;
  qty: number;
  entry_px: number;
  entry_window_ts: number;
  equity: number;
  peak_equity: number;
  shadow_equity: number;
  last_skip: string;
  uptime_sec: number;
  symbol: string;
  threshold: number;
  leverage: number;
  equity_fraction: number;
};

export type Stats = {
  n_windows: number;
  n_quoted: number;
  n_trades: number;
  n_skip: number;
  n_paired: number;
  coverage: number;
  wr: number;
  mean_bps: number;
  shadow_mean_bps: number;
  gap_bps: number;
  match_coef: number | null;
  p50_lag_ms: number;
  p90_lag_ms: number;
  p50_fill_lag_ms: number;
  p90_fill_lag_ms: number;
};

export type NullF = { Float64: number; Valid: boolean } | number | null;

export type WindowRow = {
  window_ts: number;
  decision_ts: number;
  quote_ts: number;
  p_up: number;
  p_down: number;
  spread: number;
  quote_source: string;
  side: string;
  skip_reason: string;
  lighter_entry_ts: NullF;
  lighter_entry_px: NullF;
  lighter_exit_ts: NullF;
  lighter_exit_px: NullF;
  qty: NullF;
  notional: NullF;
  equity_before: NullF;
  equity_after: NullF;
  pnl_usd: NullF;
  ret_bps: NullF;
  fees_usd: NullF;
  entry_lag_ms: NullF;
  fill_lag_ms: NullF;
  shadow_binance_px_t1: NullF;
  shadow_ret_bps: NullF;
  pm_fav: NullF;
  state: string;
};

export type EquityPoint = { ts: number; live: number; shadow: number };
export type EventRow = { ts: number; level: string; kind: string; message: string };
export type OrderRow = {
  id: number;
  window_ts: number;
  kind: string;
  status: string;
  client_order_index: number;
  tx_hash: string;
  qty: number;
  price: number;
  created_at: number;
};

export function num(v: NullF | undefined): number | null {
  if (v == null) return null;
  if (typeof v === "number") return v;
  return v.Valid ? v.Float64 : null;
}

async function req<T>(path: string, init?: RequestInit): Promise<T> {
  const r = await fetch(path, { credentials: "include", ...init });
  if (r.status === 401) throw new Error("auth");
  if (!r.ok) {
    const t = await r.text();
    throw new Error(t || r.statusText);
  }
  return r.json() as Promise<T>;
}

export const api = {
  login: (password: string) =>
    req<{ ok: string }>("/api/login", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ password }),
    }),
  logout: () => req("/api/logout", { method: "POST" }),
  status: () => req<Status>("/api/status"),
  stats: () => req<Stats>("/api/stats"),
  windows: () => req<WindowRow[]>("/api/windows"),
  trades: () => req<WindowRow[]>("/api/trades"),
  orders: () => req<OrderRow[]>("/api/orders"),
  equity: () => req<EquityPoint[]>("/api/equity"),
  events: () => req<EventRow[]>("/api/events"),
  kill: () => req("/api/kill", { method: "POST" }),
  resume: () => req("/api/resume", { method: "POST" }),
};
