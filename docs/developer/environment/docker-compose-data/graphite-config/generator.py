#!/opt/graphite/bin/python3
#
#  Copyright 2018 The Trickster Authors
#
#  Licensed under the Apache License, Version 2.0 (the "License");
#  you may not use this file except in compliance with the License.
#  You may obtain a copy of the License at
#
#      http://www.apache.org/licenses/LICENSE-2.0
#
#  Unless required by applicable law or agreed to in writing, software
#  distributed under the License is distributed on an "AS IS" BASIS,
#  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
#  See the License for the specific language governing permissions and
#  limitations under the License.
#

"""
generator.py -- deterministic Graphite test data for the Trickster dev env.

Graphite is built around a live stream and will not accept future timestamps,
so the usual "seed once, dashboards stay interesting" approach does not work.
Instead every value here is a pure function of time:

    value = f(metric, floor(t / step))

and that one function is used two ways:

  seed    Backfill [now - window, now] by writing Whisper files directly
          (past timestamps are fine, and it is orders of magnitude faster
          than replaying through carbon), validate the result through
          graphite-web's render API, then exit. Run by the `graphite_seed`
          compose service and by `make developer-seed-data`.

  stream  Catch up any gap since the last run the same way, then emit
          f(now) for every metric to carbon's plaintext port (2003) at each
          step boundary, forever. Run by the `graphite_generator` service.

Because both sides come from the same function there is no seam at the
boundary: a container that has been up for eight months has the same data
shape as one started this morning, and the same query at the same wall-clock
time yields the same values on every machine.

Coarser archives are backfilled with the *aggregate* of the fine-step values
in each bucket, using the file's own aggregation method -- which is exactly
what Whisper's write-time propagation produces for the live stream -- so the
backfilled and streamed regions of a file are indistinguishable.

Environment:
  GRAPHITE_STORAGE_DIR   default /opt/graphite/storage
  GRAPHITE_CONF_DIR      default /opt/graphite/conf
  GRAPHITE_WEB_URL       default http://graphite      (seed validation)
  CARBON_HOST            default graphite             (stream)
  CARBON_PORT            default 2003                 (stream)
  GRAPHITE_SEED_WINDOW   default 6w  (per-namespace overrides in NAMESPACES)
  GRAPHITE_SEED_FORCE    if "1", delete and recreate every generated .wsp
"""

import configparser
import hashlib
import json
import math
import os
import re
import signal
import socket
import sys
import time
import urllib.parse
import urllib.request

import whisper

STORAGE_DIR = os.environ.get("GRAPHITE_STORAGE_DIR", "/opt/graphite/storage")
CONF_DIR = os.environ.get("GRAPHITE_CONF_DIR", "/opt/graphite/conf")
WEB_URL = os.environ.get("GRAPHITE_WEB_URL", "http://graphite").rstrip("/")
CARBON_HOST = os.environ.get("CARBON_HOST", "graphite")
CARBON_PORT = int(os.environ.get("CARBON_PORT", "2003"))
SEED_WINDOW = os.environ.get("GRAPHITE_SEED_WINDOW", "6w")
SEED_FORCE = os.environ.get("GRAPHITE_SEED_FORCE", "") == "1"

WHISPER_DIR = os.path.join(STORAGE_DIR, "whisper")
STATE_PATH = os.path.join(STORAGE_DIR, "trickster-generator-state.json")

# When catching up after a restart, re-write this much overlap so that points
# carbon had in its cache but never flushed are filled in. Idempotent.
CATCHUP_OVERLAP = 10 * 60

# ---------------------------------------------------------------------------
# Namespaces and series
# ---------------------------------------------------------------------------

# The DRIFT case (todo 1.3): dev.drift.* files are created with this ladder,
# while storage-schemas.conf declares 60s:2d,5m:30d,1h:2y for the same
# pattern. Config and on-disk reality disagree on purpose, so that anything
# trusting storage-schemas.conf alone is provably wrong against this env.
DRIFT_RETENTIONS = "30s:12h,5m:14d,1h:1y"

NAMESPACES = {
    # name: dict(window=<override or None>, disk_retentions=<override or None>)
    "fast": dict(window=None, disk_retentions=None),
    "medium": dict(window=None, disk_retentions=None),
    # Fill the coarse ladder's entire retention so the maxRetention clamp is
    # observable as data (a from=-120d query returns exactly 90d of points).
    "coarse": dict(window="91d", disk_retentions=None),
    "drift": dict(window=None, disk_retentions=DRIFT_RETENTIONS),
}

TWO_PI = 2.0 * math.pi
MASK64 = (1 << 64) - 1
SPIKE_SALT = 0x5A17C0DE


def diurnal(t):
    """1.0 +/- 0.5, peaking at 15:00 UTC, lowest at 03:00 UTC."""
    return 1.0 + 0.5 * math.sin(TWO_PI * (t / 86400.0 - 0.375))


def weekly(t):
    """0.65 on Sat/Sun (UTC), 1.0 otherwise. 1970-01-01 was a Thursday."""
    return 0.65 if (t // 86400 + 3) % 7 >= 5 else 1.0


def noise(seed, bucket):
    """Deterministic uniform [0,1) from (seed, bucket); splitmix64 finalizer."""
    x = (seed ^ ((bucket * 0x9E3779B97F4A7C15) & MASK64)) & MASK64
    x = ((x ^ (x >> 30)) * 0xBF58476D1CE4E5B9) & MASK64
    x = ((x ^ (x >> 27)) * 0x94D049BB133111EB) & MASK64
    x ^= x >> 31
    return x / 18446744073709551616.0


class Series:
    __slots__ = ("name", "namespace", "shape", "p", "seed", "path")

    def __init__(self, name, shape, **p):
        self.name = name
        self.namespace = name.split(".")[1]
        self.shape = shape
        self.p = p
        # hash() of a str is salted per process; sha1 is stable everywhere.
        self.seed = int(hashlib.sha1(name.encode()).hexdigest()[:16], 16)
        self.path = os.path.join(WHISPER_DIR, *name.split(".")) + ".wsp"

    def value(self, t, step):
        """f(metric, bucket): t must be bucket-aligned to the finest step."""
        bucket = t // step
        u = noise(self.seed, bucket) * 2.0 - 1.0  # [-1, 1)
        p = self.p
        shape = self.shape
        if shape == "rate":
            # per-bucket count: per_min scaled to the bucket width, so that a
            # sum-rollup of six 10s buckets is the per-minute count.
            v = p["per_min"] * p["scale"] * (step / 60.0) * diurnal(t) * weekly(t)
            v *= 1.0 + p["noise"] * u
            if noise(self.seed ^ SPIKE_SALT, bucket) < p.get("spike", 0.0):
                v *= 2.5
            return round(v, 3)
        if shape == "latency":
            v = (p["base"] + p["amp"] * (diurnal(t) - 1.0) * 2.0) * p["scale"]
            v *= 1.0 + p["noise"] * u
            if noise(self.seed ^ SPIKE_SALT, bucket) < p.get("spike", 0.0):
                v *= 3.0
            return round(v, 3)
        if shape == "gauge":
            v = p["base"] + p["amp"] * (diurnal(t) - 1.0) * 2.0 + p["noise"] * u
            lo, hi = p.get("clamp", (None, None))
            if lo is not None:
                v = max(lo, v)
            if hi is not None:
                v = min(hi, v)
            return round(v, 3)
        if shape == "sawtooth":
            period = p["period"]
            v = p["base"] + p["amp"] * ((t % period) / period) + p["noise"] * u
            return round(max(0.0, v), 3)
        if shape == "growth":
            v = p["base"] + p["slope"] * (t - p["epoch"]) + p["amp"] * (diurnal(t) - 1.0) * 2.0
            return round(v, 0)
        raise ValueError("unknown shape %s" % shape)


def _expand(pattern, shape, members, **p):
    """members: list of (name, scale-or-overrides)."""
    out = []
    for member, over in members:
        kw = dict(p)
        if isinstance(over, dict):
            kw.update(over)
        else:
            kw["scale"] = over
        out.append(Series(pattern.replace("{}", member), shape, **kw))
    return out


SERIES = []
# dev.fast.* -- 10s:6h,60s:7d,10m:5y
SERIES += _expand("dev.fast.requests.{}.count", "rate",
                  [("api", 1.0), ("web", 0.7), ("mobile", 0.45), ("partner", 0.12)],
                  per_min=6000, noise=0.08, spike=0.003)
SERIES += _expand("dev.fast.latency.{}.p99", "latency",
                  [("api", 1.0), ("web", 1.4), ("mobile", 1.8), ("partner", 0.9)],
                  base=120.0, amp=60.0, noise=0.15, spike=0.004)
SERIES += _expand("dev.fast.cpu.{}.percent", "gauge",
                  [("host01", dict(base=35.0)), ("host02", dict(base=48.0)), ("host03", dict(base=22.0))],
                  amp=20.0, noise=4.0, clamp=(0.0, 100.0))
# dev.medium.* -- 60s:2d,5m:30d,1h:2y
SERIES += _expand("dev.medium.orders.{}.count", "rate",
                  [("us-east", 1.0), ("eu-west", 0.6), ("ap-south", 0.35)],
                  per_min=90, noise=0.12, spike=0.002)
SERIES += _expand("dev.medium.revenue.{}.dollars", "rate",
                  [("us-east", 1.0), ("eu-west", 0.6), ("ap-south", 0.35)],
                  per_min=90 * 41.0, noise=0.12, spike=0.002)
SERIES += _expand("dev.medium.queue.{}.depth", "sawtooth",
                  [("orders", dict(period=3600, base=20.0, amp=480.0)),
                   ("emails", dict(period=1800, base=5.0, amp=120.0))],
                  noise=8.0)
# dev.coarse.* -- 5m:90d
SERIES += _expand("dev.coarse.storage.{}.bytes", "growth",
                  [("bucket-a", dict(base=2.0e12, slope=20000.0)),
                   ("bucket-b", dict(base=0.75e12, slope=8000.0))],
                  epoch=1700000000, amp=5.0e8)
SERIES += _expand("dev.coarse.users.{}", "gauge",
                  [("active", dict(base=12000.0))],
                  amp=3500.0, noise=150.0, clamp=(0.0, None))
# dev.drift.* -- DRIFT: on disk 30s:12h,5m:14d,1h:1y; config says otherwise
SERIES += _expand("dev.drift.temperature.{}.celsius", "gauge",
                  [("sensor01", dict(base=21.0)), ("sensor02", dict(base=18.5))],
                  amp=4.0, noise=0.3)

# ---------------------------------------------------------------------------
# Config parsing (storage-schemas.conf / storage-aggregation.conf)
# ---------------------------------------------------------------------------

UNIT_SECONDS = {"s": 1, "m": 60, "min": 60, "h": 3600, "d": 86400, "w": 604800, "y": 31536000}


def parse_duration(s):
    m = re.fullmatch(r"\s*(\d+)\s*([a-zA-Z]*)\s*", s)
    if not m:
        raise ValueError("bad duration %r" % s)
    unit = m.group(2).lower() or "s"
    if unit not in UNIT_SECONDS:
        # whisper also accepts e.g. "hours", "days"; keep it simple
        unit = unit[0]
    return int(m.group(1)) * UNIT_SECONDS[unit]


def load_rules(path, fields):
    """Ordered list of (compiled pattern, {field: value}) -- first match wins."""
    cp = configparser.ConfigParser()
    cp.read(path)
    rules = []
    for section in cp.sections():
        pat = cp.get(section, "pattern")
        vals = {f: cp.get(section, f, fallback=None) for f in fields}
        rules.append((re.compile(pat), vals))
    return rules


def first_match(rules, name):
    for pat, vals in rules:
        if pat.search(name):
            return vals
    return None


def parse_retentions(retentions):
    """'10s:6h,60s:7d' -> [(10, 2160), (60, 10080)] (validated by whisper)."""
    archives = [whisper.parseRetentionDef(r.strip()) for r in retentions.split(",")]
    whisper.validateArchiveList(archives)
    return archives


class Plan:
    """Everything needed to create and fill one series' Whisper file."""
    __slots__ = ("series", "archives", "method", "xff", "window", "retentions")

    def __init__(self, series, archives, method, xff, window, retentions):
        self.series = series
        self.archives = archives          # [(step, points)], finest first
        self.method = method
        self.xff = xff
        self.window = window
        self.retentions = retentions      # the string, for logging

    @property
    def step(self):
        return self.archives[0][0]

    @property
    def max_retention(self):
        return max(s * n for s, n in self.archives)


def build_plans():
    schemas = load_rules(os.path.join(CONF_DIR, "storage-schemas.conf"), ["retentions"])
    aggs = load_rules(os.path.join(CONF_DIR, "storage-aggregation.conf"),
                      ["aggregationMethod", "xFilesFactor"])
    default_window = parse_duration(SEED_WINDOW)
    plans = []
    for s in SERIES:
        ns = NAMESPACES[s.namespace]
        retentions = ns["disk_retentions"]
        if retentions is None:
            m = first_match(schemas, s.name)
            if m is None:
                raise SystemExit("no storage-schemas.conf match for %s" % s.name)
            retentions = m["retentions"]
        archives = parse_retentions(retentions)
        a = first_match(aggs, s.name) or {}
        method = a.get("aggregationMethod") or "average"
        xff = float(a.get("xFilesFactor") or 0.5)
        window = parse_duration(ns["window"]) if ns["window"] else default_window
        max_ret = max(st * n for st, n in archives)
        window = min(window, max_ret - archives[0][0])
        plans.append(Plan(s, archives, method, xff, window, retentions))
    return plans


# ---------------------------------------------------------------------------
# Whisper I/O
# ---------------------------------------------------------------------------

def aggregate(method, values):
    """Mirror whisper.aggregate for the methods used here."""
    if method == "average":
        return float(sum(values)) / float(len(values))
    if method == "sum":
        return float(sum(values))
    if method == "last":
        return values[-1]
    if method == "max":
        return max(values)
    if method == "min":
        return min(values)
    return whisper.aggregate(method, values)


def ensure_file(plan, force):
    """Create the .wsp if missing (or if force). Returns True if created."""
    path = plan.series.path
    if os.path.exists(path):
        if not force:
            info = whisper.info(path)
            on_disk = [(a["secondsPerPoint"], a["points"]) for a in info["archives"]]
            if on_disk != plan.archives:
                log("WARNING %s on-disk archives %s != intended %s; "
                    "run `make developer-seed-data` to recreate", plan.series.name,
                    on_disk, plan.archives)
            return False
        os.remove(path)
    os.makedirs(os.path.dirname(path), exist_ok=True)
    whisper.create(path, plan.archives, xFilesFactor=plan.xff, aggregationMethod=plan.method)
    return True


def backfill(plan, start, now):
    """Write f over [start, now] directly into the series' Whisper archives.

    For each archive, generate points at that archive's step covering exactly
    the ages whisper will route to it, so nothing is written twice. Coarser
    archives get the aggregate of the fine-step values in each bucket, which
    is what whisper's write-time propagation yields for streamed data.
    """
    s = plan.series
    step0 = plan.step
    now_bucket = now - (now % step0)
    points = []
    prev_ret = 0
    for step, n in plan.archives:
        ret = step * n
        # ages (prev_ret, ret]  <=>  now - ret <= T < now - prev_ret
        lo = max(now - ret, start)
        t_start = lo + ((step - lo % step) % step)
        t_end = now_bucket + step0 if prev_ret == 0 else now - prev_ret
        if step == step0:
            for t in range(t_start, t_end, step):
                points.append((t, s.value(t, step0)))
        else:
            sub = step // step0
            for t in range(t_start, t_end, step):
                vals = [s.value(t + k * step0, step0) for k in range(sub)]
                points.append((t, aggregate(plan.method, vals)))
        prev_ret = ret
        if now - ret <= start:
            break
    if points:
        whisper.update_many(s.path, points, now=now)
    return len(points), now_bucket


# ---------------------------------------------------------------------------
# State
# ---------------------------------------------------------------------------

def load_state():
    try:
        with open(STATE_PATH) as fh:
            return json.load(fh)
    except (OSError, ValueError):
        return {"version": 1, "last_bucket": {}}


def save_state(state):
    tmp = STATE_PATH + ".tmp"
    with open(tmp, "w") as fh:
        json.dump(state, fh)
    os.replace(tmp, STATE_PATH)


def catch_up(plans, state, now, force):
    """Create/backfill every series so that every bucket <= now exists."""
    t0 = time.time()
    total = 0
    for plan in plans:
        created = ensure_file(plan, force)
        last = state["last_bucket"].get(plan.series.name)
        if created or last is None:
            start = now - plan.window
            how = "full"
        else:
            start = max(last - CATCHUP_OVERLAP, now - plan.window)
            how = "catch-up"
        if start > now:
            continue
        n, last_bucket = backfill(plan, start, now)
        state["last_bucket"][plan.series.name] = last_bucket
        total += n
        log("%-45s %-8s %7d points  [%s] step=%ds agg=%s xff=%s", plan.series.name,
            how, n, plan.retentions, plan.step, plan.method, plan.xff)
    save_state(state)
    log("wrote %d points in %.1fs", total, time.time() - t0)


# ---------------------------------------------------------------------------
# Validation (seed only) -- through graphite-web, the way a client sees it
# ---------------------------------------------------------------------------

def http_get(path, tries=5):
    # transient failures (truncated reads, resets, slow workers) retry with
    # backoff so one bad response fails a check, not the whole seed
    last = None
    for i in range(tries):
        try:
            req = urllib.request.Request(WEB_URL + path)
            with urllib.request.urlopen(req, timeout=30) as resp:
                return resp.read().decode()
        except Exception as e:
            last = e
            log("retry %d/%d GET %s: %s", i + 1, tries, path, e)
            time.sleep(i + 1)
    raise last


def render_raw(target, frm, until="now"):
    """Returns (start, end, step, [values]) for a single-series raw render."""
    q = urllib.parse.urlencode({"target": target, "from": frm, "until": until, "format": "raw"})
    body = http_get("/render?" + q).strip()
    if not body:
        return None
    line = body.splitlines()[0]
    head, _, data = line.partition("|")
    parts = head.rsplit(",", 3)
    start, end, step = int(parts[1]), int(parts[2]), int(parts[3])
    vals = [None if v == "None" else float(v) for v in data.split(",")] if data else []
    return start, end, step, vals


def validate(plans, now):
    failures = []

    def check(cond, msg, *args):
        if not cond:
            failures.append(msg % args)
            log("FAIL " + msg, *args)
        else:
            log("ok   " + msg, *args)

    # one representative series per namespace
    seen = set()
    for plan in plans:
        ns = plan.series.namespace
        if ns in seen:
            continue
        seen.add(ns)
        name = plan.series.name
        prev_ret = 0
        for step, n in plan.archives:
            ret = step * n
            # probe age: just inside this rung, but inside the seeded window
            age = prev_ret + min(86400, (ret - prev_ret) // 2)
            prev_ret = ret
            if age > plan.window - 3600:
                log("skip %s from=-%ds (beyond seeded window %ds)", name, age, plan.window)
                continue
            # absolute from/until anchor the window to the seeded timestamp,
            # so a slow backfill cannot shift the newest buckets out of it
            r = render_raw(name, now - age, now)
            check(r is not None, "%s from=-%ds returned a series", name, age)
            if r is None:
                continue
            start, end, got, vals = r
            check(got == step, "%s from=-%ds step=%d (expected %d)", name, age, got, step)
            nn = sum(1 for v in vals if v is not None)
            expect = len(vals) - 2  # the newest bucket or two may be in flight
            check(nn >= expect and len(vals) > 0,
                  "%s from=-%ds %d/%d non-null points", name, age, nn, len(vals))
            check(end - start <= age + 2 * step,
                  "%s from=-%ds range %ds covers the request", name, age, end - start)

    # DRIFT: the on-disk step must differ from what storage-schemas.conf says
    drift = next(p for p in plans if p.series.namespace == "drift")
    schemas = load_rules(os.path.join(CONF_DIR, "storage-schemas.conf"), ["retentions"])
    cfg = parse_retentions(first_match(schemas, drift.series.name)["retentions"])
    r = render_raw(drift.series.name, now - 3600, now)
    check(r is not None and r[2] == drift.step and r[2] != cfg[0][0],
          "drift: %s observed step %s == disk %d != config %d",
          drift.series.name, r and r[2], drift.step, cfg[0][0])

    # maxRetention clamp on the coarse ladder
    coarse = next(p for p in plans if p.series.namespace == "coarse")
    # a narrow until keeps the response to a few points; the clamp is
    # observable from where the returned series starts
    r = render_raw(coarse.series.name,
                   now - (coarse.max_retention + 30 * 86400),
                   now - coarse.max_retention + 3600)
    if r is not None:
        start, end, step, vals = r
        check(abs((now - start) - coarse.max_retention) <= 2 * step,
              "clamp: %s from beyond maxRetention starts %ds ago (maxRetention %ds)",
              coarse.series.name, now - start, coarse.max_retention)
    else:
        check(False, "clamp: %s beyond-retention query returned nothing", coarse.series.name)

    # wildcard expansion sees every namespace
    found = json.loads(http_get("/metrics/find?query=dev.*"))
    names = sorted(n["text"] for n in found)
    check(names == sorted(NAMESPACES), "metrics/find dev.* -> %s", names)

    return failures


# ---------------------------------------------------------------------------
# Streaming
# ---------------------------------------------------------------------------

class Carbon:
    def __init__(self, host, port):
        self.host, self.port = host, port
        self.sock = None

    def send(self, lines):
        data = "".join(lines).encode()
        for attempt in range(5):
            try:
                if self.sock is None:
                    self.sock = socket.create_connection((self.host, self.port), timeout=10)
                self.sock.sendall(data)
                return
            except OSError as e:
                log("carbon send failed (%s); reconnecting", e)
                self.close()
                time.sleep(1 + attempt)
        raise RuntimeError("could not send to carbon at %s:%d" % (self.host, self.port))

    def close(self):
        if self.sock is not None:
            try:
                self.sock.close()
            except OSError:
                pass
            self.sock = None


STOP = False


def _on_signal(signum, _frame):
    global STOP
    STOP = True
    log("signal %d received; stopping", signum)


def stream(plans, state):
    carbon = Carbon(CARBON_HOST, CARBON_PORT)
    sent = 0
    last_report = time.time()
    while not STOP:
        now = int(time.time())
        lines = []
        wake = now + 10
        for plan in plans:
            step = plan.step
            name = plan.series.name
            t = state["last_bucket"][name] + step
            while t <= now:
                lines.append("%s %s %d\n" % (name, plan.series.value(t, step), t))
                state["last_bucket"][name] = t
                t += step
            wake = min(wake, t)
        if lines:
            carbon.send(lines)
            save_state(state)
            sent += len(lines)
        if time.time() - last_report >= 60:
            log("streaming: %d points sent in the last minute", sent)
            sent, last_report = 0, time.time()
        delay = wake - time.time()
        if delay > 0:
            time.sleep(min(delay, 10.0))
    carbon.close()
    save_state(state)


# ---------------------------------------------------------------------------

def log(fmt, *args):
    sys.stdout.write(time.strftime("%Y-%m-%dT%H:%M:%SZ ", time.gmtime()) + (fmt % args) + "\n")
    sys.stdout.flush()


def main(argv):
    mode = argv[1] if len(argv) > 1 else "stream"
    if mode not in ("seed", "stream"):
        raise SystemExit("usage: generator.py seed|stream")
    plans = build_plans()
    state = load_state()
    now = int(time.time())
    log("%s: %d series across %d namespaces, whisper dir %s", mode, len(plans),
        len(NAMESPACES), WHISPER_DIR)
    if mode == "seed":
        catch_up(plans, state, now, force=SEED_FORCE)
        failures = validate(plans, now)
        if failures:
            log("seed validation FAILED (%d):", len(failures))
            for f in failures:
                log("  - %s", f)
            return 1
        log("seed validation passed")
        return 0
    signal.signal(signal.SIGTERM, _on_signal)
    signal.signal(signal.SIGINT, _on_signal)
    catch_up(plans, state, now, force=False)
    log("streaming to %s:%d", CARBON_HOST, CARBON_PORT)
    stream(plans, state)
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
