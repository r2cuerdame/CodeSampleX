import inspect
import sys
import warnings
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

import polars as pl

from src.frames import (
    audited_plan,
    large_orders,
    normalize_ratings,
    rating_health,
    revenue_by_region,
    with_line_totals,
)
from src.install import installed, interpreter_libc, wheel_filename

NAN = float("nan")

ORDERS = {
    "region": ["north", "south", "north", "east", "south", "north"],
    "qty": [1, 3, 2, 5, 4, 2],
    "unit_price": [10.0, 2.5, 4.0, 1.5, 3.0, 8.0],
}


def raises(expected, call):
    """Run call, require `expected`, and hand the exception back for checking."""
    try:
        call()
    except expected as caught:
        return caught
    raise AssertionError(f"expected {expected.__name__}, nothing was raised")


# --- packaging: polars is two distributions on Alpine --------------------

core = installed("polars")
runtime = installed("polars-runtime-32")

# The pure-Python half: the universal tag, and nothing compiled in it.
assert core["tags"] == ["py3-none-any"], core["tags"]
assert core["root_is_purelib"] is True
assert core["shared_objects"] == [], core["shared_objects"]

# The engine is a separate distribution, and it is the one with the wheel
# tags. pip took the musllinux wheel here, which answers the only question
# that has to be settled before any of the rest of this file can run: a
# polars wheel does exist for musl, and 1.43.2 has one.
assert "polars-runtime-32==1.43.2" in core["requires_dist"], core["requires_dist"]
assert runtime["tags"] == ["cp310-abi3-musllinux_1_2_x86_64"], runtime["tags"]
assert runtime["root_is_purelib"] is False
assert (
    wheel_filename("polars-runtime-32")
    == "polars_runtime_32-1.43.2-cp310-abi3-musllinux_1_2_x86_64.whl"
)

# cp310 on a 3.12 interpreter is the stable ABI, not a mismatch: the shared
# object is named .abi3.so instead of this interpreter's EXT_SUFFIX, which
# is why one build covers 3.10 and up and why there is no per-minor wheel.
libc = interpreter_libc()
assert runtime["shared_objects"] == ["_polars_runtime_32/_polars_runtime.abi3.so"]
assert not runtime["shared_objects"][0].endswith(libc["ext_suffix"])
assert libc["ext_suffix"] == ".cpython-312-x86_64-linux-musl.so", libc

# And the musllinux match is real rather than an image name.
assert libc["soabi"] == "cpython-312-x86_64-linux-musl", libc

# The check that catches a half-installed polars. Measured: with only the
# `polars` distribution present, import succeeds with a UserWarning, and
# __version__ is the empty string until the first NameError from inside the
# library. A non-empty version agreeing with the metadata is the cheap proof
# that the binary half arrived; `import polars` alone is not.
assert pl.__version__ == core["version"] == "1.43.2"

# --- there is no index ---------------------------------------------------

df = with_line_totals(pl.DataFrame(ORDERS))

# None of the pandas row-addressing API exists, so code carrying it fails
# loudly at the attribute rather than subtly at the result.
for attribute in ("index", "loc", "iloc", "at", "iat", "values",
                  "reset_index", "sort_values", "assign", "query", "apply"):
    assert not hasattr(df, attribute), attribute

# Row position is not an identity. Filtering renumbers from zero, and the
# only way to carry the original position past a filter is to write it into
# a column first, where it is ordinary UInt32 data.
assert df.filter(pl.col("qty") >= 4).with_row_index()["index"].to_list() == [0, 1]
assert df.with_row_index().filter(pl.col("qty") >= 4)["index"].to_list() == [3, 4]
assert df.with_row_index().schema["index"] == pl.UInt32

# --- selection is expressions, and brackets mean something else ----------

# Brackets still do the two things that look like pandas: a string picks a
# column and gives back a Series, an integer picks a row and gives back a
# one-row DataFrame. That mixture is already the reverse of pandas, where
# df[0] would be a column label lookup.
assert isinstance(df["qty"], pl.Series)
assert isinstance(df[0], pl.DataFrame) and df[0].shape == (1, 4)
assert raises(pl.exceptions.ColumnNotFoundError, lambda: df["nope"])

# The expression API is the one to write, and select takes plain strings,
# regexes and pl.all() through the same door.
assert df.select(pl.col("qty")).columns == ["qty"]
assert df.select("qty", "region").columns == ["qty", "region"]
assert df.select(pl.col("^unit_.*$")).columns == ["unit_price"]

# The one pandas reflex that can pass review. df[mask] is not a row filter
# in polars: a boolean mask in brackets selects COLUMNS. Against this frame
# the lengths disagree and it raises, and the message says what brackets
# actually meant.
mask = df["qty"] >= 4
assert mask.to_list() == [False, False, False, True, True, False]
assert "selecting columns by boolean mask" in str(
    raises(ValueError, lambda: df[mask])
)

# Measured, and the reason this is worth a sample: when the frame has as
# many columns as the mask has rows — the shape of most test fixtures — the
# length check passes and there is no error at all. Here a mask meaning
# "rows 2 and 3" silently returns columns b and c, every row intact.
square = pl.DataFrame({"a": [1, 2, 3], "b": [4, 5, 6], "c": [7, 8, 9]})
wrong = square[square["a"] > 1]
assert wrong.columns == ["b", "c"] and wrong.shape == (3, 2)
assert wrong.to_dicts() == [{"b": 4, "c": 7}, {"b": 5, "c": 8}, {"b": 6, "c": 9}]

# What the same intent produces written as a filter.
right = square.filter(pl.col("a") > 1)
assert right.columns == ["a", "b", "c"] and right.shape == (2, 3)

# --- filter takes a boolean expression -----------------------------------

assert large_orders(df, 9.0, "north").to_dicts() == [
    {"region": "north", "qty": 1, "unit_price": 10.0, "line_total": 10.0},
    {"region": "north", "qty": 2, "unit_price": 8.0, "line_total": 16.0},
]

# An Expr has no truth value, so `and` and `or` never reach polars at all.
ambiguous = "the truth value of an Expr is ambiguous"
assert ambiguous in str(raises(TypeError, lambda: bool(pl.col("qty") > 2)))
assert ambiguous in str(
    raises(TypeError, lambda: df.filter((pl.col("qty") > 1) and (pl.col("qty") < 5)))
)
assert ambiguous in str(
    raises(TypeError, lambda: df.filter((pl.col("qty") > 1) or (pl.col("qty") < 5)))
)

# & binds tighter than the comparisons, so dropping the parentheses turns
# the whole thing into a chained comparison and fails with that identical
# message — the error names truthiness, never precedence, so the reported
# symptom points away from the missing brackets.
assert ambiguous in str(
    raises(TypeError, lambda: df.filter(pl.col("qty") > 1 & pl.col("unit_price") < 5.0))
)
assert df.filter((pl.col("qty") > 1) & (pl.col("unit_price") < 5.0)).height == 4

# Separate positional predicates mean AND with no precedence to get wrong,
# and keyword form is equality against a literal.
assert df.filter(pl.col("qty") > 1, pl.col("unit_price") < 5.0).height == 4
assert df.filter(region="north").height == 3

# --- with_columns returns a new frame ------------------------------------

original = pl.DataFrame(ORDERS)
derived = with_line_totals(original)
assert derived is not original
assert original.columns == ["region", "qty", "unit_price"]
assert derived.columns == ["region", "qty", "unit_price", "line_total"]
assert derived["line_total"].to_list() == [10.0, 7.5, 8.0, 7.5, 12.0, 16.0]

# The expression API has no inplace= to pass and no item assignment to
# reach for. The TypeError names the replacement rather than leaving you to
# guess at it.
assert "inplace" not in inspect.signature(pl.DataFrame.with_columns).parameters


def assign_column():
    original["line_total"] = [1.0] * 6


message = str(raises(TypeError, assign_column))
assert "does not support" in message and "DataFrame.with_columns" in message, message
assert original.columns == ["region", "qty", "unit_price"]

# Handing back a new frame is cheap because it is not a copy of the data:
# the column with_columns did not touch is the same Arrow allocation in both
# frames. _get_buffer_info is a private accessor, used here as evidence and
# not as an API to write — it reports (pointer, offset, length).
assert original["qty"]._get_buffer_info() == derived["qty"]._get_buffer_info()

# Measured, against "polars never mutates and has no in_place anywhere":
# the frame-stacking methods kept an escape hatch. hstack, vstack and
# shrink_to_fit take in_place=, and extend, insert_column and drop_in_place
# mutate the receiver outright. The in_place= form returns the same object
# instead of None, so the assignment reads exactly like the pure version
# while every other reference to that frame has changed underneath it.
stackable = pl.DataFrame(ORDERS)
combined = stackable.vstack(pl.DataFrame(ORDERS), in_place=True)
assert combined is stackable and stackable.height == 12
assert stackable.hstack([pl.Series("tag", ["x"] * 12)], in_place=True) is stackable
assert stackable.columns == ["region", "qty", "unit_price", "tag"]

growing = pl.DataFrame({"x": [1, 2]})
assert growing.extend(pl.DataFrame({"x": [3]})) is growing and growing.height == 3
assert growing.insert_column(1, pl.Series("y", [9, 9, 9])) is growing
assert growing.drop_in_place("y").to_list() == [9, 9, 9]
assert growing.columns == ["x"]

# An expression is named after the first column it mentions, so without an
# alias with_columns REPLACES that column instead of adding one. The frame
# is still new either way — the overwrite is of a column in the copy.
overwritten = original.with_columns(pl.col("qty") * 2)
assert overwritten.columns == original.columns
assert overwritten["qty"].to_list() == [2, 6, 4, 10, 8, 4]
assert original["qty"].to_list() == [1, 3, 2, 5, 4, 2]

# --- a LazyFrame does nothing until collect ------------------------------

calls: list[int] = []


def audit(value: int) -> int:
    calls.append(value)
    return value


plan = audited_plan(pl.LazyFrame(ORDERS), audit)
assert isinstance(plan, pl.LazyFrame)

# The plan exists as a readable string while the call list is still empty:
# explain resolves the schema and prints the query, and moves no data.
explained = plan.explain()
assert isinstance(explained, str)
assert calls == [], calls
assert "FILTER" in explained and "WITH_COLUMNS" in explained

# What explain shows is not the code that was written. Unoptimized, the
# filter sits above the projection, in source order. Optimized, it has been
# pushed underneath it.
unoptimized = plan.explain(optimized=False)
assert unoptimized.index("FILTER") < unoptimized.index("WITH_COLUMNS")
assert explained.index("WITH_COLUMNS") < explained.index("FILTER")

# Collect is the only call that runs anything, and the pushdown is not just
# a nicer printout: the Python hook is invoked on the two rows that survive
# the filter, in the order the engine reached them.
result = plan.collect()
assert calls == [5, 4], calls
assert result.shape == (2, 4)
assert result["audited"].to_list() == [5, 4]

# The same three lines eagerly run the hook over all six rows and discard
# four of the results. This is the whole argument for the lazy API stated as
# a measurement, and it is why a lazy pipeline is not just a deferred eager
# one.
eager_calls: list[int] = []


def audit_eagerly(value: int) -> int:
    eager_calls.append(value)
    return value


eager = (
    pl.DataFrame(ORDERS)
    .with_columns(
        pl.col("qty")
        .map_elements(audit_eagerly, return_dtype=pl.Int64)
        .alias("audited")
    )
    .filter(pl.col("qty") >= 4)
)
assert eager_calls == [1, 3, 2, 5, 4, 2], eager_calls
assert eager.height == 2

# Deferral moves the errors too. A LazyFrame over a column that does not
# exist is built without complaint; the ColumnNotFoundError arrives when the
# plan is resolved, and explain resolves it, so explain is the cheap way to
# find a typo without running the query.
typo = pl.LazyFrame({"a": [1]}).select(pl.col("nope"))
assert isinstance(typo, pl.LazyFrame)
assert 'unable to find column "nope"' in str(
    raises(pl.exceptions.ColumnNotFoundError, typo.explain)
)
raises(pl.exceptions.ColumnNotFoundError, typo.collect)

# A LazyFrame has no shape, because knowing the height means running the
# query. It will answer for its columns, but only by resolving the schema,
# and it says so with a PerformanceWarning rather than doing it quietly.
lazy = pl.LazyFrame(ORDERS)
assert not hasattr(lazy, "shape")
with warnings.catch_warnings(record=True) as caught:
    warnings.simplefilter("always")
    columns = lazy.columns
assert [type(w.message) for w in caught] == [pl.exceptions.PerformanceWarning]
assert columns == ["region", "qty", "unit_price"]
assert lazy.collect_schema().names() == columns

# --- null is not NaN -----------------------------------------------------

ratings = pl.DataFrame({"rating": [4.5, None, NAN, 3.0]})
series = ratings["rating"]

# Two different holes, and each predicate sees only its own. is_nan over a
# null is null, not False: the logic is three-valued, so a mask built this
# way is not a plain list of booleans.
assert series.is_null().to_list() == [False, True, False, False]
assert series.is_nan().to_list() == [False, None, True, False]

# The consequence: null_count reports a clean column while the mean is nan.
# Nulls are skipped by aggregates, a NaN is a real float and propagates, and
# count() sees the NaN as a value while pl.len() counts every row.
health = rating_health(ratings)
assert health["rows"] == 4
assert health["counted"] == 3
assert health["nulls"] == 1
assert health["nans"] == 1
assert health["mean"] != health["mean"]

# The same column without a null in it, which is the shape that survives
# review: null_count reports a clean column and the mean is nan anyway. A
# missing-data check written as null_count() == 0 passes on this.
nan_only = pl.Series("v", [1.0, NAN, 3.0])
assert nan_only.null_count() == 0
assert nan_only.mean() != nan_only.mean()
assert nan_only.is_nan().sum() == 1

# Measured, against the expectation that a NaN poisons every aggregate the
# way it poisons the mean: min and max ignore it. sum and mean propagate.
# The same column gives a usable answer from one aggregate and nan from the
# next, which is what makes this hard to spot in a report.
assert series.sum() != series.sum()
assert series.min() == 3.0 and series.max() == 4.5

# Comparison is three-valued too. A null compares to null, not to False, so
# a null row is neither kept nor rejected by a comparison — filter drops it.
# eq_missing is the two-valued version.
assert (series == 4.5).to_list() == [True, None, False, False]
assert series.eq_missing(4.5).to_list() == [True, False, False, False]

# Measured, and the opposite of the IEEE rule Python itself follows: polars
# orders NaN above every number and compares it equal to itself, so nan ==
# nan is True here and nan > 1e308 is True, where the plain Python floats
# asserted first say False to both. A "drop the outliers" filter written as
# a comparison therefore KEEPS the NaN rows and drops the nulls — exactly
# backwards from the intent.
assert NAN != NAN
assert not (NAN > 0)
assert (pl.Series([NAN]) == NAN).to_list() == [True]
assert (pl.Series([NAN]) > 1e308).to_list() == [True]
kept = ratings.filter(pl.col("rating") > 0)["rating"].to_list()
assert len(kept) == 3 and kept[1] != kept[1]

# Sorting places nulls first in both directions, which is not where a
# descending sort by score would put "missing".
assert ratings.sort("rating")["rating"].null_count() == 1
assert ratings.sort("rating")["rating"][0] is None
assert ratings.sort("rating", descending=True)["rating"][0] is None
assert ratings.sort("rating", nulls_last=True)["rating"][3] is None

# Each dropper keeps the other's holes, and each filler ignores them.
assert series.drop_nulls().len() == 3 and series.drop_nulls().is_nan().sum() == 1
assert series.drop_nans().len() == 3 and series.drop_nans().null_count() == 1
assert series.fill_null(0.0).is_nan().sum() == 1
assert series.fill_nan(0.0).null_count() == 1

# fill_nan(None) collapses the two kinds into one so a single fill covers
# both, which is what the pandas fillna(0) reflex was assuming all along.
assert normalize_ratings(ratings, 0.0)["rating"].to_list() == [4.5, 0.0, 0.0, 3.0]

# An integer column keeps its dtype through a missing value, where pandas
# upcasts to float64 and writes NaN. Aggregates skip the null on both sides
# of the division: mean is 4 / 2, not 4 / 3.
integers = pl.Series("i", [1, None, 3])
assert integers.dtype == pl.Int64
assert integers.sum() == 4 and integers.mean() == 2.0

# --- group_by: column order is fixed, row order is not -------------------

summary = revenue_by_region(df)

# Keys first, then one column per agg expression in the order given.
assert summary.columns == ["region", "revenue", "orders"]
assert summary.to_dicts() == [
    {"region": "east", "revenue": 7.5, "orders": 1},
    {"region": "north", "revenue": 34.0, "orders": 3},
    {"region": "south", "revenue": 19.5, "orders": 2},
]

# Row order is the part to pin down yourself. maintain_order=True gives
# first-appearance order, and first appearance is not sorted order — pandas
# groupby sorts by the key by default, so a ported test comparing row lists
# fails on ordering alone. Without maintain_order the groups are built in
# parallel and the order is not specified at all, which is why only the set
# of rows is checked there.
ordered = df.group_by("region", maintain_order=True).agg(
    pl.col("line_total").sum().alias("revenue")
)
assert ordered["region"].to_list() == ["north", "south", "east"]
assert sorted(ordered["region"].to_list()) == ["east", "north", "south"]

unordered = df.group_by("region").agg(pl.col("line_total").sum().alias("revenue"))
assert sorted(unordered.to_dicts(), key=lambda row: row["region"]) == [
    {"region": "east", "revenue": 7.5},
    {"region": "north", "revenue": 34.0},
    {"region": "south", "revenue": 19.5},
]

# Two aggregates over one column both default to that column's name, and the
# collision is a DuplicateError rather than a silently dropped column.
assert df.group_by("region").agg(pl.col("line_total").sum()).columns == [
    "region",
    "line_total",
]
assert "has more than one occurrence" in str(
    raises(
        pl.exceptions.DuplicateError,
        lambda: df.group_by("region").agg(
            pl.col("line_total").sum(), pl.col("line_total").max()
        ),
    )
)

# Measured while writing this: the identical mistake in a select reports as
# DuplicateError too but with different wording, so a log grep tuned to one
# message will not find the other.
assert "duplicate output name" in str(
    raises(
        pl.exceptions.DuplicateError,
        lambda: df.select(pl.col("qty").min(), pl.col("qty").max()),
    )
)

# The result is a plain DataFrame: no index to reset, the keys are columns.
assert not hasattr(summary, "index")

print("contract ok:", pl.__version__, runtime["tags"][0])
