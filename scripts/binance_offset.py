#!/usr/bin/env python3
"""Live Binance clock offset. Read-only: does not change system time.

offset_ms = Binance serverTime − midpoint(local t0, t1)
Positive = Binance ahead of this machine.
"""
from __future__ import annotations

import argparse
import json
import sys
import time
import urllib.error
import urllib.request

URL = "https://api.binance.com/api/v3/time"


def measure(timeout: float) -> tuple[float, float]:
    t0 = time.time()
    req = urllib.request.Request(URL, headers={"User-Agent": "poly-lighter-clock/1"})
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        raw = resp.read()
    t1 = time.time()
    srv = json.loads(raw)["serverTime"] / 1000.0
    mid = t0 + (t1 - t0) / 2
    return (srv - mid) * 1000.0, (t1 - t0) * 1000.0


def main() -> int:
    p = argparse.ArgumentParser(description="Print Binance vs local clock every N seconds")
    p.add_argument("-n", "--interval", type=float, default=10.0, help="seconds between samples (default 10)")
    p.add_argument("--timeout", type=float, default=5.0)
    p.add_argument("--url", default=URL)
    args = p.parse_args()
    global URL
    URL = args.url

    print("local_utc                 offset_ms  rtt_ms", flush=True)
    print("Binance ahead of host if offset_ms > 0. Ctrl+C to stop.", flush=True)
    while True:
        utc = time.strftime("%Y-%m-%d %H:%M:%S", time.gmtime())
        try:
            off, rtt = measure(args.timeout)
            print(f"{utc}Z   {off:+8.1f}   {rtt:6.0f}", flush=True)
        except (urllib.error.URLError, TimeoutError, json.JSONDecodeError, KeyError, OSError) as e:
            print(f"{utc}Z   ERROR {type(e).__name__}: {e}", flush=True)
        time.sleep(max(0.2, args.interval))


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except KeyboardInterrupt:
        print(file=sys.stderr)
        raise SystemExit(0)
