import numpy as np


source = np.array([1, 2, 3], dtype=np.int32)

# np.array copies an ndarray by default. copy=False can reuse the exact input
# when no dtype or layout change is requested.
array_default = np.array(source)
assert array_default is not source
assert not np.shares_memory(array_default, source)
array_default[0] = 99
assert source.tolist() == [1, 2, 3]

array_reused = np.array(source, copy=False)
assert array_reused is source
assert np.shares_memory(array_reused, source)

# np.asarray defaults to reuse, while copy=False is a strict promise: a Python
# sequence cannot become an ndarray without allocation, so it raises.
asarray_reused = np.asarray(source)
assert asarray_reused is source
try:
    np.asarray([1, 2, 3], copy=False)
except ValueError as exc:
    assert "Unable to avoid copy" in str(exc)
else:
    raise AssertionError("np.asarray(list, copy=False) must reject allocation")

# ndarray.astype uses a softer copy=False rule. It returns the same object for
# an unchanged dtype, but performs the required allocation for a dtype change
# instead of raising. Its default still copies even when the dtype is unchanged.
astype_reused = source.astype(np.int32, copy=False)
assert astype_reused is source

astype_default = source.astype(np.int32)
assert astype_default is not source
assert not np.shares_memory(astype_default, source)

astype_converted = source.astype(np.float64, copy=False)
assert astype_converted.dtype == np.float64
assert astype_converted.tolist() == [1.0, 2.0, 3.0]
assert not np.shares_memory(astype_converted, source)

# Equal values do not imply aliasing. Basic slicing is a view, whereas advanced
# integer indexing allocates a result with its own buffer.
slice_view = source[1:]
advanced_copy = source[[1, 2]]
same_values = np.array([2, 3], dtype=np.int32)
assert np.shares_memory(slice_view, source)
assert not np.shares_memory(advanced_copy, source)
assert not np.shares_memory(slice_view, same_values)

print("CONTRACT PASS: NumPy 2.5.1 copy and memory-sharing semantics")
