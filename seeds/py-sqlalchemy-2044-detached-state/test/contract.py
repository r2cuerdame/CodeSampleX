from sqlalchemy import ForeignKey, String, create_engine, select
from sqlalchemy.orm import DeclarativeBase, Mapped, Session, mapped_column, relationship, selectinload
from sqlalchemy.orm.exc import DetachedInstanceError


class Base(DeclarativeBase):
    pass


class User(Base):
    __tablename__ = "users"

    id: Mapped[int] = mapped_column(primary_key=True)
    name: Mapped[str] = mapped_column(String(50))
    addresses: Mapped[list["Address"]] = relationship(back_populates="user")


class Address(Base):
    __tablename__ = "addresses"

    id: Mapped[int] = mapped_column(primary_key=True)
    email: Mapped[str] = mapped_column(String(100))
    user_id: Mapped[int] = mapped_column(ForeignKey("users.id"))
    user: Mapped[User] = relationship(back_populates="addresses")


def assert_detached(load):
    try:
        load()
    except DetachedInstanceError as error:
        assert "not bound to a Session" in str(error)
        return
    raise AssertionError("expected DetachedInstanceError")


engine = create_engine("sqlite+pysqlite:///:memory:")
Base.metadata.create_all(engine)
with Session(engine) as session:
    session.add(User(name="Ada", addresses=[Address(email="ada@example.com")]))
    session.commit()


with Session(engine) as session:
    default_user = session.scalar(select(User))
    assert default_user is not None
    assert default_user.name == "Ada"
    assert [address.email for address in default_user.addresses] == ["ada@example.com"]
    session.commit()
    assert "name" not in default_user.__dict__
    assert "addresses" not in default_user.__dict__
session.close()

assert_detached(lambda: default_user.name)
assert_detached(lambda: default_user.addresses)


with Session(engine) as session:
    loaded_scalar = session.scalar(select(User))
    assert loaded_scalar is not None
    assert loaded_scalar.name == "Ada"
session.close()

assert loaded_scalar.name == "Ada"
assert_detached(lambda: loaded_scalar.addresses)


with Session(engine, expire_on_commit=False) as session:
    no_expire_user = session.scalar(select(User))
    assert no_expire_user is not None
    session.commit()
    assert no_expire_user.name == "Ada"
session.close()

assert no_expire_user.name == "Ada"
assert_detached(lambda: no_expire_user.addresses)


with Session(engine) as session:
    eager_user = session.scalar(select(User).options(selectinload(User.addresses)))
    assert eager_user is not None
    assert [address.email for address in eager_user.addresses] == ["ada@example.com"]
session.close()

assert eager_user.name == "Ada"
assert [address.email for address in eager_user.addresses] == ["ada@example.com"]


with Session(engine) as session:
    refreshed_user = session.scalar(select(User))
    assert refreshed_user is not None
    session.refresh(refreshed_user, attribute_names=["addresses"])
    assert "addresses" in refreshed_user.__dict__
session.close()

assert [address.email for address in refreshed_user.addresses] == ["ada@example.com"]

print("CONTRACT PASS: SQLAlchemy 2.0.44 commit expiration and detached loading are measured")
