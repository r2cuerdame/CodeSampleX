# Pydantic 2.8.2 StringConstraints ordering

This sample pins Pydantic 2.8.2 and its full dependency set, then measures how
`Annotated[str, StringConstraints(...)]` behaves inside `BaseModel` fields and
list items.

The important ordering result is that whitespace stripping happens before
length and pattern checks, but `to_upper=True` happens after the pattern check.
With the combined constraint in this sample, `" AB "` passes and becomes
`"AB"`, while `" ab "` fails the uppercase-only pattern before it can be
converted. A second model proves that case conversion itself works when no
pattern is present.

The contract also distinguishes default byte decoding from `strict=True`,
checks indexed locations for multiple invalid list items, and confirms that
`model_dump` returns the normalized plain data. Error assertions omit the
documentation URL so the contract focuses on the stable structured fields.
