#!/usr/bin/env python3
"""Read only the current container's bounded retained logs; never emit raw text."""
import calendar
from datetime import datetime, timezone
import json
import queue
import re
import subprocess
import threading
import time

WINDOW_SECONDS = 3600
MAX_LINES = 10000
MAX_BYTES = 4 * 1024 * 1024
MAX_LINE_BYTES = 8192
LOG_TIMEOUT = 10
MAX_AGE_NS = 30 * 86400 * 1000000000
KINDS = ("NO_WORK", "WANTED", "FINDING", "EXPANSION", "DEPENDENCY")
ERROR_CLASSES = ("statement_timeout", "pool_busy")
FAILURES = ("none", "command_failed", "command_timeout", "byte_limit", "line_limit",
            "tail_limit", "invalid_utf8", "invalid_log", "malformed_poll", "unknown_fallback",
            "invalid_identity", "identity_changed", "collector_failed", "transport_failed",
            "invalid_summary", "not_collected")
STAMP = re.compile(r"([0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2})(?:\.([0-9]{1,9}))?Z")
POLL_PREFIX = "csx-server: authoring poll "
FALLBACK_PREFIX = "csx-server: authoring expansion candidates unavailable ("
FALLBACK_SUFFIX = "); serving WANTED-only work this snapshot"
POLL = re.compile(r"session=[A-Za-z0-9_-]{16} wanted=([0-9]{1,3})/([0-9]{1,3}) "
                  r"expansion=([0-9]{1,3})/([0-9]{1,3}) served=(NO_WORK|WANTED|FINDING|EXPANSION|DEPENDENCY) "
                  r"snapshotAge=([^\s]{1,64})")
INSPECT = ("docker", "inspect", "--format",
           '{"id":{{json .Id}},"imageDigest":{{json .Image}},"startedAt":{{json .State.StartedAt}},'
           '"revision":{{json (index .Config.Labels "org.opencontainers.image.revision")}}}',
           "codesamplex-server-1")


class Unavailable(Exception):
    def __init__(self, reason):
        self.reason = reason if reason in FAILURES else "collector_failed"


def timestamp_ns(value):
    if not isinstance(value, str):
        raise Unavailable("invalid_log")
    match = STAMP.fullmatch(value)
    if match is None:
        raise Unavailable("invalid_log")
    try:
        base = datetime.strptime(match[1], "%Y-%m-%dT%H:%M:%S")
        return calendar.timegm(base.timetuple()) * 1000000000 + int((match[2] or "").ljust(9, "0"))
    except (ValueError, OverflowError):
        raise Unavailable("invalid_log") from None


def stamp(ns):
    return datetime.fromtimestamp(ns // 1000000000, timezone.utc).strftime("%Y-%m-%dT%H:%M:%S") + ".%09dZ" % (ns % 1000000000)


def duration_ns(value):
    # Go durations use ordered h/m/s components or a sub-second unit. Bound
    # the numeric domain before conversion; never copy the source string.
    if not isinstance(value, str) or len(value) > 64:
        raise Unavailable("malformed_poll")
    match = re.fullmatch(r"(-?)(?:(?:(\d{1,6})h)?(?:(\d{1,6})m)?(\d{1,6})(?:\.(\d{1,9}))?s|(\d{1,9})(?:\.(\d{1,9}))?(ms|us|µs|μs|ns))", value)
    if match is None:
        raise Unavailable("malformed_poll")
    if match[4] is not None:
        ns = (int(match[2] or 0) * 3600 + int(match[3] or 0) * 60 + int(match[4])) * 1000000000
        ns += int((match[5] or "").ljust(9, "0"))
        if (match[2] and int(match[3] or 0) >= 60) or ((match[2] or match[3]) and int(match[4]) >= 60):
            raise Unavailable("malformed_poll")
    else:
        scale = {"ms": 1000000, "us": 1000, "µs": 1000, "μs": 1000, "ns": 1}[match[8]]
        fraction = match[7] or ""
        numerator = int(match[6]) * (10 ** len(fraction)) + int(fraction or 0)
        ns, remainder = divmod(numerator * scale, 10 ** len(fraction))
        if remainder:
            raise Unavailable("malformed_poll")
    if ns > MAX_AGE_NS:
        raise Unavailable("malformed_poll")
    return -ns if match[1] else ns


def bounded_command(argv, timeout, byte_limit, source=None, merge_stderr=False):
    """Bound memory while reading, not after communicate() has accumulated it."""
    process = subprocess.Popen(argv, stdin=subprocess.PIPE if source is not None else subprocess.DEVNULL,
                               stdout=subprocess.PIPE, stderr=subprocess.STDOUT if merge_stderr else subprocess.DEVNULL)
    chunks = queue.Queue(maxsize=2)
    stopped = threading.Event()

    def read():
        try:
            while not stopped.is_set():
                data = process.stdout.read(4096)
                while not stopped.is_set():
                    try:
                        chunks.put(data, timeout=0.05)
                        break
                    except queue.Full:
                        pass
                if not data:
                    return
        except (OSError, ValueError):
            pass

    reader = threading.Thread(target=read, daemon=True)
    reader.start()
    deadline = time.monotonic() + timeout
    output = bytearray()
    try:
        if source is not None:
            # The reviewed collector is small. A writer thread also keeps a
            # stalled SSH stdin inside the same wall-clock deadline.
            def write():
                try:
                    process.stdin.write(source)
                    process.stdin.close()
                except (OSError, ValueError):
                    pass
            threading.Thread(target=write, daemon=True).start()
        while True:
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise Unavailable("command_timeout")
            try:
                data = chunks.get(timeout=remaining)
            except queue.Empty:
                raise Unavailable("command_timeout") from None
            if not data:
                break
            if len(output) + len(data) > byte_limit:
                raise Unavailable("byte_limit")
            output.extend(data)
        try:
            code = process.wait(timeout=max(0.001, deadline - time.monotonic()))
        except subprocess.TimeoutExpired:
            raise Unavailable("command_timeout") from None
        if code != 0:
            raise Unavailable("command_failed")
        return bytes(output)
    finally:
        stopped.set()
        if process.poll() is None:
            process.kill()
        process.wait(timeout=2)
        reader.join(timeout=0.2)
        process.stdout.close()


def empty_summary(end_ns, reason="not_collected"):
    return {"schemaVersion": 1, "availability": "unavailable", "failureClass": reason,
            "scope": "retained_current_container_logs", "windowSeconds": WINDOW_SECONDS,
            "windowCoverage": "retention_not_proven",
            "windowStart": stamp(end_ns - WINDOW_SECONDS * 1000000000), "windowEnd": stamp(end_ns),
            "identity": None, "read": None, "funnel": None, "fallback": None,
            "funnelScope": "logged_successful_poll_snapshot_counts_not_distinct_work",
            "eligibilityScope": "before_completeness_dependency_maven_filters",
            "snapshotAgeScope": "source_rounded_to_seconds",
            "fallbackScope": "logged_snapshot_read_failures_not_affected_poll_count"}


def parse_logs(raw, start_ns, end_ns):
    if len(raw) > MAX_BYTES:
        raise Unavailable("byte_limit")
    if raw and not raw.endswith(b"\n"):
        raise Unavailable("invalid_log")
    lines = raw.splitlines(keepends=True)
    if len(lines) > MAX_LINES:
        raise Unavailable("tail_limit")
    funnel = {"polls": 0, "wantedReadSum": 0, "wantedEligibleSum": 0,
              "expansionReadSum": 0, "expansionEligibleSum": 0,
              "snapshotAgeNsMin": None, "snapshotAgeNsMax": None,
              "servedCounts": dict.fromkeys(KINDS, 0), "lastPoll": None}
    fallback = {"events": 0, "byClass": dict.fromkeys(ERROR_CLASSES, 0)}
    first_ns = last_ns = None
    for raw_line in lines:
        if len(raw_line) > MAX_LINE_BYTES:
            raise Unavailable("line_limit")
        try:
            line = raw_line[:-1].decode("utf-8", errors="strict")
        except UnicodeDecodeError:
            raise Unavailable("invalid_utf8") from None
        timestamp, separator, message = line.partition(" ")
        event_ns = timestamp_ns(timestamp)
        if not separator or not start_ns <= event_ns <= end_ns or "\r" in line or "\x00" in line:
            raise Unavailable("invalid_log")
        first_ns = event_ns if first_ns is None else min(first_ns, event_ns)
        last_ns = event_ns if last_ns is None else max(last_ns, event_ns)
        # Docker timestamps wrap Go's standard logger timestamp.
        message = re.sub(r"^[0-9]{4}/[0-9]{2}/[0-9]{2} [0-9]{2}:[0-9]{2}:[0-9]{2} ", "", message, count=1)
        if message.startswith(POLL_PREFIX):
            match = POLL.fullmatch(message[len(POLL_PREFIX):])
            if match is None:
                raise Unavailable("malformed_poll")
            wanted, wanted_eligible, expansion, expansion_eligible = map(int, match.group(1, 2, 3, 4))
            if not 0 <= wanted_eligible <= wanted <= 200 or not 0 <= expansion_eligible <= expansion <= 400:
                raise Unavailable("malformed_poll")
            age = duration_ns(match[6])
            funnel["polls"] += 1
            for key, value in (("wantedReadSum", wanted), ("wantedEligibleSum", wanted_eligible),
                               ("expansionReadSum", expansion), ("expansionEligibleSum", expansion_eligible)):
                funnel[key] += value
            funnel["servedCounts"][match[5]] += 1
            for key, fn in (("snapshotAgeNsMin", min), ("snapshotAgeNsMax", max)):
                funnel[key] = age if funnel[key] is None else fn(funnel[key], age)
            if funnel["lastPoll"] is None or event_ns >= timestamp_ns(funnel["lastPoll"]["at"]):
                funnel["lastPoll"] = {"at": stamp(event_ns), "wantedRead": wanted, "wantedEligible": wanted_eligible,
                                      "expansionRead": expansion, "expansionEligible": expansion_eligible,
                                      "served": match[5], "snapshotAgeNs": age}
        elif message.startswith(FALLBACK_PREFIX):
            if not message.endswith(FALLBACK_SUFFIX):
                raise Unavailable("unknown_fallback")
            error = message[len(FALLBACK_PREFIX):-len(FALLBACK_SUFFIX)]
            if error == "ERROR: canceling statement due to statement timeout (SQLSTATE 57014)":
                kind = "statement_timeout"
            elif error == "serverstore: no database connection available within the wait budget":
                kind = "pool_busy"
            else:
                pool = re.fullmatch(r"serverstore: no database connection available within the wait budget "
                                    r"\(class (background|interactive|probe), waited ([^\s]{1,64})\)", error)
                if pool is None or duration_ns(pool[2]) < 0:
                    raise Unavailable("unknown_fallback")
                kind = "pool_busy"
            fallback["events"] += 1
            fallback["byClass"][kind] += 1
    read = {"lines": len(lines), "bytes": len(raw), "firstAt": None if first_ns is None else stamp(first_ns),
            "lastAt": None if last_ns is None else stamp(last_ns)}
    return read, funnel, fallback


def inspect_identity(run):
    try:
        value = json.loads(run(INSPECT, 3, 4096))
        if set(value) != {"id", "imageDigest", "startedAt", "revision"} or not all(isinstance(v, str) for v in value.values()):
            raise ValueError()
        if not re.fullmatch(r"[0-9a-f]{64}", value["id"]) or not re.fullmatch(r"sha256:[0-9a-f]{64}", value["imageDigest"]) or not re.fullmatch(r"[0-9a-f]{40}", value["revision"]):
            raise ValueError()
        timestamp_ns(value["startedAt"])
        return value
    except (ValueError, TypeError, KeyError):
        raise Unavailable("invalid_identity") from None


def collect(run=bounded_command, now=time.time_ns):
    end_ns = now()
    result = empty_summary(end_ns)
    try:
        before = inspect_identity(run)
        argv = ("docker", "logs", "--timestamps", "--since", result["windowStart"], "--until", result["windowEnd"],
                "--tail", str(MAX_LINES + 1), "codesamplex-server-1")
        raw = run(argv, LOG_TIMEOUT, MAX_BYTES, merge_stderr=True)
        read, funnel, fallback = parse_logs(raw, timestamp_ns(result["windowStart"]), end_ns)
        after = inspect_identity(run)
        if before != after:
            raise Unavailable("identity_changed")
        result.update(availability="available", failureClass="none", read=read, funnel=funnel, fallback=fallback,
                      identity={k: before[k] for k in ("imageDigest", "revision", "startedAt")})
    except Unavailable as failure:
        result["failureClass"] = failure.reason
    except Exception:
        result["failureClass"] = "collector_failed"
    return result


if __name__ == "__main__":
    # Only this constructed object reaches SSH stdout; no exception text does.
    print(json.dumps(collect(), separators=(",", ":"), ensure_ascii=True))
