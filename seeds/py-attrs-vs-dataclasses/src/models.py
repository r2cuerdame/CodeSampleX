"""attrs 26 next to the standard library dataclasses, on Python 3.12.

"Do I still need attrs?" has an answer that is smaller than the folklore and
sharper. dataclasses have absorbed most of the shape: keyword-only fields,
slots, frozen, replace. What they have not absorbed is the part that runs
code on your data. attrs has validators and converters, wired into __init__
AND into assignment; dataclasses have neither, and their only hook is a
__post_init__ you write by hand. That hook runs at __init__ and again when
replace() goes back through __init__, and never on assignment, which is the
one moment a converter would have earned its keep.

The correction that matters before any of this: attrs does not type-check
annotations either. `n: int` on an @define class is documentation, exactly as
it is on a dataclass, and `Unchecked("nope")` below stores the string. attrs
gives you a place to put the check and runs it at the two moments that
matter. Enforcing the annotation itself is what pydantic is for. If the
reason you are reaching for attrs is "so the types are real", neither of
these libraries is the answer.

The differences that bite during a migration, all measured on this image:

  - Slots. @define is slots=True, so an undeclared attribute is an
    AttributeError instead of a typo that persists. @dataclass is not, and
    @dataclass(slots=True) since 3.10 is — but not on the same terms. attrs
    puts __weakref__ in __slots__ and the stdlib does not unless you also
    pass weakref_slot=True, so a slotted dataclass cannot be the target of a
    weakref.ref(). See also the closure note below.
  - Namespaces. The move went one way only. `attr` gained every modern name,
    as the same object — attr.define is attrs.define — while `attrs` never
    gained the old ones, so a half-migrated file keeps working and only the
    reverse reach fails. What does not survive is the assumption that a
    shared name means a shared object: attr.asdict and
    attrs.asdict are two different functions with two different signatures,
    and attr.exceptions is not attrs.exceptions even though the exception
    classes inside them are identical.
  - Assignment. @define installs on_setattr = pipe(convert, validate). The
    converter that normalised __init__ runs again on `obj.port = "1234"`.
    Nothing in dataclasses does, and neither does the old @attr.s.
  - Frozen errors are unrelated classes. attrs raises the one whose module
    is attr.exceptions — the OLD namespace, even when imported from
    attrs.exceptions — and dataclasses raises dataclasses.FrozenInstanceError.
    Neither catches the other. Both derive from AttributeError, which is the
    narrowest single clause that covers a codebase holding both.
  - Private fields. attrs strips the leading underscore to build the
    __init__ parameter: field `_token` is `Session(token=...)`, and
    attrs.evolve keys on that parameter name too. dataclasses keep `_token`
    verbatim in both __init__ and replace(). This is the migration bug that
    survives a find-and-replace, because both spellings look right.
  - Ordering. Same rule, different exception: a mandatory field after a
    defaulted one is a ValueError from attrs and a TypeError from
    dataclasses, both at class-definition time. kw_only=True lifts it in
    both.

Measured, and it refuted the hypothesis this sample started with: on Python
3.12, @dataclass(slots=True) breaks zero-argument super(). Both decorators
have to build a NEW class object to add __slots__ — the returned class is not
the one you wrote — but attrs repairs the __class__ closure cell that methods
capture and the stdlib does not, so a method calling super() in a slotted
dataclass raises "TypeError: super(type, obj): obj must be an instance or
subtype of type". attrs' slotted subclass with the identical body works. If
you are adding slots=True to dataclasses that inherit from anything, that is
the failure to expect, and passing the class explicitly to super() is the
workaround.

One thing validators are not: a boundary. attrs.validators.disabled() turns
every validator in the process off, so a validator states an invariant for
the code that constructs the object, not a guarantee for code that receives
it.
"""

import dataclasses
from dataclasses import dataclass

import attr
import attrs
from attrs import define, field, frozen, validators


@define
class Listener:
    """The whole attrs case in one class: check at init, coerce on assignment.

    converter runs before validator, which is why an int-annotated field with
    validators.instance_of(int) still accepts "9000" — by the time the
    validator sees it, it is an int. Reverse that order and the pair is
    useless together.

    The two validators raise different builtin types: instance_of raises
    TypeError, the comparison validators (gt/ge/lt/le) raise ValueError. An
    `except ValueError` around construction catches half of them.
    """

    host: str = field(validator=validators.instance_of(str))
    port: int = field(
        default=8080,
        converter=int,
        validator=[validators.instance_of(int), validators.gt(0)],
    )


@dataclass
class DataclassListener:
    """The same fields with no place to put a check. Accepts anything."""

    host: str
    port: int = 8080


@define
class Unchecked:
    """@define with nothing to run: the annotation alone enforces no more here
    than it does on a dataclass, and this stores a str in an int field."""

    n: int = 0


@define(on_setattr=attrs.setters.NO_OP)
class QuietListener:
    """on_setattr is the switch people flip for speed, or to allow a plain
    __setattr__, and it silently takes the converter with it. __init__ still
    converts; assignment stops. Same class, two different notions of what
    `port` contains, depending on how it got there."""

    port: int = field(default=8080, converter=int)


@frozen
class FrozenListener:
    host: str = field(validator=validators.instance_of(str))


@dataclass(frozen=True)
class FrozenDataclassListener:
    host: str


@define
class Session:
    """A private field. The init parameter is `token`, not `_token`."""

    _token: str = field(validator=validators.instance_of(str))
    label: str = "anon"


@dataclass
class DataclassSession:
    """The same fields, where the init parameter really is `_token`."""

    _token: str
    label: str = "anon"


@define
class AttrsDerived:
    a: int = 0
    b: int = field(default=7, init=False)


@dataclass
class DataclassDerived:
    a: int = 0
    b: int = dataclasses.field(default=7, init=False)


@define(kw_only=True)
class KwListener:
    """kw_only is the escape hatch from the ordering rule, in both libraries:
    a mandatory field after a defaulted one is legal once nothing is
    positional."""

    host: str = "localhost"
    port: int


@dataclass(kw_only=True)
class KwDataclassListener:
    host: str = "localhost"
    port: int


class Base:
    def describe(self) -> str:
        return "base"


class _Mark:
    """Undecorated on purpose, so both decorators below can be applied to a
    class we still hold a reference to."""

    n: int = 0


SlottedDataclassMark = dataclass(slots=True)(_Mark)


class _OtherMark:
    n: int = 0


SlottedAttrsMark = define(_OtherMark)


def slotted_dataclass_subclass():
    """Defined inside a function so the methods carry a __class__ cell, which
    is what zero-argument super() reads."""

    @dataclass(slots=True)
    class Child(Base):
        n: int = 0

        def describe(self) -> str:
            return "child of " + super().describe()

    return Child


def slotted_attrs_subclass():
    @define
    class Child(Base):
        n: int = 0

        def describe(self) -> str:
            return "child of " + super().describe()

    return Child


def slotted_dataclass_explicit_super():
    """The workaround for the stdlib case: name the class, so the method never
    reads the __class__ cell that the rebuilt class left stale."""

    @dataclass(slots=True)
    class Child(Base):
        n: int = 0

        def describe(self) -> str:
            return "child of " + super(Child, self).describe()

    return Child


POST_INIT_CALLS = []


@dataclass
class PostInitDataclass:
    """The hook people reach for when told dataclasses cannot validate.

    It is not the same offer. __post_init__ fires at __init__ and again from
    dataclasses.replace(), because replace() constructs a new instance — but
    an ordinary assignment never touches it, so a dataclass that is correct at
    construction can stop being correct one line later.
    """

    port: int = 8080

    def __post_init__(self) -> None:
        POST_INIT_CALLS.append(self.port)


@attr.s(auto_attribs=True)
class LegacyListener:
    """The class you already have, in the pre-2020 namespace, still supported.

    @attr.s is not @define with an older spelling. It leaves on_setattr unset
    and slots off, so this class behaves like a dataclass on both counts:
    the converter runs at __init__ and never again, and an undeclared
    attribute lands in an ordinary __dict__.

    That is the actual content of the migration. Rewriting @attr.s to @define
    is not cosmetic — it turns on slots and starts running converters and
    validators on every assignment, which is where a codebase that was quietly
    assigning strings to int fields finds out.
    """

    port: int = attr.ib(default=8080, converter=int)


def attrs_default_before_mandatory():
    """Return the exception attrs raises at class definition, or None."""
    try:

        @define
        class Bad:
            a: int = 0
            b: int

    except Exception as exc:
        return exc
    return None


def dataclass_default_before_mandatory():
    try:

        @dataclass
        class Bad:
            a: int = 0
            b: int

    except Exception as exc:
        return exc
    return None
