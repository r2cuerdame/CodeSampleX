"""Claim (2.13.1): ValidationInfo.data is populated for a field_validator when the model is
validated with model_validate_json, as it is with model_validate."""
from pydantic import BaseModel, ValidationInfo, field_validator

seen = {}


class Foo(BaseModel):
    field1: str
    field2: str

    @field_validator("field2")
    @classmethod
    def _validate_field2(cls, v: str, info: ValidationInfo) -> str:
        seen[info.mode if hasattr(info, "mode") else "?"] = info.data
        if info.data is None or "field1" not in info.data:
            raise ValueError(f"info.data is {info.data!r}; field1 was validated before field2")
        return v


Foo.model_validate({"field1": "a", "field2": "b"})
try:
    Foo.model_validate_json('{"field1": "a", "field2": "b"}')
except Exception as e:  # noqa: BLE001 - the bug surfaces as a ValidationError wrapping the ValueError
    raise AssertionError(f"model_validate_json: {e}") from None
print("CONTRACT PASS")
