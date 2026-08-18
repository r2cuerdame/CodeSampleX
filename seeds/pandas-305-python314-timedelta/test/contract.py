from datetime import timedelta

import pandas as pd


EXPECTED_DAYS = 28
EXPECTED_NANOSECONDS = 2_419_200_000_000_000


def assert_twenty_eight_days(value: pd.Timedelta, label: str) -> None:
    assert isinstance(value, pd.Timedelta), f"{label}: {type(value)!r}"
    assert value == pd.Timedelta(days=EXPECTED_DAYS), f"{label}: {value!r}"
    assert value.value == EXPECTED_NANOSECONDS, f"{label}: {value.value}"
    assert value.to_pytimedelta() == timedelta(days=EXPECTED_DAYS), label


def main() -> None:
    # Each expression was a minimal reproducer for the pandas 3.0.4
    # CPython 3.14 wheel crash. Reaching the assertions is part of the
    # contract: a segmentation fault terminates the interpreter first.
    assert_twenty_eight_days(pd.Timedelta(days=28), "keyword constructor")
    assert_twenty_eight_days(pd.Timedelta("28 days"), "duration string")
    assert_twenty_eight_days(pd.Timedelta(28, unit="D"), "value and unit")

    negative = pd.Timedelta(days=-28)
    assert negative.value == -EXPECTED_NANOSECONDS
    assert -negative == pd.Timedelta(days=28)


if __name__ == "__main__":
    main()
    print("pandas 3.0.5 Python 3.14 Timedelta contract passed")
