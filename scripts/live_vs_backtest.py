#!/usr/bin/env python3
"""Compare live dashboard trades to the canonical T+1s / 0.125% Binance backtest.

The gate is the bot's own p_up at the window open (favorite >= 0.65).
CLOB prices-history is not a signal: its first print is often 10–15s late
and pulls in windows the bot never quoted at the threshold.

Price path, same as the research canon:
  entry = BTCUSDT 1s close at T+1
  stop  = 0.125% of that entry, checked on 1s high/low from T+2 through T+299
  else  = 1s close at T+299
A model stop is always -12.5 bps.

Paired mean / sum / WR use only rows the bot actually entered (LONG/SHORT
with a ret bps). Windows that cleared the gate and were skipped are printed
and left out of that mean.

1s bars come from the research repo daily zips
(data/binance/btcusdt_1s/daily/BTCUSDT-1s-YYYY-MM-DD.zip). Seconds the zip
does not have are filled from Binance spot REST klines.

Input is a dashboard paste. The trades tab is the column order this parser
expects (no skip column): window, side, exit, p_up, spread, quote, lag,
timings, lighter in, lighter out, stop, pnl, ret bps, shadow bps, sl slip.
The windows tab is used only to see skips that still cleared the gate.
"""
from __future__ import annotations

import argparse
import csv
import io
import json
import time
import urllib.request
import zipfile
from dataclasses import dataclass
from datetime import datetime, timedelta, timezone
from pathlib import Path

SL = 0.00125
THRESHOLD = 0.65
# Startpack §12 / §13: live mean worse than the model by this much is the red line.
# The no-stop research mean is the whole edge a constant drag can eat.
KILL_GAP_BPS = 1.5
EDGE_BPS = 1.72
DEFAULT_RESEARCH = Path(r"C:\All\Develop\new-algotrading-adventure")
BINANCE_KLINES = "https://api.binance.com/api/v3/klines"


@dataclass
class Row:
    ts: int
    iso: str
    side: int  # +1 long, -1 short, 0 not an entry
    p_up: float
    exit_label: str
    live_bps: float | None
    shadow_bps: float | None

    @property
    def entered(self) -> bool:
        return self.side != 0 and self.live_bps is not None


def parse_num(value: str) -> float | None:
    text = value.strip().replace(",", "")
    if not text or text in {"—", "-", "–"}:
        return None
    try:
        return float(text)
    except ValueError:
        return None


def parse_side(value: str) -> int | None:
    token = value.strip().lower()
    if token in {"long", "l"}:
        return 1
    if token in {"short", "s"}:
        return -1
    if token in {"skip", "—", "-", ""}:
        return 0
    return None


def parse_iso(value: str) -> int | None:
    text = value.strip()
    if not text.endswith("Z") or "T" not in text:
        return None
    try:
        return int(datetime.fromisoformat(text.replace("Z", "+00:00")).timestamp())
    except ValueError:
        return None


def parse_paste(text: str) -> list[Row]:
    """Merge a dashboard paste. A trades-tab row (ret bps present) wins over a windows-tab repeat."""
    by_ts: dict[int, Row] = {}
    for line in text.splitlines():
        if not line.startswith("20"):
            continue
        parts = line.split("\t")
        if len(parts) < 4:
            continue
        ts = parse_iso(parts[0])
        side = parse_side(parts[1])
        p_up = parse_num(parts[3])
        if ts is None or side is None or p_up is None:
            continue
        live = parse_num(parts[12]) if len(parts) > 12 else None
        shadow = parse_num(parts[13]) if len(parts) > 13 else None
        # Windows tab inserts a skip column, so index 12 is not ret bps there.
        if side != 0 and live is None:
            side_for_row = side
            live_bps = None
            shadow_bps = None
        elif side != 0 and live is not None:
            side_for_row = side
            live_bps = live
            shadow_bps = shadow
        else:
            side_for_row = 0
            live_bps = None
            shadow_bps = None
        row = Row(
            ts=ts,
            iso=parts[0].strip(),
            side=side_for_row,
            p_up=p_up,
            exit_label=parts[2].strip(),
            live_bps=live_bps,
            shadow_bps=shadow_bps,
        )
        prev = by_ts.get(ts)
        if prev is None or (row.entered and not prev.entered):
            by_ts[ts] = row
        elif prev.entered and not row.entered:
            continue
        elif row.p_up and not prev.p_up:
            by_ts[ts] = row
    return [by_ts[ts] for ts in sorted(by_ts)]


def gate_side(p_up: float, threshold: float) -> int:
    """+1 / -1 when the bot's quote cleared the favorite threshold, else 0."""
    if p_up >= threshold:
        return 1
    if p_up > 0 and (1.0 - p_up) >= threshold:
        return -1
    return 0


def sim(
    side: int,
    ts: int,
    bars: dict[int, tuple[float, float, float]],
    sl_pct: float = SL,
) -> tuple[bool, float] | None:
    """Return (stop_hit, ret_bps). Entry is the T+1 close. Stop scans T+2..T+299, then the T+299 close."""
    entry_bar = bars.get(ts + 1)
    close_bar = bars.get(ts + 299)
    if entry_bar is None or close_bar is None:
        return None
    entry = entry_bar[2]
    stop = entry * (1.0 - sl_pct) if side == 1 else entry * (1.0 + sl_pct)
    hit = False
    for t in range(ts + 2, ts + 300):
        bar = bars.get(t)
        if bar is None:
            continue
        high, low, _ = bar
        if (side == 1 and low <= stop) or (side == -1 and high >= stop):
            hit = True
            break
    close5 = close_bar[2]
    if not hit and ((side == 1 and close5 <= stop) or (side == -1 and close5 >= stop)):
        hit = True
    ret = -sl_pct * 1e4 if hit else side * (close5 / entry - 1.0) * 1e4
    return hit, ret


def _normalize_open_ms(value: int) -> int:
    return value // 1000 if value > 10**14 else value


def _read_zip(path: Path, t_min: int, t_max: int, bars: dict[int, tuple[float, float, float]]) -> None:
    with zipfile.ZipFile(path) as zf:
        name = next(n for n in zf.namelist() if n.endswith(".csv"))
        with zf.open(name) as raw:
            for row in csv.reader(io.TextIOWrapper(raw, encoding="utf-8")):
                if len(row) < 5:
                    continue
                try:
                    ot = _normalize_open_ms(int(row[0])) // 1000
                except ValueError:
                    continue
                if t_min <= ot <= t_max:
                    bars[ot] = (float(row[2]), float(row[3]), float(row[4]))


def _fetch_klines(start_s: int, end_s: int, bars: dict[int, tuple[float, float, float]]) -> None:
    cursor = start_s * 1000
    end_ms = end_s * 1000
    while cursor <= end_ms:
        url = (
            f"{BINANCE_KLINES}?symbol=BTCUSDT&interval=1s&limit=1000"
            f"&startTime={cursor}&endTime={end_ms}"
        )
        req = urllib.request.Request(url, headers={"User-Agent": "poly-lighter-live-vs-bt/1"})
        with urllib.request.urlopen(req, timeout=30) as resp:
            batch = json.loads(resp.read())
        if not batch:
            break
        for k in batch:
            bars[int(k[0]) // 1000] = (float(k[2]), float(k[3]), float(k[4]))
        cursor = int(batch[-1][0]) + 1000
        if len(batch) < 1000:
            break
        time.sleep(0.05)


def load_bars(research_root: Path, t_min: int, t_max: int) -> dict[int, tuple[float, float, float]]:
    """Load 1s (high, low, close) for [t_min, t_max]. Daily vision zip first, REST for the rest."""
    bars: dict[int, tuple[float, float, float]] = {}
    daily = research_root / "data" / "binance" / "btcusdt_1s" / "daily"
    day = datetime.fromtimestamp(t_min, timezone.utc).date()
    last = datetime.fromtimestamp(t_max, timezone.utc).date()
    while day <= last:
        path = daily / f"BTCUSDT-1s-{day.isoformat()}.zip"
        if path.exists():
            _read_zip(path, t_min, t_max, bars)
        day += timedelta(days=1)
    missing_from = next((t for t in range(t_min, t_max + 1) if t not in bars), None)
    if missing_from is not None:
        _fetch_klines(missing_from, t_max, bars)
    return bars


def _fmt(value: float | None, width: int = 7, digits: int = 2) -> str:
    if value is None:
        return " " * (width - 1) + "—"
    return f"{value:+{width}.{digits}f}"


def report(rows: list[Row], bars: dict[int, tuple[float, float, float]], threshold: float) -> int:
    paired: list[tuple[Row, bool, float]] = []
    skipped: list[tuple[Row, int, bool, float]] = []
    missing: list[str] = []
    for row in rows:
        if row.entered:
            side = row.side
        else:
            side = gate_side(row.p_up, threshold)
            if side == 0:
                continue
        out = sim(side, row.ts, bars)
        if out is None:
            missing.append(row.iso)
            continue
        hit, ret = out
        if row.entered:
            gated = gate_side(row.p_up, threshold)
            if gated != 0 and gated != row.side:
                print(f"{row.iso}  live side {row.side} != gate side {gated} from p_up {row.p_up:.3f}")
            paired.append((row, hit, ret))
        else:
            skipped.append((row, side, hit, ret))

    if not paired and not skipped:
        print("no window in the paste cleared the 0.65 gate")
        return 1

    print("time   side   exit        backtest    live   shadow   live-bt")
    bt: list[float] = []
    live: list[float] = []
    for row, hit, ret in paired:
        name = "LONG" if row.side == 1 else "SHORT"
        how = "stop" if hit else "close"
        delta = None if row.live_bps is None else row.live_bps - ret
        bt.append(ret)
        if row.live_bps is not None:
            live.append(row.live_bps)
        print(
            f"{row.iso[11:16]}  {name:5}  {how:5}  {_fmt(ret)}  {_fmt(row.live_bps, 6, 1)}  "
            f"{_fmt(row.shadow_bps, 6, 1)}  {_fmt(delta)}"
        )

    n = len(bt)
    if n:
        gap = [l - b for l, b in zip(live, bt)]
        live_stops = sum(1 for row, _, _ in paired if "стоп" in row.exit_label or row.exit_label.lower() in {"sl", "stop"})
        print(
            f"backtest  n={n}  wr={sum(1 for r in bt if r > 0)}/{n}  "
            f"stops={sum(1 for _, hit, _ in paired if hit)}  "
            f"mean={sum(bt)/n:.2f}  sum={sum(bt):.2f}"
        )
        print(
            f"live      n={len(live)}  wr={sum(1 for r in live if r > 0)}/{len(live)}  "
            f"stops={live_stops}  mean={sum(live)/len(live):.2f}  sum={sum(live):.2f}"
        )
        print(
            f"live-bt   mean={sum(gap)/len(gap):.2f}  sum={sum(gap):.2f}  "
            f"mae={sum(abs(g) for g in gap)/len(gap):.2f}  "
            f"same_sign={sum(1 for l, b in zip(live, bt) if (l > 0) == (b > 0))}/{n}"
        )
        print(
            f"gap {sum(gap)/len(gap):.2f} bps vs kill line {KILL_GAP_BPS:.1f} bps "
            f"and edge {EDGE_BPS:.2f} bps (startpack §12–§13)"
        )
    if skipped:
        print(f"gated skips excluded from the mean: {len(skipped)}")
        for row, side, hit, ret in skipped:
            name = "LONG" if side == 1 else "SHORT"
            how = "stop" if hit else "close"
            print(f"  {row.iso}  {name}  p_up={row.p_up:.3f}  {how} {ret:+.2f}  {row.exit_label}")
    if missing:
        print("no 1s path: " + ", ".join(missing))
        return 1
    return 0


def self_test() -> None:
    bars = {t: (100.0, 100.0, 100.0) for t in range(1, 301)}
    bars[1] = (100.0, 100.0, 100.0)
    bars[299] = (101.0, 101.0, 101.0)
    hit, ret = sim(1, 0, bars) or (None, None)
    assert hit is False and ret is not None and abs(ret - 100.0) < 1e-9
    bars[10] = (100.0, 99.8, 99.9)
    hit, ret = sim(1, 0, bars) or (None, None)
    assert hit is True and ret is not None and abs(ret - (-12.5)) < 1e-9
    paste = "\n".join(
        [
            "2026-11-21T21:30:00Z\tSHORT\tclose окна\t0.345\t0.010\tws_clob\t0 ms\ttrace\t1\t2\t3\t0.02\t8.4\t9.7\t—",
            "2026-11-21T23:30:00Z\tskip\tskip\t0.855\t0.010\tws_clob\tready",
            "2026-11-21T23:55:00Z\tskip\tskip\t0.645\t0.010\tws_clob\tbelow",
        ]
    )
    rows = parse_paste(paste)
    assert len(rows) == 3
    assert rows[0].entered and rows[0].live_bps == 8.4 and rows[0].side == -1
    assert gate_side(rows[1].p_up, THRESHOLD) == 1 and not rows[1].entered
    assert gate_side(rows[2].p_up, THRESHOLD) == 0
    print("self-test ok")


def main() -> int:
    parser = argparse.ArgumentParser(description="Live dashboard trades vs canonical Binance T+1 / SL 0.125% backtest")
    parser.add_argument("paste", type=Path, nargs="?", help="dashboard TSV paste")
    parser.add_argument(
        "--research-root",
        type=Path,
        default=DEFAULT_RESEARCH,
        help="new-algotrading-adventure checkout that holds data/binance/btcusdt_1s",
    )
    parser.add_argument("--threshold", type=float, default=THRESHOLD)
    parser.add_argument("--self-test", action="store_true")
    args = parser.parse_args()
    if args.self_test:
        self_test()
        return 0
    if args.paste is None:
        parser.error("paste file is required")
    rows = parse_paste(args.paste.read_text(encoding="utf-8"))
    needed = [r for r in rows if r.entered or gate_side(r.p_up, args.threshold) != 0]
    if not needed:
        print("paste has no entered trade and no quote at the threshold")
        return 1
    t_min = min(r.ts + 1 for r in needed)
    t_max = max(r.ts + 299 for r in needed)
    bars = load_bars(args.research_root, t_min, t_max)
    print(f"rows {len(rows)}  1s bars {len(bars)}  {datetime.fromtimestamp(t_min, timezone.utc)} .. {datetime.fromtimestamp(t_max, timezone.utc)}")
    return report(rows, bars, args.threshold)


if __name__ == "__main__":
    raise SystemExit(main())
