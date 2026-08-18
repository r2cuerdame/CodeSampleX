# Zod 4.4.3 enum-key records

In Zod 4, `z.record(z.enum([...]), valueSchema)` is exhaustive: it validates
that every enum member exists. Use `z.partialRecord(...)` when those known keys
are optional. Neither form accepts keys outside the enum, but their error shapes
differ: exhaustive records report root-level `unrecognized_keys`, while partial
records report `invalid_key` at the offending key.

A record with `z.string()` keys remains an ordinary open dictionary.
