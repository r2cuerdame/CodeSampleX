import inspect
import os
import sys
import types

import collections.abc as abc

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
import wrapt

from src.compat import AUTO, BASE, COMPAT, can_iterate, looks_iterable


class Bare:
    """A plain object with no dunder methods beyond the defaults."""

    def __init__(self):
        self.value = 1


# ObjectProxy is NOT the base class in 2.x, and not an alias for it either: it
# is a subclass of the real base, and so is AutoObjectProxy.
assert wrapt.ObjectProxy is not wrapt.BaseObjectProxy
assert issubclass(wrapt.ObjectProxy, wrapt.BaseObjectProxy)
assert issubclass(wrapt.AutoObjectProxy, wrapt.BaseObjectProxy)

# The base class does not DEFINE __iter__ on the type, and that is where Python
# looks for it, so a proxied list will not iterate: the obvious "list(proxy)"
# raises TypeError even though the wrapped object is a list. Attribute
# forwarding is irrelevant here -- this is the whole reason ObjectProxy stayed.
base_list = BASE([1, 2, 3])
assert can_iterate(base_list) is False, "BaseObjectProxy unexpectedly iterated"
# ...while every non-dunder-dispatched check insists it is fine.
assert looks_iterable(base_list) is True
assert hasattr(base_list, "__iter__") is True
assert base_list.__class__ is list
assert len(base_list) == 3 and base_list[0] == 1

# Calling the forwarded attribute by hand does work; only the iter() protocol,
# which looks the method up on the type, fails.
assert list(base_list.__iter__()) == [1, 2, 3]

# The compat class iterates, because __iter__ is precisely what it adds. That
# is why 1.x-shaped code -- "class MyProxy(wrapt.ObjectProxy)" -- keeps running
# unchanged, and why nothing announces that a different base is now intended.
assert can_iterate(COMPAT([1, 2, 3])) is True


class LegacyProxy(COMPAT):
    """Exactly what twelve years of wrapt 1.x examples tell you to write."""


assert list(LegacyProxy([1, 2, 3])) == [1, 2, 3]

# But it adds __iter__ unconditionally, so it claims a NON-iterable is iterable.
compat_bare = COMPAT(Bare())
assert looks_iterable(compat_bare) is True, "expected the false positive"
assert can_iterate(compat_bare) is False
# The base class gets this one right: the false positive changes sides with the
# base class you picked, so neither answer can be trusted on its own.
assert looks_iterable(BASE(Bare())) is False

# AutoObjectProxy is the only one that agrees with reality in both directions.
assert can_iterate(AUTO([1, 2, 3])) is True
assert looks_iterable(AUTO(Bare())) is False
assert isinstance(AUTO([1, 2, 3]), abc.Iterable) is True

# The cost is a freshly built class for every instance, so identity checks on
# type() and any per-class caching keyed on it stop working.
assert type(AUTO([1, 2])) is not type(AUTO([3, 4]))
assert type(COMPAT([1, 2])) is type(COMPAT([3, 4]))

# wrapt.signature is a MODULE, not a callable. "wrapt.signature(fn)" reads like
# a proxy-aware inspect.signature and fails at runtime instead; the exported
# helper is wrapt.with_signature, and plain inspect.signature already sees
# through a wrapt decorator.
assert isinstance(wrapt.signature, types.ModuleType)
try:
    wrapt.signature(lambda a: a)
    raise AssertionError("expected TypeError: module object is not callable")
except TypeError:
    pass
assert callable(wrapt.with_signature)


@wrapt.decorator
def passthrough(wrapped, instance, args, kwargs):
    return wrapped(*args, **kwargs)


@passthrough
def add(a, b=2):
    return a + b


# The reason you do not need a proxy-aware signature helper: the decorated
# function still advertises the ORIGINAL signature, not (*args, **kwargs).
assert str(inspect.signature(add)) == "(a, b=2)"
assert add(1) == 3

# Likewise wrapt.function_wrapper is not another name for wrapt.decorator: it
# takes the wrapper and nothing else, so the enabled/adapter/proxy options exist
# on decorator only.
assert wrapt.function_wrapper is not wrapt.decorator
assert list(inspect.signature(wrapt.function_wrapper).parameters) == ["wrapper"]
assert list(inspect.signature(wrapt.decorator).parameters) == [
    "wrapper",
    "enabled",
    "adapter",
    "proxy",
]

# Subclass state still needs the _self_ prefix on the 2.x base: any other name
# is written through onto the wrapped object rather than kept on the proxy.
class Counter(BASE):
    def __init__(self, wrapped):
        super().__init__(wrapped)
        self._self_hits = 0

    def bump(self):
        self._self_hits += 1
        return self._self_hits


counter = Counter(Bare())
counter.bump()
assert counter.bump() == 2
assert not hasattr(counter.__wrapped__, "_self_hits")


class Leaky(BASE):
    def __init__(self, wrapped):
        super().__init__(wrapped)
        self.hits = 0


leaky = Leaky(Bare())
assert leaky.__wrapped__.hits == 0, "plain attribute should leak to the wrapped object"

print("CONTRACT PASS: wrapt 2.x proxy bases differ on __iter__ and isinstance lies")
