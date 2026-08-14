"""Replacing json.dumps with orjson.dumps, and the differences that change
behaviour without changing your code.

orjson is not a drop-in for json. The signature is dumps(obj, default=None,
option=None) and that is all it takes: every json.dumps keyword other than
default — sort_keys, indent, ensure_ascii, separators, allow_nan, cls — is
either a bit in `option` or does not exist. Passing one is a TypeError, so
those breakages are loud. The ones worth writing a sample about are the
quiet ones:

  - dumps returns bytes, not str. Anything doing `+ "\\n"` or handing the
    result to a str-typed API breaks; anything writing to a socket or a
    binary file takes them unchanged and never notices.
  - non-ASCII stays UTF-8. json escapes to \\uXXXX by default, orjson has no
    ensure_ascii, so byte-for-byte comparisons against json output fail on
    any payload carrying a character above ASCII.
  - the spacing is tighter: nothing after ":" or ",". That one is only a
    default — under OPT_INDENT_2 the output is json's own indent=2 layout,
    byte for byte, which is the cheapest way to keep such a comparison.
  - datetime, date, time and UUID serialize natively where json raises
    TypeError. The format is RFC 3339 with +00:00, not Z — if a consumer
    matched on Z, add OPT_UTC_Z.
  - non-str dict keys are refused instead of coerced. json turns {1: "a"}
    into {"1": "a"} silently, which is where {1: "a", "1": "b"} becomes two
    identical keys and one value disappears on the way back.
  - nan and infinity become null, silently. See below.

Two things the folklore gets backwards, corrected by measurement:

  `default` positionally. orjson.dumps takes default as its second
  positional argument and option as its third, so orjson.dumps(obj, hook)
  works. json.dumps is the one that refuses it — every parameter after obj
  is keyword-only there, so json.dumps(obj, hook) raises "dumps() takes 1
  positional argument but 2 were given". The migration hazard runs the other
  way from how it is usually told.

  NaN and Infinity. orjson does not reject them. It serializes nan, inf and
  -inf as null, with no error and no way to intercept: `default` is only
  consulted for types orjson cannot serialize, and float is a type it can,
  so the hook is never called. json instead emits bare NaN and Infinity,
  which is not JSON — and orjson.loads then refuses to read it back with
  orjson.JSONDecodeError. So the two libraries fail in opposite directions
  on the same value: json produces something no strict parser accepts,
  orjson produces valid JSON that has quietly lost the number. If a NaN in
  the payload must not pass silently, the check has to be yours, before the
  dumps call.

One more identity worth knowing before writing except clauses: encode errors
are raised as builtins.TypeError itself. orjson.JSONEncodeError is not a
subclass of TypeError, it is the same object, so the two except clauses
catch exactly the same thing. Decode errors are a real subclass, of both
ValueError and json.JSONDecodeError, so existing json error handling keeps
working on the read side.
"""

import orjson


def dumps_text(
    obj,
    *,
    default=None,
    sort_keys: bool = False,
    non_str_keys: bool = False,
    indent: bool = False,
) -> str:
    """A json.dumps-shaped wrapper: str out, keywords mapped onto `option`.

    Write this adapter once rather than fixing call sites one at a time. The
    options are a bitmask, so they OR together; 0 means "no options", which
    orjson accepts in the same position as a real flag.
    """
    option = 0
    if sort_keys:
        option |= orjson.OPT_SORT_KEYS
    if non_str_keys:
        option |= orjson.OPT_NON_STR_KEYS
    if indent:
        option |= orjson.OPT_INDENT_2
    return orjson.dumps(obj, default, option).decode()


def has_non_finite_float(obj) -> bool:
    """The guard orjson cannot give you: find nan/inf before they become null.

    Only floats are walked. int cannot be non-finite; Decimal can —
    Decimal("NaN") and Decimal("Infinity") are ordinary values — but orjson
    refuses Decimal outright with TypeError, so that one fails loudly and
    needs no guard. It goes quiet again if a `default` hook converts the
    Decimal to float, which is the case this function does not cover.
    """
    if isinstance(obj, float):
        return obj != obj or obj in (float("inf"), float("-inf"))
    if isinstance(obj, dict):
        return any(has_non_finite_float(value) for value in obj.values())
    if isinstance(obj, (list, tuple)):
        return any(has_non_finite_float(item) for item in obj)
    return False
