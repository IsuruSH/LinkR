#!/usr/bin/env python3
"""
Concurrent load test for Linkr's redirect hot path.

Fires N requests at a chosen concurrency against GET /{code} and reports
throughput, latency percentiles, and the HTTP status breakdown. Redirects are
NOT followed (allow_redirects=False), so you measure Linkr's 302 latency, not a
round-trip to the destination site.

Why the redirect: it's public (no JWT), it's the documented hot path (Redis +
async click write), and 10k hits exercise the bounded click buffer — the whole
point of the async design is that the 302 stays fast even if some clicks are shed.

Usage:
    pip install aiohttp
    python loadtest.py --url https://parfumworld.store --path /gh-repo \
        --requests 10000 --concurrency 500

--------------------------------------------------------------------------------
READ THIS BEFORE BLAMING THE SERVER
--------------------------------------------------------------------------------
A load test measures three things at once — the network, TLS, and the app — and
the first two often dominate. Two failure modes are the CLIENT, not Linkr:

  * `ClientConnectorError` in bulk, with a ~3ms latency, means your machine
    couldn't open a socket. Back-to-back runs leave thousands of sockets in
    TIME_WAIT, and Windows only has ~16k ephemeral ports (49152-65535). The
    second run exhausts them. Check with:  netstat -an | find /c "TIME_WAIT"
    Fix: wait ~2 min between runs, lower --concurrency, or (best) run from Linux.

  * A high `min` latency (~200ms) is the network floor — the round trip to the
    region (eu-north-1 / Stockholm). No server tuning goes below the speed of
    light. To measure the SERVER, run this from a VM in the same region, or on
    the box itself against http://localhost/<code>.

Windows is a poor high-concurrency load client (small port pool, no tw_reuse).
For a real number, run from a Linux VM in the target region:
    ulimit -n 65535
    python loadtest.py --url http://localhost --path /<code> --concurrency 1000

This script reuses keep-alive connections (so N requests ride ~concurrency
sockets, not N) and retries transient connect errors with backoff, which softens
— but cannot eliminate — client-side socket exhaustion.

Notes:
  * --path must be a REAL short code that exists (create one, or use a seeded
    demo link). A missing code returns 404/303 and still load-tests routing.
  * Latency percentiles cover requests that got an HTTP response. Transport
    errors are counted separately so instant local failures don't skew them.
  * Only run this against a system you own.
"""

import argparse
import asyncio
import random
import ssl
import time
from collections import Counter

try:
    import aiohttp
except ImportError:
    raise SystemExit("aiohttp is required:  pip install aiohttp")

# Errors that are worth a retry: a momentarily exhausted port pool, a connection
# the server closed on us, or a timeout. A 4xx/5xx is a real response, not one of
# these — we never "retry" an HTTP status.
TRANSIENT = (
    aiohttp.ClientConnectorError,
    aiohttp.ClientOSError,
    aiohttp.ServerDisconnectedError,
    asyncio.TimeoutError,
)


def percentile(sorted_vals, pct):
    """Nearest-rank percentile over an already-sorted list (ms)."""
    if not sorted_vals:
        return 0.0
    k = max(0, min(len(sorted_vals) - 1, int(round(pct / 100.0 * len(sorted_vals) + 0.5)) - 1))
    return sorted_vals[k]


class Stats:
    def __init__(self):
        self.latencies = []      # ms, only for requests that got an HTTP response
        self.statuses = Counter()
        self.errors = Counter()
        self.retries = 0


async def one_request(session, url, sem, stats, retries, backoff):
    """Send one request, retrying transient CONNECT errors with backoff+jitter."""
    async with sem:
        start = time.perf_counter()
        for attempt in range(retries + 1):
            try:
                async with session.get(url, allow_redirects=False) as resp:
                    await resp.read()  # drain the tiny body so the conn is reusable
                    stats.statuses[resp.status] += 1
                    stats.latencies.append((time.perf_counter() - start) * 1000.0)
                    return
            except TRANSIENT as exc:
                if attempt < retries:
                    stats.retries += 1
                    # Exponential backoff with jitter: spreads a reconnect storm
                    # instead of everyone retrying on the same tick.
                    await asyncio.sleep(backoff * (2 ** attempt) + random.uniform(0, backoff))
                    continue
                stats.errors[type(exc).__name__] += 1
                return
            except Exception as exc:  # noqa: BLE001 - bucket anything else, don't crash the run
                stats.errors[type(exc).__name__] += 1
                return


async def warmup(session, url, sem, n, retries, backoff):
    """Prime the connection pool / DNS / TLS so the measured run isn't paying for
    a cold first wave of handshakes. Results are discarded."""
    if n <= 0:
        return
    print(f"Warmup : {n} requests (not measured)...")
    throwaway = Stats()
    await asyncio.gather(*[
        one_request(session, url, sem, throwaway, retries, backoff) for _ in range(n)
    ])


async def run(args):
    url = args.url.rstrip("/") + args.path
    sem = asyncio.Semaphore(args.concurrency)
    stats = Stats()

    # Reuse keep-alive connections aggressively: cap the pool at the concurrency
    # so N requests ride ~concurrency sockets, not N. enable_cleanup_closed works
    # around servers that don't close TLS connections cleanly.
    connector = aiohttp.TCPConnector(
        limit=args.concurrency,
        limit_per_host=args.concurrency,
        ttl_dns_cache=300,
        keepalive_timeout=30,
        enable_cleanup_closed=True,
        ssl=ssl.create_default_context() if url.startswith("https") else False,
    )
    timeout = aiohttp.ClientTimeout(total=args.timeout)
    headers = {"Accept": "text/html", "User-Agent": "linkr-loadtest/1.0"}

    print(f"Target : {url}")
    print(f"Plan   : {args.requests} requests, concurrency {args.concurrency}, "
          f"timeout {args.timeout}s, retries {args.retries}, redirects NOT followed")

    async with aiohttp.ClientSession(connector=connector, timeout=timeout, headers=headers) as session:
        await warmup(session, url, sem, args.warmup, args.retries, args.backoff)
        print("Running...\n")
        wall_start = time.perf_counter()
        await asyncio.gather(*[
            one_request(session, url, sem, stats, args.retries, args.backoff)
            for _ in range(args.requests)
        ])
        wall = time.perf_counter() - wall_start

    report(args, wall, stats)


def report(args, wall, stats):
    responded = sum(stats.statuses.values())
    failed = sum(stats.errors.values())
    lat = sorted(stats.latencies)

    print("=" * 54)
    print(f"Responses        : {responded}   ({failed} transport errors, {stats.retries} retries)")
    print(f"Wall time        : {wall:.2f} s")
    print(f"Throughput       : {args.requests / wall:,.0f} req/s")
    print()
    print("Status codes:")
    for code, n in sorted(stats.statuses.items()):
        label = {302: "redirect (hot path OK)", 303: "expired/not-found bounce",
                 404: "not found", 410: "gone", 429: "rate limited"}.get(code, "")
        print(f"  {code}  {n:>7}   {label}")
    if stats.errors:
        print("Transport errors (CLIENT-side unless the server is down):")
        for name, n in stats.errors.most_common():
            print(f"  {name:<24} {n:>7}")
    print()
    if lat:
        print("Latency of responded requests (ms):")
        print(f"  min   {lat[0]:8.1f}")
        print(f"  p50   {percentile(lat, 50):8.1f}")
        print(f"  p90   {percentile(lat, 90):8.1f}")
        print(f"  p95   {percentile(lat, 95):8.1f}")
        print(f"  p99   {percentile(lat, 99):8.1f}")
        print(f"  max   {lat[-1]:8.1f}")
    print("=" * 54)

    # A nudge toward the right diagnosis when the client, not the server, buckled.
    if stats.errors.get("ClientConnectorError", 0) > responded:
        print("\nHINT: most failures were ClientConnectorError — that's your machine "
              "\n      running out of sockets (TIME_WAIT), not the server. Wait ~2 min, "
              "\n      lower --concurrency, or run from Linux. See the header comment.")


def parse_args():
    p = argparse.ArgumentParser(description="Concurrent load test for Linkr's redirect.")
    p.add_argument("--url", default="https://parfumworld.store", help="Base URL of the deployment")
    p.add_argument("--path", default="/gh-repo", help="Path to hit — a real short code, e.g. /gh-repo")
    p.add_argument("--requests", type=int, default=10000, help="Total requests to send")
    p.add_argument("--concurrency", type=int, default=500, help="Max in-flight requests")
    p.add_argument("--warmup", type=int, default=100, help="Unmeasured priming requests (0 to skip)")
    p.add_argument("--retries", type=int, default=2, help="Retries on transient connect errors")
    p.add_argument("--backoff", type=float, default=0.05, help="Base backoff between retries (s)")
    p.add_argument("--timeout", type=float, default=30.0, help="Per-request timeout (s)")
    return p.parse_args()


if __name__ == "__main__":
    asyncio.run(run(parse_args()))
