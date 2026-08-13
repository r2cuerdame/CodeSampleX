"""pydantic v2 validation, with the v1 names that no longer exist."""

from pydantic import BaseModel, Field, ValidationError


class Peer(BaseModel):
    peer_id: str = Field(min_length=3)
    port: int = Field(ge=1, le=65535)
    tags: list[str] = []


def validate(payload):
    """Validate a dict into a Peer.

    v2 renamed the whole surface: parse_obj -> model_validate,
    parse_raw -> model_validate_json, .dict() -> .model_dump(),
    .json() -> .model_dump_json(). The old names still resolve as
    deprecated shims, so a v1 codebase keeps running after the upgrade and
    the migration looks finished when it is not — the warnings are the
    only signal, and they are easy to filter away.
    """
    return Peer.model_validate(payload)


def errors_for(payload):
    """Return the structured error list for an invalid payload."""
    try:
        Peer.model_validate(payload)
    except ValidationError as exc:
        return exc.errors()
    return []
