import sys
from pathlib import Path

import pydantic
from pydantic import ValidationError

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from src.models import NormalizedPayload, Payload, StrictPayload


def expect_error(model, data):
    try:
        model.model_validate(data)
    except ValidationError as error:
        return error.errors(include_url=False)
    raise AssertionError("validation unexpectedly succeeded")


assert pydantic.__version__ == "2.8.2"

payload = Payload.model_validate(
    {
        "code": " AB ",
        "tags": [b"OK", " YES "],
    }
)
assert payload.code == "AB"
assert payload.tags == ["OK", "YES"]
assert payload.model_dump() == {"code": "AB", "tags": ["OK", "YES"]}
assert Payload.model_validate(payload) is payload

# Case conversion works when used by itself.
assert NormalizedPayload.model_validate({"code": " ab "}).code == "AB"

# In this combined StringConstraints schema, stripping happens before the
# pattern, but to_upper happens after it: spaced uppercase passes while the
# lowercase spelling fails before it can become uppercase.
pattern_errors = expect_error(Payload, {"code": " ab ", "tags": ["OK"]})
assert len(pattern_errors) == 1
assert pattern_errors[0]["type"] == "string_pattern_mismatch"
assert pattern_errors[0]["loc"] == ("code",)
assert pattern_errors[0]["input"] == " ab "
assert pattern_errors[0]["ctx"] == {"pattern": "^[A-Z]+$"}

# Length checks also see the stripped value, while errors retain the original
# input so callers can report what was actually supplied.
length_errors = expect_error(Payload, {"code": " a ", "tags": ["OK"]})
assert len(length_errors) == 1
assert length_errors[0]["type"] == "string_too_short"
assert length_errors[0]["loc"] == ("code",)
assert length_errors[0]["input"] == " a "
assert length_errors[0]["ctx"] == {"min_length": 2}

# Non-strict string constraints decode bytes, while strict=True refuses the
# same bytes with a string_type error. An actual string is still normalized.
assert Payload.model_validate({"code": b"AB", "tags": ["OK"]}).code == "AB"
assert StrictPayload.model_validate({"code": " ab "}).code == "AB"
strict_errors = expect_error(StrictPayload, {"code": b" ab "})
assert len(strict_errors) == 1
assert strict_errors[0]["type"] == "string_type"
assert strict_errors[0]["loc"] == ("code",)
assert strict_errors[0]["input"] == b" ab "

# Annotated constraints apply to each list item and error locations retain the
# failing indexes. Pydantic reports both failures in one ValidationError.
nested_errors = expect_error(
    Payload,
    {"code": "OK", "tags": ["YES", "x", "NO-1"]},
)
assert [(error["type"], error["loc"]) for error in nested_errors] == [
    ("string_too_short", ("tags", 1)),
    ("string_pattern_mismatch", ("tags", 2)),
]
assert [error["input"] for error in nested_errors] == ["x", "NO-1"]

print("Pydantic StringConstraints contract passed")
