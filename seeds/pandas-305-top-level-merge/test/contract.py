import pandas as pd


def assert_merge_error(callback) -> None:
    try:
        callback()
    except pd.errors.MergeError:
        return
    raise AssertionError("expected pandas.errors.MergeError")


def make_frames() -> tuple[pd.DataFrame, pd.DataFrame]:
    left = pd.DataFrame(
        {
            "key": pd.array([1, None, 3, 3, 4], dtype="Int64"),
            "left_value": ["one", "missing", "three-a", "three-b", "four"],
        }
    )
    right = pd.DataFrame(
        {
            "key": pd.array([None, 2, 3], dtype="Int64"),
            "right_value": ["missing", "two", "three"],
        }
    )
    return left, right


def test_outer_merge_and_validation() -> None:
    left, right = make_frames()
    merged = pd.merge(
        left,
        right,
        on="key",
        how="outer",
        indicator=True,
        validate="many_to_many",
    )

    counts = merged["_merge"].astype("string").value_counts().to_dict()
    assert counts == {"both": 3, "left_only": 2, "right_only": 1}, counts

    missing = merged.loc[merged["key"].isna()]
    assert len(missing) == 1
    assert missing.iloc[0]["left_value"] == "missing"
    assert missing.iloc[0]["right_value"] == "missing"
    assert missing.iloc[0]["_merge"] == "both"

    assert_merge_error(
        lambda: pd.merge(left, right, on="key", validate="one_to_one")
    )


def test_anti_joins_treat_missing_keys_as_matches() -> None:
    left, right = make_frames()
    left_unique = left.drop_duplicates("key", keep="first")

    left_anti = pd.merge(left_unique, right, on="key", how="left_anti")
    right_anti = pd.merge(left_unique, right, on="key", how="right_anti")

    assert sorted(left_anti["key"].astype("int64").tolist()) == [1, 4]
    assert right_anti["key"].astype("int64").tolist() == [2]
    assert not left_anti["key"].isna().any()
    assert not right_anti["key"].isna().any()


if __name__ == "__main__":
    test_outer_merge_and_validation()
    test_anti_joins_treat_missing_keys_as_matches()
    print("pandas 3.0.5 top-level merge contract passed")
