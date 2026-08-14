"""SQLAlchemy 1.4 -> 2.0, in the two places it actually costs time.

The first is quiet. session.execute(select(User)) does not give you User
objects — it gives you Row tuples with a User inside each one, exactly like
a SQL driver would. Code written against Query gets a list of one-element
tuples, and the failure surfaces later as an attribute error on a tuple.
.scalars() is the unwrapping step, and it is the whole difference between
the two idioms.

The second is loud but easy to misread. A bare SQL string is no longer
executable, and the exception depends on where you passed it: a Connection
says ObjectNotExecutableError, a Session says ArgumentError. Both mean
wrap it in text().

What did NOT change, measured rather than assumed: Query still works, and
session.query(User).get(1) warns instead of failing. So a 2.0 upgrade can
run green while none of the code is 2.0 yet.
"""

import sqlalchemy as sa
from sqlalchemy import select
from sqlalchemy.orm import DeclarativeBase, Mapped, Session, mapped_column


class Base(DeclarativeBase):
    pass


class User(Base):
    __tablename__ = "users"

    id: Mapped[int] = mapped_column(primary_key=True)
    name: Mapped[str]


def seeded_engine() -> sa.Engine:
    engine = sa.create_engine("sqlite+pysqlite:///:memory:")
    Base.metadata.create_all(engine)
    with Session(engine) as session:
        session.add_all([User(id=1, name="ada"), User(id=2, name="linus")])
        session.commit()
    return engine


def rows(session: Session) -> list:
    """The 2.0 call that surprises people: Row tuples, not entities."""
    return session.execute(select(User)).all()


def entities(session: Session) -> list[User]:
    """The same query, unwrapped. This is what Query used to return."""
    return session.scalars(select(User)).all()
