"""Picking an object-proxy base class in wrapt 2.x.

wrapt sat on 1.x from 2013 until August 2025, so essentially every example in
circulation says "subclass wrapt.ObjectProxy". In 2.x that name still exists and
still imports, which is exactly why the change is easy to miss: ObjectProxy is
no longer the base class. The base is BaseObjectProxy, and ObjectProxy is a thin
backwards-compatibility subclass whose only added special method is __iter__.

Which class you pick decides whether iteration works, and the usual duck-typing
checks cannot tell you which one you have: isinstance() and hasattr() both
answer "iterable" for the two combinations where iteration actually fails.
"""

import collections.abc as abc

import wrapt

# The C-level base type in 2.x (wrapt.BaseObjectProxy, module "_wrappers").
# wrapt's own ObjectProxy docstring points new subclasses here: "If you don't
# need backward compatibility for __iter__() support then it is preferable to
# use BaseObjectProxy directly."
BASE = wrapt.BaseObjectProxy

# The 1.x-compatible name (module "wrapt.proxies"). It is a SUBCLASS of BASE,
# not an alias for it, and the only special method it adds is __iter__.
COMPAT = wrapt.ObjectProxy

# Builds a bespoke class per instance carrying the dunder methods the wrapped
# object actually has. Correct, but it pays a new class for every proxy created.
AUTO = wrapt.AutoObjectProxy


def can_iterate(proxy):
    """Whether the proxy can ACTUALLY be consumed as an iterable.

    Python resolves __iter__ on the type and never on the instance, so a proxy
    iterates only when its own class defines __iter__. wrapt's __getattr__
    forwarding does not help here. The consequence that catches people out:
    list(BaseObjectProxy([1, 2, 3])) raises TypeError even though the wrapped
    object is a list.
    """
    try:
        list(proxy)
        return True
    except TypeError:
        return False


def looks_iterable(proxy):
    """The cheap pre-check an agent reaches for -- and it over-reports.

    isinstance() against an ABC consults the proxy's __class__, which is
    forwarded to the WRAPPED object, and also the proxy's real type; iteration
    is decided by the real type alone. So "if isinstance(p, Iterable): list(p)"
    is the obvious wrong version, and it is wrong the SAME way both times --
    a false positive, for two different reasons. BASE around a list says True
    because the wrapped list is iterable; COMPAT around a non-iterable says
    True because the proxy type defines __iter__. Both then raise on list(p).
    """
    return isinstance(proxy, abc.Iterable)
