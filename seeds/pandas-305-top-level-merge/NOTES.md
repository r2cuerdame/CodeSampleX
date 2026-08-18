# pandas 3.0.5 top-level merge boundaries

`pandas.merge` matches missing keys to one another, unlike SQL joins. That
choice affects the result rows, relationship validation, and the newer
`left_anti` and `right_anti` joins: a missing key present on both sides is a
match and therefore must not appear in either anti-join result.

The contract uses nullable `Int64` keys and calls the top-level API requested
by Wanted. It checks outer-join provenance, the exact missing-key match,
`one_to_one` validation failure for a duplicated ordinary key, and both
anti-join directions.

The dependency closure is pinned for the Python 3.14 Alpine verifier. Resolve
is networked and script-free; the contract runs in a fresh network-disabled
container.
