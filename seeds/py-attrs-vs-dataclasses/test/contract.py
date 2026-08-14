import dataclasses
import importlib.metadata
import inspect
import subprocess
import sys
import weakref
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

import attr
import attrs

from src.models import (
    POST_INIT_CALLS,
    AttrsDerived,
    Base,
    DataclassDerived,
    DataclassListener,
    DataclassSession,
    FrozenDataclassListener,
    FrozenListener,
    KwDataclassListener,
    KwListener,
    LegacyListener,
    Listener,
    PostInitDataclass,
    QuietListener,
    Session,
    SlottedAttrsMark,
    SlottedDataclassMark,
    Unchecked,
    _Mark,
    _OtherMark,
    attrs_default_before_mandatory,
    dataclass_default_before_mandatory,
    slotted_attrs_subclass,
    slotted_dataclass_explicit_super,
    slotted_dataclass_subclass,
)

# Neither library enforces the annotation. State it first, because every
# claim below is only interesting once this one is out of the way.
assert DataclassListener(host=123, port="8080").port == "8080"
assert Unchecked("nope").n == "nope"

# What attrs adds is a place to put the check that __init__ actually runs.
# The two builtin exception types differ: instance_of raises TypeError,
# the comparison validators raise ValueError.
try:
    Listener(123)
    raise AssertionError("instance_of should reject an int host")
except TypeError as exc:
    assert "must be <class 'str'>" in str(exc)

try:
    Listener("h", 0)
    raise AssertionError("gt(0) should reject 0")
except ValueError as exc:
    assert "must be > 0" in str(exc)

# The whole comparison family behaves that way, called directly with the
# Attribute they would receive from __init__.
port_attribute = attrs.fields(Listener)[1]
for name, bad in (("ge", 0), ("lt", 100), ("le", 100)):
    validator = getattr(attrs.validators, name)(3)
    try:
        validator(None, port_attribute, bad)
        raise AssertionError(name + " should reject " + str(bad))
    except ValueError:
        pass

# converter before validator, which is the only order that lets converter=int
# and validators.instance_of(int) coexist on one field.
assert Listener("h", "9000").port == 9000
assert type(Listener("h", "9000").port) is int

# The difference dataclasses cannot reproduce at all: assignment. @define
# installs on_setattr = pipe(convert, validate), so both run again here.
listener = Listener("h")
listener.port = "1234"
assert listener.port == 1234
try:
    listener.port = 0
    raise AssertionError("the validator should run on assignment too")
except ValueError:
    pass

plain = DataclassListener("h")
plain.port = "1234"
assert plain.port == "1234"

# __post_init__ is the hook people offer as the answer to that, and it is not
# one: it fires at __init__ and again through replace(), because replace()
# builds a new instance, but assignment goes nowhere near it.
POST_INIT_CALLS.clear()
hooked = PostInitDataclass(1)
hooked.port = "not an int"
assert POST_INIT_CALLS == [1]
dataclasses.replace(hooked, port=2)
assert POST_INIT_CALLS == [1, 2]
assert hooked.port == "not an int"

# on_setattr=NO_OP is a real hole rather than a tuning knob: __init__ still
# converts, assignment does not, so the same field holds either type.
quiet = QuietListener("5")
assert quiet.port == 5
quiet.port = "5"
assert quiet.port == "5"

# And validators are not a boundary. Any caller can switch all of them off,
# for every class in the process at once and not just the one being called.
with attrs.validators.disabled():
    assert Listener(123).host == 123
    assert Session(token=123)._token == 123
assert attrs.validators.get_disabled() is False

# Slots. @define is slots=True, so a typo is an error instead of a new
# attribute; the dataclass takes it.
try:
    listener.hsot = "typo"
    raise AssertionError("@define should be slotted")
except AttributeError as exc:
    assert "hsot" in str(exc)
plain.hsot = "typo"
assert plain.hsot == "typo"
assert not hasattr(listener, "__dict__")
assert hasattr(plain, "__dict__")

# dataclasses got slots in 3.10, and it is opt-in. In both libraries adding
# slots means the decorator returns a NEW class object — the name you
# decorated is rebound, and any reference taken before it is stale.
assert SlottedDataclassMark is not _Mark
assert SlottedAttrsMark is not _OtherMark
assert SlottedDataclassMark.__slots__ == ("n",)

# The two slot layouts are not the same layout. attrs reserves __weakref__ and
# the stdlib does not, so anything that keeps weak references to your objects
# — caches, registries, observers — stops working the day slots=True lands,
# unless weakref_slot=True goes with it.
assert SlottedAttrsMark.__slots__ == ("n", "__weakref__")
attrs_mark = SlottedAttrsMark()
assert weakref.ref(attrs_mark)() is attrs_mark
dataclass_mark = SlottedDataclassMark()
try:
    weakref.ref(dataclass_mark)
    raise AssertionError("a slotted dataclass has no __weakref__ by default")
except TypeError as exc:
    assert "cannot create weak reference" in str(exc)

# Measured, and it contradicts the hypothesis this sample started from:
# CPython 3.12 does not repair the __class__ closure cell when it rebuilds
# the class, so zero-argument super() in a slotted dataclass fails at call
# time — not at definition, which is what makes it expensive to find. attrs
# repairs the cell, and the identical class body works.
slotted_dataclass_child = slotted_dataclass_subclass()
assert issubclass(slotted_dataclass_child, Base)
try:
    slotted_dataclass_child().describe()
    raise AssertionError("expected the stdlib slots/super() failure on 3.12")
except TypeError as exc:
    assert "obj must be an instance or subtype of type" in str(exc)
assert slotted_attrs_subclass()().describe() == "child of base"

# And this is the mechanism, not a guess about it. Both decorators return a
# class the method's __class__ cell was never told about; attrs rebinds the
# cell to the class it returns and the stdlib leaves it pointing at the
# discarded original, which is precisely what zero-argument super() reads.
dataclass_cell = slotted_dataclass_child.describe.__closure__[0].cell_contents
assert dataclass_cell is not slotted_dataclass_child
attrs_child = slotted_attrs_subclass()
assert attrs_child.describe.__closure__[0].cell_contents is attrs_child

# Naming the class explicitly is the workaround, because super(Child, self)
# never reads the cell.
assert slotted_dataclass_explicit_super()().describe() == "child of base"

# The measurement above is version-specific by nature. Pin the version it was
# taken on, so an image where CPython has fixed this fails here loudly instead
# of leaving a stale claim in the sample.
assert sys.version_info[:2] == (3, 12)

# Frozen. The two errors are unrelated classes from different modules, and
# attrs' one reports the OLD namespace as its module even though it is
# imported from attrs.exceptions.
assert attrs.exceptions.FrozenInstanceError.__module__ == "attr.exceptions"
assert dataclasses.FrozenInstanceError.__module__ == "dataclasses"
assert attrs.exceptions.FrozenInstanceError is attr.exceptions.FrozenInstanceError
assert attrs.exceptions.FrozenInstanceError is not dataclasses.FrozenInstanceError
assert not issubclass(
    attrs.exceptions.FrozenInstanceError, dataclasses.FrozenInstanceError
)
assert not issubclass(
    dataclasses.FrozenInstanceError, attrs.exceptions.FrozenInstanceError
)

# So the obvious except clause silently misses half the codebase.
frozen_attrs = FrozenListener("h")
try:
    try:
        frozen_attrs.host = "other"
        raise AssertionError("frozen should refuse assignment")
    except dataclasses.FrozenInstanceError:
        raise AssertionError("dataclasses.FrozenInstanceError caught attrs'")
except attrs.exceptions.FrozenInstanceError:
    pass

frozen_dc = FrozenDataclassListener("h")
try:
    frozen_dc.host = "other"
    raise AssertionError("frozen dataclass should refuse assignment")
except attrs.exceptions.FrozenInstanceError:
    raise AssertionError("attrs' FrozenInstanceError caught the dataclass one")
except dataclasses.FrozenInstanceError:
    pass

# Both derive from AttributeError, the narrowest single clause covering both.
for error in (attrs.exceptions.FrozenInstanceError, dataclasses.FrozenInstanceError):
    assert issubclass(error, AttributeError)

# Field ordering: same rule, different exception type, both at class
# definition time rather than at first use.
attrs_order_error = attrs_default_before_mandatory()
dataclass_order_error = dataclass_default_before_mandatory()
assert type(attrs_order_error) is ValueError
assert "No mandatory attributes allowed after" in str(attrs_order_error)
assert type(dataclass_order_error) is TypeError
assert "non-default argument 'b' follows default argument" in str(
    dataclass_order_error
)

# kw_only=True lifts the rule in both, and dataclasses have had it since 3.10.
assert KwListener(port=1).host == "localhost"
assert KwDataclassListener(port=1).host == "localhost"

# Private fields. attrs strips the underscore to name the __init__ parameter;
# dataclasses keep it. Both spellings read as correct at a call site, which is
# what makes this the migration bug that survives review.
assert Session(token="t")._token == "t"
assert attrs.fields(Session)[0].name == "_token"
assert attrs.fields(Session)[0].alias == "token"
try:
    Session(_token="t")
    raise AssertionError("attrs renames the parameter to token")
except TypeError as exc:
    assert "unexpected keyword argument '_token'" in str(exc)

assert DataclassSession(_token="t")._token == "t"
try:
    DataclassSession(token="t")
    raise AssertionError("dataclasses keep the underscore")
except TypeError as exc:
    assert "unexpected keyword argument 'token'" in str(exc)

# evolve/replace inherit that difference, because both go back through
# __init__ — which also means attrs re-runs the validator and replace has
# nothing of its own to re-run.
session = Session(token="t")
assert attrs.evolve(session, token="new")._token == "new"
try:
    attrs.evolve(session, _token="new")
    raise AssertionError("evolve keys on the init parameter name")
except TypeError:
    pass
try:
    attrs.evolve(session, token=123)
    raise AssertionError("evolve should re-run the validator")
except TypeError as exc:
    assert "must be <class 'str'>" in str(exc)

dc_session = DataclassSession(_token="t")
assert dataclasses.replace(dc_session, _token=123)._token == 123

# init=False fields are refused by both, with a message worth reading in only
# one of them: replace names the problem, evolve reports an unknown keyword.
try:
    dataclasses.replace(DataclassDerived(1), b=9)
    raise AssertionError("replace should refuse an init=False field")
except ValueError as exc:
    assert "cannot be specified with replace()" in str(exc)
try:
    attrs.evolve(AttrsDerived(1), b=9)
    raise AssertionError("evolve should refuse an init=False field")
except TypeError as exc:
    assert "unexpected keyword argument 'b'" in str(exc)

# The two libraries do not recognise each other. Anything dispatching on
# dataclasses.is_dataclass — serializers, adapters, editors — sees an attrs
# class as a plain object.
assert dataclasses.is_dataclass(Listener) is False
assert attrs.has(DataclassListener) is False
try:
    dataclasses.fields(Listener)
    raise AssertionError("attrs classes are not dataclasses")
except TypeError:
    pass
try:
    attrs.fields(DataclassListener)
    raise AssertionError("dataclasses are not attrs classes")
except attrs.exceptions.NotAnAttrsClassError:
    pass

# The migration question underneath: the old namespace still imports, and it
# is the same package seen twice. Checked in a fresh interpreter, because by
# this line `attr` is already in sys.modules and a re-import would execute
# nothing — the usual way this gets "verified" and proves nothing. -W error
# turns every warning into a failure, so rc 0 means not one was raised.
probe = subprocess.run(
    [sys.executable, "-W", "error", "-c", "import attr"],
    capture_output=True,
    text=True,
)
assert probe.returncode == 0, probe.stderr
assert attr.s is attr.attrs and attr.ib is attr.attrib
assert attr.__version__ == importlib.metadata.version("attrs")

# The move went one way only, which is the opposite of what "attr is the old
# name for attrs" suggests. Every modern name was added to `attr` as the same
# object, so a half-migrated file works; none of the old ones were ever added
# to `attrs`, so only the reverse reach fails.
assert attr.define is attrs.define
assert attr.field is attrs.field and attr.frozen is attrs.frozen
assert attr.evolve is attrs.evolve and attr.fields is attrs.fields
assert not hasattr(attrs, "s") and not hasattr(attrs, "ib")
assert not hasattr(attrs, "attrib") and not hasattr(attrs, "attributes")

# But a shared name is not always a shared object, and that is where a
# find-and-replace across the two namespaces actually breaks: attrs.asdict is
# a different function with a keyword-only signature that dropped
# retain_collection_types, and the two exceptions modules are distinct objects
# that happen to expose the same classes.
assert attr.asdict is not attrs.asdict
assert "retain_collection_types" in inspect.signature(attr.asdict).parameters
assert "retain_collection_types" not in inspect.signature(attrs.asdict).parameters
assert attr.exceptions is not attrs.exceptions
assert attr.validators.instance_of is attrs.validators.instance_of

# And @attr.s is not @define with an older spelling. No slots, and the
# converter runs at __init__ only — the dataclass behaviour, which is exactly
# what changes the day someone rewrites it to @define.
legacy = LegacyListener("5")
assert legacy.port == 5
legacy.port = "5"
assert legacy.port == "5"
legacy.hsot = "typo"
assert hasattr(legacy, "__dict__")
# The difference is visible on the class: @define writes a __setattr__ to run
# the pipeline, @attr.s leaves the object's own.
assert LegacyListener.__setattr__ is object.__setattr__
assert Listener.__setattr__ is not object.__setattr__

print("contract ok:", importlib.metadata.version("attrs"))
