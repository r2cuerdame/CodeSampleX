"""Writing polars when your hands still type pandas.

The pandas habits that carry over produce either a loud error or, once, a
quietly wrong answer. This module is the pipeline written the polars way,
with the trap named at each step.

What is not here, because it does not exist: there is no index. No .loc, no
.iloc, no .index, no reset_index. A polars DataFrame is a list of named
columns and rows are addressed by position only for the moment you are
looking at them. Filtering renumbers; if you need the original row number
to survive a filter, you add it as a real column with with_row_index BEFORE
filtering, and then it is data like any other column.

Selection is expressions, not bracket indexing. df.select(pl.col("x")) is
the API; brackets exist but mean something else, and that is where the one
silent failure lives. df[bool_series] does not filter rows — a boolean mask
in brackets selects COLUMNS. With a mask whose length does not match the
column count you get a ValueError naming the real meaning. With a frame
that happens to have as many columns as rows, which is exactly the shape of
a small test fixture, you get no error at all and a frame of the wrong
columns. The pandas reflex df[df["qty"] > 2] is the one line here that can
pass review. Write df.filter(pl.col("qty") > 2).

Mutation is where "polars always hands back a new frame" turned out to be
too strong, and the measurement is the more useful answer. The expression
API really does not mutate: select, filter and with_columns return a new
frame, there is no inplace= to pass one, and df["new"] = values raises
TypeError pointing at with_columns. The frame-stacking methods are the
exception. hstack, vstack and shrink_to_fit take in_place=, and extend,
insert_column and drop_in_place mutate the receiver outright. The in_place=
form returns the SAME object rather than None, so

    combined = df.vstack(other, in_place=True)

reads exactly like the pure version while every other reference to df has
just changed underneath it.

Returning a new frame is cheap because the Arrow buffers are shared: a
column the operation did not touch is the same allocation in both frames,
so what gets copied is the column list, not the data.
"""

import polars as pl


def with_line_totals(df: pl.DataFrame) -> pl.DataFrame:
    """Add a derived column. The input frame is untouched afterwards.

    The naming rule catches people once: an expression's output name is the
    name of the FIRST column it mentions, so pl.col("qty") * 2 is called
    "qty" and with_columns replaces the qty column instead of adding one.
    Adding a column rather than overwriting one is what .alias() is for.
    """
    return df.with_columns(
        (pl.col("qty") * pl.col("unit_price")).alias("line_total")
    )


def large_orders(df: pl.DataFrame, minimum: float, region: str) -> pl.DataFrame:
    """filter takes one boolean expression, and the parentheses are load bearing.

    Two conditions combine with & and |, never with `and` and `or`: an Expr
    has no truth value, so `and` raises TypeError before polars ever sees
    the query. The parentheses matter because & binds tighter than the
    comparison operators, so

        pl.col("line_total") >= minimum & pl.col("region") == region

    parses as line_total >= (minimum & col) == region, a chained comparison,
    which Python evaluates with `and` and which therefore dies with the same
    "truth value of an Expr is ambiguous" as the first mistake. The error
    text does not mention precedence, so the fix looks unrelated to the bug.

    Passing the conditions as separate positional arguments is the version
    with no precedence to get wrong: df.filter(a, b) means a AND b.
    """
    return df.filter(
        (pl.col("line_total") >= minimum) & (pl.col("region") == region)
    )


def revenue_by_region(df: pl.DataFrame) -> pl.DataFrame:
    """Aggregate. The column order is documented, the ROW order is not.

    Columns come out in a fixed order: the group keys first, then one column
    per expression in the order given to agg. That much you can rely on.

    Row order is the trap. pandas groupby sorts by the key by default;
    polars group_by does not sort and does not preserve input order either,
    because the groups are built in parallel. maintain_order=True buys back
    first-appearance order at a cost in speed, and first appearance is still
    not sorted order. If a test compares against a literal list of rows,
    sort explicitly — which is why this ends in .sort("region") even though
    maintain_order has already made it deterministic.

    Every agg expression gets an alias for the same reason as above: two
    expressions over the same column both default to that column's name and
    the query fails with DuplicateError.
    """
    return (
        df.group_by("region", maintain_order=True)
        .agg(
            pl.col("line_total").sum().alias("revenue"),
            pl.len().alias("orders"),
        )
        .sort("region")
    )


def audited_plan(frame: pl.LazyFrame, hook) -> pl.LazyFrame:
    """A lazy plan that leaves a countable trace of what the engine ran.

    Nothing in here executes. A LazyFrame is a query plan, and the methods
    on it only extend the plan: .collect() is the single call that runs it.
    You can watch that from outside, which is what the hook is for — it is a
    Python function called once per row the engine actually feeds through
    map_elements, so an empty call list is proof that no data moved.

    The plan is inspectable before it runs. .explain() returns the plan as a
    string without touching a row, and it is worth reading, because the plan
    is not the code you wrote. Filters are pushed down below projections, so
    the map_elements hook here runs on the rows that survive the filter, not
    on all of them. Written eagerly the identical three lines run the hook
    over every row and throw most of the results away.

    Building the plan is not the same as checking it. A LazyFrame naming a
    column that does not exist is constructed without complaint; the
    ColumnNotFoundError arrives when the schema is resolved, which .explain()
    also does. So laziness defers the typo to the end of the pipeline, and
    .explain() is the cheap way to surface it without running the query.
    """
    return frame.with_columns(
        pl.col("qty").map_elements(hook, return_dtype=pl.Int64).alias("audited")
    ).filter(pl.col("qty") >= 4)


def rating_health(df: pl.DataFrame) -> dict:
    """Count missing ratings. There are two kinds and they do not overlap.

    pandas has one hole, NaN, and uses it for both "no value" and "not a
    number". polars keeps them apart: null is absence, recorded in a
    validity bitmap for every dtype, and NaN is a float value that happens
    to be unordered. is_null() and is_nan() therefore answer different
    questions, and is_nan() over a null returns null rather than False —
    three-valued logic, all the way through.

    The consequence people hit in production is here in the mean: nulls are
    skipped by every aggregate, but a NaN is a real float and propagates, so
    one NaN turns the average of a million rows into nan while null_count()
    still reports zero. Only is_nan() finds it.

    An integer column with a missing value is worth contrasting: polars
    keeps it Int64 with a null, where pandas upcasts the column to float64
    and writes NaN. Nothing about the dtype tells you a value went missing
    in polars, and nothing about the value tells you it was an integer in
    pandas.
    """
    return df.select(
        pl.len().alias("rows"),
        pl.col("rating").count().alias("counted"),
        pl.col("rating").null_count().alias("nulls"),
        pl.col("rating").is_nan().sum().alias("nans"),
        pl.col("rating").mean().alias("mean"),
    ).row(0, named=True)


def normalize_ratings(df: pl.DataFrame, default: float) -> pl.DataFrame:
    """Fill both holes. Neither fill function touches the other's.

    fill_null leaves NaN alone and fill_nan leaves null alone, so the pandas
    fillna(0) reflex covers half the cases and silently misses the other
    half. Chaining fill_nan(None) first is the idiom that collapses the two
    kinds into one: it rewrites NaN as null, and then a single fill_null
    handles everything. drop_nulls and drop_nans split the same way — each
    keeps the other's values.
    """
    return df.with_columns(
        pl.col("rating").fill_nan(None).fill_null(default)
    )
