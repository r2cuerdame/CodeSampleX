from typing import Annotated

from pydantic import BaseModel, StringConstraints


NormalizedWord = Annotated[
    str,
    StringConstraints(strip_whitespace=True, to_upper=True),
]

PatternedWord = Annotated[
    str,
    StringConstraints(
        strip_whitespace=True,
        to_upper=True,
        min_length=2,
        max_length=6,
        pattern=r"^[A-Z]+$",
    ),
]

StrictWord = Annotated[
    str,
    StringConstraints(strict=True, strip_whitespace=True, to_upper=True),
]


class Payload(BaseModel):
    code: PatternedWord
    tags: list[PatternedWord]


class NormalizedPayload(BaseModel):
    code: NormalizedWord


class StrictPayload(BaseModel):
    code: StrictWord
