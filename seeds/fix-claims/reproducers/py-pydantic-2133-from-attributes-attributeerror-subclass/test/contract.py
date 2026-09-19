"""Claim (2.13.3): with from_attributes=True, an attribute whose access raises a subclass of
AttributeError is treated as missing (the field takes its default) rather than failing
validation with get_attribute_error. Upstream (#13092) reproduced this on Python 3.12 and
not on 3.13, so the runtime version is part of the coordinate."""
from pydantic import BaseModel, ConfigDict


class MissingRelation(AttributeError):
    pass


class Child(BaseModel):
    x: int


class Parent(BaseModel):
    model_config = ConfigDict(from_attributes=True)
    child: Child | None = None


class Obj:
    @property
    def child(self):
        raise MissingRelation("missing child")


parent = Parent.model_validate(Obj())
assert parent.child is None, parent.child
print("CONTRACT PASS")
