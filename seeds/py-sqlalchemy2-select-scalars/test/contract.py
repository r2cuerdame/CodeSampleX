import sys
import warnings
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

import sqlalchemy as sa
from sqlalchemy import select
from sqlalchemy.orm import Session

from src.store import User, entities, rows, seeded_engine

engine = seeded_engine()

with Session(engine) as session:
    # execute() returns Row tuples that WRAP the entity.
    first = rows(session)[0]
    assert type(first).__name__ == "Row", type(first)
    assert isinstance(first[0], User), first
    assert not isinstance(first, User)

    # scalars() unwraps them. This is the line most migrations are missing.
    people = entities(session)
    assert [u.name for u in people] == ["ada", "linus"], people

    # The 1.x Query API was not removed, which is why an upgrade can look
    # finished before it has started.
    assert [u.name for u in session.query(User).all()] == ["ada", "linus"]

    with warnings.catch_warnings(record=True) as caught:
        warnings.simplefilter("always")
        session.query(User).get(1)
    assert [type(c.message).__name__ for c in caught] == ["LegacyAPIWarning"], caught

    # session.get is the replacement, and it returns the entity directly.
    assert session.get(User, 1).name == "ada"
    assert session.scalars(select(User).where(User.id == 99)).one_or_none() is None

# Connectionless execution really is gone: this one is an AttributeError,
# not a deprecation.
assert not hasattr(engine, "execute")

# A bare SQL string is not executable, and the two entry points disagree
# about which exception says so.
with engine.connect() as conn:
    try:
        conn.execute("select 1")
        raise AssertionError("a bare string should not be executable")
    except sa.exc.ObjectNotExecutableError:
        pass
    assert list(conn.execute(sa.text("select 1"))) == [(1,)]

with Session(engine) as session:
    try:
        session.execute("select 1")
        raise AssertionError("a bare string should not be executable")
    except sa.exc.ArgumentError as exc:
        assert "text(" in str(exc), exc

print("contract ok:", sa.__version__)
