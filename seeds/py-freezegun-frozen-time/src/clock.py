"""An ordinary module that reads the clock, imported long before any freeze.

Nothing here knows a test exists. The names are bound once, at import, the
way normal code binds them, and that is exactly what makes this the right
thing to freeze against: freeze_time has to reach back into modules that
were already loaded when the test started.

It does that by walking sys.modules and rebinding every module attribute
whose identity IS the real datetime class or one of the real time
functions. So ``datetime``, ``time`` and ``monotonic`` below are swapped for
the duration of the freeze, and every function that reads them through the
module global sees frozen time. That is the difference between freezegun
and a hand-rolled patch: monkeypatching ``datetime.datetime`` in the test
module does not touch this module's already-bound name.

Three shapes here are outside what that sweep can reach, and they are here
on purpose because they are what people actually write:

  DEFAULT_START   a value, not a reference. The sweep rebinds names that
                  are the class; it cannot recompute a datetime that was
                  already derived from it.
  _captured_now   a bound classmethod pulled off the real class at import.
                  Its identity is not the class's identity, so the sweep
                  walks past it and it keeps calling the real clock.
  AuditStamp      a subclass. Its MRO still names the real datetime, and
                  freeze_time does not rewrite MROs.

test/contract.py measures what each of them does under a freeze. One of
the three does not behave the way the shape suggests.
"""

from datetime import datetime, timezone
from time import monotonic, time

# Computed once, at import, from whatever the real clock said at the moment
# this module was first loaded. A test that freezes time later cannot change
# it, so every duration measured against it is wrong by the distance between
# the freeze and the real clock.
DEFAULT_START = datetime.now()

# The same mistake in a different shape, and a common one in code that wants
# a cheap alias or an injectable clock.
_captured_now = datetime.now


class AuditStamp(datetime):
    """A datetime subclass, defined at import time.

    Domain code subclasses datetime to attach a repr, a serialiser or a
    validator. The class inherits the real classmethods, and freeze_time
    leaves the MRO alone, so the inherited methods are the real ones. The
    contract shows that this does not produce one consistent answer.
    """


def stamp():
    """Naive local time, the call almost every codebase makes."""
    return datetime.now()


def utc_stamp():
    """The aware replacement people move to when utcnow is deprecated."""
    return datetime.now(timezone.utc)


def utc_stamp_legacy():
    """The deprecated call the move above is meant to replace."""
    return datetime.utcnow()


def wall_clock():
    """Seconds since the epoch, through a name imported at module level."""
    return time()


def monotonic_reading():
    """A reading from the clock that is documented never to go backwards."""
    return monotonic()


def cached_stamp():
    return _captured_now()


def elapsed_since_start():
    """Seconds this module has been loaded, as its author intended it."""
    return (datetime.now() - DEFAULT_START).total_seconds()
