# NumPy 2.5.1 copy and memory-sharing semantics

This sample distinguishes three similar-looking copy controls:

- `numpy.array` copies an existing array by default and can reuse it with
  `copy=False` when no conversion is needed.
- `numpy.asarray` reuses a compatible array by default, and its `copy=False`
  rejects inputs that would require allocation.
- `ndarray.astype(copy=False)` reuses an unchanged dtype but still allocates
  when a dtype conversion is required; it does not make the same strict promise
  as `numpy.asarray(copy=False)`.

It also proves that `numpy.shares_memory` follows buffer aliasing rather than
value equality: a basic slice shares storage, while advanced indexing and an
independently created equal-valued array do not.
