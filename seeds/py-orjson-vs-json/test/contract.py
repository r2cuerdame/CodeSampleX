import datetime as dt
import json
import os
import sys
import uuid
from decimal import Decimal
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

import orjson

from src.serialize import dumps_text, has_non_finite_float
from src.wheel import (
    elf_needed_libraries,
    extension_module_path,
    installed_wheel,
    interpreter_libc,
    musl_loader_report,
    wheel_filename,
)


def raises(expected, call):
    """Run call, require `expected`, and hand the exception back for checking."""
    try:
        call()
    except expected as caught:
        return caught
    raise AssertionError(f"expected {expected.__name__}, nothing was raised")


# --- packaging: which wheel is installed on this image --------------------

info = installed_wheel()

# pip took the musllinux wheel: one tag, matching this interpreter exactly.
# A manylinux tag cannot match on Alpine, and no match at all means pip
# falls back to the sdist, which on python:3.12-alpine downloads a Rust
# toolchain of its own and then fails anyway — src/wheel.py has the measured
# log. The filename here is reconstructed from the recorded tag rather than
# scraped from a log, so it can be checked offline.
assert info["tags"] == ["cp312-cp312-musllinux_1_2_x86_64"], info["tags"]
assert wheel_filename() == "orjson-3.11.9-cp312-cp312-musllinux_1_2_x86_64.whl"

# It is a compiled extension, so the wheel choice is load bearing.
assert info["root_is_purelib"] is False
extension = extension_module_path()
libc = interpreter_libc()
assert os.path.basename(extension) == "orjson.cpython-312-x86_64-linux-musl.so"

# That filename is this interpreter's own EXT_SUFFIX, which is what makes
# import find it: a glibc CPython looks for a name ending in -gnu.so and
# never sees this file at all. The wheel is refused by name before any
# loader is asked anything.
assert os.path.basename(extension) == "orjson" + libc["ext_suffix"]

# And it pulls in nothing else, which is why a --no-deps requirements.txt of
# one line is the complete transitive closure.
assert info["requires_dist"] == []

# Measured, against the expectation that a musllinux wheel would be
# statically linked: it is not. The extension needs exactly one library, and
# it is named libc.so — a name no file carries anywhere the loader looks.
assert elf_needed_libraries(extension) == ["libc.so"]
assert libc["libc_so_file"] is False, libc
assert libc["musl_libc_file"] is True, libc

# It imports regardless, because musl's loader answers to that name itself,
# and says so when asked directly. That name is the second thing that stops
# this file on glibc, behind the EXT_SUFFIX mismatch above: pushed past
# import with ctypes there, python:3.12-slim raises "libc.so: cannot open
# shared object file", carrying only libc.so.6. Measured on that image, not
# in here — nothing in this container can see it.
assert orjson.dumps({"loaded": True}) == b'{"loaded":true}'
report = musl_loader_report(extension)
assert "libc.so => /lib/ld-musl-x86_64.so.1" in report, report

# Run standalone the loader also cannot relocate the CPython symbols, which
# is the other half of how the extension is linked: Py* comes from the
# interpreter process, so nothing links libpython.
assert "Error relocating" in report and "PyDict_New: symbol not found" in report
assert not any("python" in name for name in elf_needed_libraries(extension))

# The reader is not just returning what suits the story: the same parser
# finds the versioned musl soname and libpython in the interpreter binary.
control = elf_needed_libraries(os.path.realpath(sys.executable))
assert "libc.musl-x86_64.so.1" in control, control
assert "libpython3.12.so.1.0" in control, control

# And the musl match is real rather than an image name: the ABI tag names
# musl, the musl loader is present, glibc's is not.
assert libc["soabi"] == "cpython-312-x86_64-linux-musl", libc
assert libc["host"] == "x86_64-pc-linux-musl", libc
assert libc["musl_loader"] is True and libc["glibc_loader"] is False, libc

# --- bytes, not str ------------------------------------------------------

# The difference that breaks the most call sites: the result never goes
# through a str decode on the way out.
assert isinstance(orjson.dumps({"a": 1}), bytes)
assert isinstance(json.dumps({"a": 1}), str)

# Two smaller output differences that break byte-for-byte comparisons
# against json: orjson emits no spaces after ":" or ",", and it has no
# ensure_ascii, so non-ASCII stays UTF-8 instead of being \uXXXX escaped.
assert orjson.dumps({"a": 1, "b": 2}) == b'{"a":1,"b":2}'
assert json.dumps({"a": 1, "b": 2}) == '{"a": 1, "b": 2}'
assert orjson.dumps({"k": "café"}) == '{"k":"café"}'.encode("utf-8")
assert json.dumps({"k": "café"}) == r'{"k": "caf\u00e9"}'
assert dumps_text({"k": "café"}) == '{"k":"café"}'

# The tight spacing is the default, not a fixed format: under OPT_INDENT_2
# the space after the colon comes back and the output is byte-for-byte what
# json.dumps(indent=2) produces. If a diff against json output has to keep
# passing, pretty printing is the cheaper migration than a separators fix.
pretty = {"a": [1, 2], "b": {"c": 1}}
assert dumps_text(pretty, indent=True) == json.dumps(pretty, indent=2)
assert dumps_text(pretty, indent=True) == '{\n  "a": [\n    1,\n    2\n  ],\n  "b": {\n    "c": 1\n  }\n}'

# There is no file API at all — json.dump(obj, fp) has no counterpart, you
# write the bytes yourself.
assert not hasattr(orjson, "dump") and not hasattr(orjson, "load")

# --- datetime, date, UUID ------------------------------------------------

moment = dt.datetime(2026, 8, 14, 12, 30, 45, 123456, tzinfo=dt.timezone.utc)
identifier = uuid.UUID("12345678-1234-5678-1234-567812345678")

# orjson serializes these natively, in RFC 3339 form.
assert orjson.dumps({"t": moment}) == b'{"t":"2026-08-14T12:30:45.123456+00:00"}'
assert orjson.dumps(dt.date(2026, 8, 14)) == b'"2026-08-14"'
assert orjson.dumps(dt.time(12, 30, 45)) == b'"12:30:45"'
assert orjson.dumps({"u": identifier}) == b'{"u":"12345678-1234-5678-1234-567812345678"}'

# The offset is written +00:00, not Z. Consumers that match on Z need the
# option; this is the one datetime detail that bites after the swap.
assert orjson.dumps(moment, None, orjson.OPT_UTC_Z) == b'"2026-08-14T12:30:45.123456Z"'

# json raises for all of them, which is why so much code carries a default
# hook that orjson does not need.
assert "datetime is not JSON serializable" in str(
    raises(TypeError, lambda: json.dumps({"t": moment}))
)
assert "UUID is not JSON serializable" in str(
    raises(TypeError, lambda: json.dumps({"u": identifier}))
)


class Money:
    def __init__(self, cents: int) -> None:
        self.cents = cents


def encode_money(value):
    if isinstance(value, Money):
        return value.cents
    raise TypeError(f"unsupported: {type(value).__name__}")


# --- the `default` argument, the other way round from the folklore --------

# Measured, against the expectation: orjson is the library that DOES take
# default positionally — dumps(obj, default=None, option=None) — and
# json.dumps is the one that refuses, because everything after obj there is
# keyword-only. So a positional hook that works with orjson raises when the
# call is switched back to json, not the reverse.
assert orjson.dumps({"m": Money(500)}, encode_money) == b'{"m":500}'
assert orjson.dumps({"m": Money(500)}, default=encode_money) == b'{"m":500}'
assert "takes 1 positional argument" in str(
    raises(TypeError, lambda: json.dumps({"m": Money(500)}, encode_money))
)
assert json.dumps({"m": Money(500)}, default=encode_money) == '{"m": 500}'

# What orjson refuses is every other json.dumps keyword: obj, default and
# option are the entire signature.
assert "unexpected keyword argument" in str(
    raises(TypeError, lambda: orjson.dumps({"b": 1}, sort_keys=True))
)
assert "unexpected keyword argument" in str(
    raises(TypeError, lambda: orjson.dumps({"b": 1}, indent=2))
)

# Encode failures come back as builtins.TypeError itself: JSONEncodeError is
# an alias, not a subclass, so `except orjson.JSONEncodeError` and
# `except TypeError` are the same clause. Decode failures are a real
# subclass of both ValueError and json.JSONDecodeError, so json-shaped read
# error handling keeps working.
assert orjson.JSONEncodeError is TypeError
assert "Type is not JSON serializable: Money" == str(
    raises(TypeError, lambda: orjson.dumps({"m": Money(500)}))
)
assert orjson.JSONDecodeError is not json.JSONDecodeError
assert issubclass(orjson.JSONDecodeError, json.JSONDecodeError)
assert issubclass(orjson.JSONDecodeError, ValueError)

# --- sort_keys is an option bit -----------------------------------------

messy = {"b": 1, "a": 2, "C": 3}
assert orjson.dumps(messy) == b'{"b":1,"a":2,"C":3}'
assert orjson.dumps(messy, None, orjson.OPT_SORT_KEYS) == b'{"C":3,"a":2,"b":1}'
assert dumps_text(messy, sort_keys=True) == '{"C":3,"a":2,"b":1}'

# Same ordering json produces with sort_keys=True — sorted by code point, so
# uppercase first. Only the spelling of the flag changed.
assert json.dumps(messy, sort_keys=True) == '{"C": 3, "a": 2, "b": 1}'

# --- non-str dict keys: refused, not coerced -----------------------------

assert "Dict key must be str" == str(raises(TypeError, lambda: orjson.dumps({1: "a"})))
assert orjson.dumps({1: "a"}, None, orjson.OPT_NON_STR_KEYS) == b'{"1":"a"}'
assert json.dumps({1: "a"}) == '{"1": "a"}'

# Why the refusal is worth having. json's silent coercion turns two distinct
# Python keys into one JSON key, emits both, and the value of the first is
# gone as soon as anything parses it back.
collision = {1: "a", "1": "b"}
assert len(collision) == 2
assert json.dumps(collision) == '{"1": "a", "1": "b"}'
assert json.loads(json.dumps(collision)) == {"1": "b"}
raises(TypeError, lambda: orjson.dumps(collision))

# OPT_NON_STR_KEYS opts into json's behaviour, collision included — the
# option is not a safer coercion, it is the same one made explicit.
assert orjson.dumps(collision, None, orjson.OPT_NON_STR_KEYS) == b'{"1":"a","1":"b"}'

# Both draw the line at keys that are not scalars.
raises(TypeError, lambda: orjson.dumps({(1, 2): "x"}, None, orjson.OPT_NON_STR_KEYS))
raises(TypeError, lambda: json.dumps({(1, 2): "x"}))

# --- nan and infinity: also the other way round --------------------------

hook_calls = []


def record(value):
    hook_calls.append(value)
    return None


# Measured, against the expectation: orjson does not reject non-finite
# floats. It writes null, silently, and the default hook is never consulted
# because float is a type orjson handles — so there is no way to intercept
# the loss from inside the dumps call.
assert orjson.dumps(float("nan"), record) == b"null"
assert orjson.dumps([float("inf"), float("-inf")], record) == b"[null,null]"
assert hook_calls == []

# json goes wrong in the opposite direction: it emits bare NaN and Infinity,
# which are not JSON, and only refuses when asked to.
assert json.dumps(float("nan")) == "NaN"
assert json.dumps([float("inf")]) == "[Infinity]"
assert "not JSON compliant" in str(
    raises(ValueError, lambda: json.dumps(float("nan"), allow_nan=False))
)

# The consequence of that pairing: json output can be unreadable by orjson.
# json.loads takes its own non-standard tokens back, orjson does not.
broken = json.dumps({"x": float("inf")})
decode_error = raises(orjson.JSONDecodeError, lambda: orjson.loads(broken))
assert isinstance(decode_error, json.JSONDecodeError)
assert json.loads(broken)["x"] == float("inf")

# Since orjson cannot report the loss, the guard has to run before dumps.
assert has_non_finite_float({"a": [1.0, float("nan")]}) is True
assert has_non_finite_float({"a": [1.0, 2, "3"]}) is False

# Decimal is the other type that can hold a NaN, and the guard deliberately
# ignores it: orjson does not serialize Decimal at all, so a non-finite one
# raises instead of vanishing. The silence returns only if a default hook
# floats it, and then it is null like any other nan.
assert has_non_finite_float(Decimal("NaN")) is False
assert Decimal("NaN").is_nan() and Decimal("Infinity").is_infinite()
assert "Type is not JSON serializable: decimal.Decimal" == str(
    raises(TypeError, lambda: orjson.dumps(Decimal("NaN")))
)
assert orjson.dumps({"d": Decimal("NaN")}, float) == b'{"d":null}'

# --- one more silent difference in range ---------------------------------

# orjson caps integers at 64 bits, json does not. Measured, the accepted
# band is the union of the signed and unsigned ranges — -2**63 through
# 2**64-1 — so an id at the very top of a uint64 column still serializes.
# What starts raising after the swap is a Python bignum, in either
# direction.
assert json.dumps(2**64) == "18446744073709551616"
assert orjson.dumps(2**64 - 1) == b"18446744073709551615"
assert orjson.dumps(-(2**63)) == b"-9223372036854775808"
assert "Integer exceeds 64-bit range" == str(raises(TypeError, lambda: orjson.dumps(2**64)))
assert "Integer exceeds 64-bit range" == str(
    raises(TypeError, lambda: orjson.dumps(-(2**63) - 1))
)

print("contract ok:", orjson.__version__, info["tags"][0])
