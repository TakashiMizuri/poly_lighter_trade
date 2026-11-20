#!/usr/bin/env python3
"""Live Lighter clock offset. Read-only: does not change system time.

GET https://mainnet.zklighter.elliot.ai/  →  {status, network_id, timestamp}
timestamp is UNIX **seconds** (not ms like Binance). HTTP Date is also 1s grain.

offset_ms = Lighter_timestamp − midpoint(local t0, t1)
Positive = Lighter ahead of this machine.
"""
from __future__ import annotations

import argparse
import json
import sys
import time
import urllib.error
import urllib.request
from email.utils import parsedate_to_datetime

URL = "https://mainnet.zklighter.elliot.ai/"


def measure(timeout: float) -> tuple[float, float, float | None]:
    t0 = time.time()
    req = urllib.request.Request(URL, headers={"User-Agent": "poly-lighter-clock/1"})
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        date_hdr = resp.headers.get("Date")
        raw = resp.read()
    t1 = time.time()
    mid = t0 + (t1 - t0) / 2
    rtt_ms = (t1 - t0) * 1000.0
    body = json.loads(raw)
    ts = body["timestamp"]
    if ts > 1_000_000_000_000:
        ts = ts / 1000.0
    off_ms = (float(ts) - mid) * 1000.0
    date_off = None
    if date_hdr:
        try:
            date_off = (parsedate_to_datetime(date_hdr).timestamp() - mid) * 1000.0
        except (TypeError, ValueError, OverflowError):
            date_off = None
    return off_ms, rtt_ms, date_off


def main() -> int:
    p = argparse.ArgumentParser(description="Print Lighter vs local clock every N seconds")
    p.add_argument("-n", "--interval", type=float, default=10.0)
    p.add_argument("--timeout", type=float, default=5.0)
    p.add_argument("--url", default=URL)
    args = p.parse_args()
    global URL
    URL = args.url

    print("local_utc                 offset_ms  rtt_ms  http_date_ms", flush=True)
    print("Lighter timestamp is 1s grain (unlike Binance ms). + = Lighter ahead. Ctrl+C to stop.", flush=True)
    while True:
        utc = time.strftime("%Y-%m-%d %H:%M:%S", time.gmtime())
        try:
            off, rtt, date_off = measure(args.timeout)
            d = f"{date_off:+8.0f}" if date_off is not None else "     —"
            print(f"{utc}Z   {off:+8.1f}   {rtt:6.0f}   {d}", flush=True)
        except (urllib.error.URLError, TimeoutError, json.JSONDecodeError, KeyError, OSError) as e:
            print(f"{utc}Z   ERROR {type(e).__name__}: {e}", flush=True)
        time.sleep(max(0.2, args.interval))


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except KeyboardInterrupt:
        print(file=sys.stderr)
        raise SystemExit(0)
