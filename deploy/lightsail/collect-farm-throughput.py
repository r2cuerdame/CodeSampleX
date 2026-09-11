#!/usr/bin/env python3
"""One fixed scalar SELECT; source and helpers arrive through reviewed stdin."""
import hashlib
import json
import re
import sys
import time

import _farm_bounded_transport as transport

SQL_TEMPLATE = globals().get("SQL_TEMPLATE_FROM_TRANSPORT", "")
COMMAND_SECONDS, TOTAL_SECONDS, MAX_BYTES = 3, 12, 8192
MAX_COUNT = 9007199254740991
GEN_COUNTS = ("currentSlotAttributedDrafts", "createdAndUpdatedTimestampEqual", "postcreationTimestampChanged")
FAILURES = ("none", "not_collected", "invalid_expected_revision", "invalid_binding",
            "revision_mismatch", "invalid_identity", "identity_changed", "invalid_counts",
            "invalid_summary", "command_failed", "command_timeout", "byte_limit",
            "window_timeout", "transport_failed", "collector_failed")
FAILURE_STAGES = ("none", "unknown", "identity_before", "sql_read", "identity_after", "validation")


class Unavailable(Exception):
    def __init__(self, reason):
        self.reason = reason if reason in FAILURES else "collector_failed"


def require(ok, reason="invalid_summary"):
    if not ok:
        raise Unavailable(reason)


def keys(value, expected):
    require(type(value) is dict and set(value) == set(expected))


def integer(value, low=0, high=MAX_COUNT):
    require(type(value) is int and low <= value <= high)


def unique(pairs):
    out = {}
    for key, value in pairs:
        require(key not in out)
        out[key] = value
    return out


def decode(raw):
    require(type(raw) is bytes and len(raw) <= MAX_BYTES and raw.endswith(b"\n"))
    return json.loads(raw.decode("utf-8", "strict"), object_pairs_hook=unique,
                      parse_constant=lambda _: require(False))


def binding(peer, slots):
    require(type(peer) is str and re.fullmatch(r"ed25519:[0-9a-f]{16}", peer) is not None, "invalid_binding")
    require(type(slots) in (tuple, list) and len(slots) == 3, "invalid_binding")
    require(all(type(s) is str and re.fullmatch(r"[0-9a-f]{64}", s) for s in slots), "invalid_binding")
    require(len(set(slots)) == 3, "invalid_binding")
    return hashlib.sha256((peer + "\n" + "\n".join(slots) + "\n").encode("ascii")).hexdigest()


def empty(expected=None, peer=None, slots=None, reason="not_collected", stage="unknown"):
    try:
        bound = binding(peer, slots)
    except Exception:
        bound = None
    return {"schemaVersion": 1, "availability": "unavailable", "failureClass": reason,
            "failureStage": stage if stage in FAILURE_STAGES else "unknown",
            "expectedRevision": expected if transport.valid_revision(expected) else None,
            "inputBindingSha256": bound, "windowSeconds": 3600,
            "receiptScope": "committed_receipt_ids_by_stored_server_created_at_for_bound_public_peer",
            "firstPassScope": "sample_first_historical_pass_all_peers_not_network_purl_first_proven",
            "genScope": "current_draft_slot_label_attribution_not_original_writer_history",
            "timestampEqualityScope": "timestamp_equality_not_immutable_history",
            "cleanWindowScope": "server_container_only_requires_separate_farm_health_cohort_evidence",
            "identity": None, "counts": None, "cleanWindowEligible": None}


def throughput_sql(peer, slots):
    binding(peer, slots)
    require(type(SQL_TEMPLATE) is str and 0 < len(SQL_TEMPLATE.encode()) <= 16384, "collector_failed")
    sql = SQL_TEMPLATE
    for token, value in [("__NODE_PEER__", peer)] + [("__SLOT%d_HASH__" % n, v) for n, v in enumerate(slots, 1)]:
        require(sql.count(token) == (2 if token == "__NODE_PEER__" else 1), "collector_failed")
        sql = sql.replace(token, value)
    return sql


def sql_command(sql):
    return ("docker", "compose", "-f", "/opt/codesamplex/deploy/docker-compose.yml", "exec", "-T",
            "-e", "PGOPTIONS=-c default_transaction_read_only=on -c statement_timeout=1000 -c lock_timeout=500 -c jit=off -c standard_conforming_strings=on",
            "-e", "PGCONNECT_TIMEOUT=2", "db", "psql", "-X", "-v", "ON_ERROR_STOP=1", "-U", "csx", "-d", "csx", "-Atqc", sql)


def validate_identity(value, expected, private=False):
    keys(value, ("id", "imageDigest", "revision", "startedAt") if private else ("imageDigest", "revision", "startedAt"))
    if private:
        require(type(value["id"]) is str and re.fullmatch(r"[0-9a-f]{64}", value["id"]) is not None)
    require(type(value["imageDigest"]) is str and re.fullmatch(r"sha256:[0-9a-f]{64}", value["imageDigest"]) is not None)
    require(value["revision"] == expected)
    transport.timestamp_ns(value["startedAt"])
    return value


def validate_counts(value):
    keys(value, ("windowStart", "windowEnd", "receipts", "sampleFirstPass", "gen"))
    start, end = (transport.timestamp_ns(value[k]) for k in ("windowStart", "windowEnd"))
    require(end - start == 3600 * 1000000000)
    receipts = value["receipts"]
    keys(receipts, ("accepted", "pass"))
    integer(receipts["accepted"])
    integer(receipts["pass"], 0, receipts["accepted"])
    first = value["sampleFirstPass"]
    keys(first, ("unambiguousNodeSamples", "sharedFirstTimestampSamples", "unknownHistoricalTimestampSamples"))
    for count in first.values():
        integer(count, 0, receipts["pass"])
    require(sum(first.values()) <= receipts["pass"])
    gen = value["gen"]
    keys(gen, GEN_COUNTS + ("slots",))
    require(type(gen["slots"]) is list and len(gen["slots"]) == 3)
    for n, slot in enumerate(gen["slots"], 1):
        keys(slot, GEN_COUNTS + ("slot",))
        require(type(slot["slot"]) is int and slot["slot"] == n)
        for key in GEN_COUNTS:
            integer(slot[key])
        require(slot[GEN_COUNTS[0]] == slot[GEN_COUNTS[1]] + slot[GEN_COUNTS[2]])
    for key in GEN_COUNTS:
        integer(gen[key])
        require(gen[key] == sum(s[key] for s in gen["slots"]))
    return value


def validate(raw, expected, peer, slots):
    value = decode(raw)
    template = empty(expected, peer, slots)
    keys(value, template)
    for key in ("schemaVersion", "expectedRevision", "inputBindingSha256", "windowSeconds",
                "receiptScope", "firstPassScope", "genScope", "timestampEqualityScope", "cleanWindowScope"):
        require(type(value[key]) is type(template[key]) and value[key] == template[key])
    require(value["availability"] in ("available", "unavailable") and value["failureClass"] in FAILURES)
    require(value["failureStage"] in FAILURE_STAGES)
    if value["availability"] == "unavailable":
        require(value["failureClass"] != "none" and value["failureStage"] != "none")
        if value["failureClass"] in ("not_collected", "transport_failed"):
            require(value["failureStage"] == "unknown")
        require(all(value[k] is None for k in ("identity", "counts", "cleanWindowEligible")))
        return value
    require(value["failureClass"] == "none" and value["failureStage"] == "none" and transport.valid_revision(expected))
    binding(peer, slots)
    validate_identity(value["identity"], expected)
    validate_counts(value["counts"])
    eligible = transport.timestamp_ns(value["identity"]["startedAt"]) <= transport.timestamp_ns(value["counts"]["windowStart"])
    require(type(value["cleanWindowEligible"]) is bool and value["cleanWindowEligible"] == eligible)
    require(transport.timestamp_ns(value["identity"]["startedAt"]) <= transport.timestamp_ns(value["counts"]["windowEnd"]))
    return value


def collect(expected, peer, slots, run=transport.bounded_command, monotonic=time.monotonic):
    result = empty(expected, peer, slots)
    if not transport.valid_revision(expected):
        return empty(expected, peer, slots, "invalid_expected_revision", "validation")
    try:
        binding(peer, slots)
    except Exception:
        return empty(expected, peer, slots, "invalid_binding", "validation")
    deadline = monotonic() + TOTAL_SECONDS

    def command(argv):
        budget = min(COMMAND_SECONDS, deadline - monotonic())
        require(budget > 0, "window_timeout")
        return run(argv, budget, MAX_BYTES)

    def identity():
        value = decode(command(transport.INSPECT))
        require(type(value) is dict and value.get("revision") == expected, "revision_mismatch")
        try:
            return validate_identity(value, expected, private=True)
        except Exception:
            raise Unavailable("invalid_identity") from None

    stage = "validation"
    try:
        sql = throughput_sql(peer, slots)
        stage = "identity_before"
        before = identity()
        stage = "sql_read"
        raw = command(sql_command(sql))
        stage = "validation"
        try:
            counts = validate_counts(decode(raw))
        except Exception:
            raise Unavailable("invalid_counts") from None
        stage = "identity_after"
        after = identity()
        stage = "validation"
        require(before == after, "identity_changed")
        public = {key: value for key, value in before.items() if key != "id"}
        result.update(availability="available", failureClass="none", failureStage="none", identity=public, counts=counts,
                      cleanWindowEligible=transport.timestamp_ns(before["startedAt"]) <= transport.timestamp_ns(counts["windowStart"]))
        return validate((json.dumps(result) + "\n").encode(), expected, peer, slots)
    except (Unavailable, transport.Unavailable) as exc:
        return empty(expected, peer, slots, exc.reason if exc.reason in FAILURES else "collector_failed", stage)
    except Exception:
        return empty(expected, peer, slots, "collector_failed", stage)


if __name__ == "__main__":
    if len(sys.argv) != 6:
        raise SystemExit(1)
    summary = collect(sys.argv[1], sys.argv[2], sys.argv[3:])
    sys.stdout.write(json.dumps(summary, separators=(",", ":"), ensure_ascii=True) + "\n")
