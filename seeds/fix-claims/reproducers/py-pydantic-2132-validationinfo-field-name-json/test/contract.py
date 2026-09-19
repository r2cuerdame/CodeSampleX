"""Claim (2.13.2): ValidationInfo.field_name names the field for a field_validator when the
model is validated with model_validate_json, as it does with model_validate."""
from pydantic import BaseModel, ValidationInfo, field_validator


class Foo(BaseModel):
    field1: str
    field2: str

    @field_validator("field2")
    @classmethod
    def _validate_field2(cls, v: str, info: ValidationInfo) -> str:
        if info.field_name != "field2":
            raise ValueError(f"info.field_name is {info.field_name!r}, not 'field2'")
        return v


Foo.model_validate({"field1": "a", "field2": "b"})
try:
    Foo.model_validate_json('{"field1": "a", "field2": "b"}')
except Exception as e:  # noqa: BLE001
    raise AssertionError(f"model_validate_json: {e}") from None
print("CONTRACT PASS")
