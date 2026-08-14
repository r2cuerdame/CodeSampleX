import sys
import time
import warnings
from datetime import date, datetime, timedelta, timezone
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

import freezegun
from freezegun import freeze_time

# Imported before anything is frozen. That ordering is the subject of this
# file: every question below is about code that was already loaded, with its
# names already bound, when the test started.
from src import clock

REAL_START = clock.DEFAULT_START

# The real classes have to be stashed somewhere freeze_time cannot reach,
# and a module global is not that place. The sweep rebinds every module
# attribute that IS the real datetime class -- including this file's, and
# including one literally named REAL_DATETIME -- so a comparison against a
# bare global would compare the fake class to itself and pass. Values inside
# a container are not module attributes, so this dict survives the freeze.
REAL = {"datetime": type(REAL_START), "date": type(REAL_START.date())}

# Left exposed on purpose, to measure the rewrite happening.
EXPOSED_DATETIME = type(REAL_START)

# The contract tells real time from frozen time by era, so it needs a
# machine clock on this side of 2020. If this fails, the host clock is wrong
# and nothing below means anything.
assert REAL_START > datetime(2020, 1, 1), REAL_START

FROZEN = datetime(1999, 12, 31, 23, 59, 58, 500000)
FROZEN_EPOCH = 946684798.5

# A real monotonic reading, taken outside the freeze, to measure against
# later. It is uptime, not epoch seconds, so the guard below only has to
# rule out a host that has been running for thirty years.
real_monotonic_before = time.monotonic()
assert real_monotonic_before < 946_000_000

with freeze_time("1999-12-31 23:59:58.5") as frozen:
    # --- one instant, read every way there is --------------------------
    assert datetime.now() == FROZEN
    assert datetime.utcnow() == FROZEN
    assert time.time() == FROZEN_EPOCH
    assert time.time_ns() == 946_684_798_500_000_000
    assert date.today() == date(1999, 12, 31)
    assert type(date.today()) is not REAL["date"]

    # The readings agree with each other, which is the part hand-rolled
    # patches get wrong: patching datetime.now alone leaves time.time()
    # running, and any code that mixes the two sees the clock jump.
    assert datetime.now().timestamp() == time.time()

    # And it does not tick. Two reads with work between them are the same
    # instant, so a test can assert an exact timestamp.
    first = datetime.now()
    sum(range(200_000))
    assert datetime.now() == first

    # --- code that imported datetime before the freeze -----------------
    # src.clock did `from datetime import datetime` at import. freeze_time
    # walked sys.modules and rebound that name, so the module is frozen
    # without being told, without an injected clock, without an argument
    # threaded through five call sites.
    assert clock.datetime is not REAL["datetime"]
    assert clock.datetime.__name__ == "FakeDatetime"
    assert clock.stamp() == FROZEN
    assert clock.wall_clock() == FROZEN_EPOCH
    assert clock.monotonic_reading() == FROZEN_EPOCH

    # This file is a module too, so the sweep rewrote its globals on the
    # way past. The handle stashed at the top for comparison is now the
    # fake class itself, which is how a test asserting `type(x) is not
    # REAL_DATETIME` ends up asserting nothing at all.
    assert EXPOSED_DATETIME is clock.datetime
    assert EXPOSED_DATETIME is not REAL["datetime"]

    # A bound classmethod captured at import is not a reference to the
    # class, so the sweep walks past it. `now = datetime.now` at module
    # level keeps calling the real clock from inside the freeze, and what
    # comes back is still a datetime, so nothing raises -- the freeze just
    # does not apply to that one call site.
    assert clock.cached_stamp() > datetime(2020, 1, 1)

    # --- the trap: a constant computed at import stays computed ---------
    # freeze_time rebinds names that ARE the real class. DEFAULT_START is
    # not a name for the class, it is a value already derived from it, so
    # it holds what the real clock said at import and keeps holding it.
    assert clock.DEFAULT_START == REAL_START
    assert clock.DEFAULT_START > datetime(2020, 1, 1)
    assert type(clock.DEFAULT_START) is REAL["datetime"]

    # The frozen reading really is a different class, and isinstance still
    # will not tell you which one you are holding: FakeDatetime's metaclass
    # answers instance checks against the real class, so both values are
    # datetimes and nothing anywhere looks wrong.
    assert type(datetime.now()) is not REAL["datetime"]
    assert isinstance(clock.DEFAULT_START, datetime)
    assert isinstance(datetime.now(), datetime)

    # The bug that produces: a duration measured against the stale constant
    # comes out negative by the whole distance between the freeze and the
    # real clock, more than twenty-five years here, silently and with no
    # exception raised.
    assert clock.elapsed_since_start() < -8e8

    # --- a datetime subclass defined at import --------------------------
    # AuditStamp inherits from the real class and freeze_time does not
    # rewrite MROs, so now() is the real implementation reading the real
    # system clock. The freeze does not apply to it at all.
    assert clock.AuditStamp.now() > datetime(2020, 1, 1)

    # Same class, same freeze, opposite answer. Measured, not assumed:
    # CPython builds today() out of cls.fromtimestamp(time.time()) while
    # now() reads the system clock in C, and freeze_time patches time.time.
    # So today() is frozen and now() is not, on one class, more than
    # twenty-five years apart, and the class that was added to make
    # timestamps consistent is the one that reports two different days.
    assert clock.AuditStamp.today() == FROZEN

    # --- tick() and move_to() move the frozen clock ---------------------
    assert type(frozen).__name__ == "FrozenDateTimeFactory"

    # A bare tick() is one second and returns the new time.
    assert frozen.tick() == FROZEN + timedelta(seconds=1)
    assert datetime.now() == FROZEN + timedelta(seconds=1)
    assert time.time() == FROZEN_EPOCH + 1

    # A timedelta or a plain number of seconds moves further, here across
    # the rollover the frozen instant was chosen to sit in front of.
    assert frozen.tick(timedelta(minutes=1, seconds=30)) == datetime(
        2000, 1, 1, 0, 1, 29, 500000)
    assert frozen.tick(2.5) == datetime(2000, 1, 1, 0, 1, 32)
    assert datetime.now() == datetime(2000, 1, 1, 0, 1, 32)

    # move_to takes an absolute time and returns None, so
    # `now = frozen.move_to(...)` binds None rather than the new time.
    assert frozen.move_to("2000-01-01 00:00:00") is None
    assert datetime.now() == datetime(2000, 1, 1)
    assert time.time() == 946684800.0
    assert clock.stamp() == datetime(2000, 1, 1)

    # And the proof of the mechanism above: today() followed move_to, since
    # patched time.time is the only clock it shares with the freeze, while
    # now() on the same class is still reading the real one.
    assert clock.AuditStamp.today() == datetime(2000, 1, 1)
    assert clock.AuditStamp.now() > datetime(2020, 1, 1)

    # --- monotonic is handled, and that is the second trap --------------
    # freeze_time patches time.monotonic and time.perf_counter to the same
    # frozen wall clock, so they equal time.time() exactly. Real monotonic
    # clocks share no origin with the epoch, so a duration measured across
    # the boundary of the freeze is about thirty years long.
    assert time.monotonic() == time.time()
    assert time.perf_counter() == time.time()
    assert time.monotonic() - real_monotonic_before > 946_000_000

    # Moving the clock back moves "monotonic" back with it. Code holding
    # `deadline = time.monotonic() + timeout` never reaches its deadline,
    # and a retry loop written that way hangs instead of failing.
    monotonic_before_move = time.monotonic()
    frozen.move_to("1999-01-01")
    assert time.monotonic() < monotonic_before_move

    # --- the mirror image of the DEFAULT_START trap ---------------------
    # A module imported for the first time from inside a frozen block
    # computes its constant from the frozen clock, and keeps it for the
    # rest of the process. Whichever test imports it first decides.
    from src import late_import

    assert late_import.IMPORTED_AT == datetime(1999, 1, 1)

# --- leaving the block puts everything back -----------------------------
assert datetime.now() > datetime(2020, 1, 1)
assert clock.datetime is REAL["datetime"]
assert clock.stamp() > datetime(2020, 1, 1)
assert time.monotonic() < 946_000_000
assert EXPOSED_DATETIME is REAL["datetime"]
assert late_import.datetime is REAL["datetime"]

# Except the value: it is still 1999, and it is still one of freezegun's
# datetimes, in a module that will be imported from other tests all suite.
assert late_import.IMPORTED_AT == datetime(1999, 1, 1)
assert type(late_import.IMPORTED_AT) is not REAL["datetime"]

# --- tz_offset, measured rather than assumed ----------------------------
# freezegun's own README example. The expectation to check at the door is
# that tz_offset makes the naive clock local and an aware clock UTC. It does
# not. tz_offset is added to now(); utcnow() never gets it. So the pair that
# differs by the offset is now() against utcnow(), and an aware
# now(timezone.utc) reads exactly like the naive one with a UTC label
# stapled on -- a label that is wrong by the offset.
with freeze_time("2012-01-14 03:21:34", tz_offset=-4):
    naive = clock.stamp()
    aware = clock.utc_stamp()
    legacy_utc = clock.utc_stamp_legacy()

    assert naive == datetime(2012, 1, 13, 23, 21, 34)
    assert legacy_utc == datetime(2012, 1, 14, 3, 21, 34)
    assert naive - legacy_utc == timedelta(hours=-4)

    # The aware value carries the same reading as the naive one; only the
    # tzinfo differs. Subtracting a naive from an aware datetime raises, so
    # the two are compared by reading.
    assert aware == datetime(2012, 1, 13, 23, 21, 34, tzinfo=timezone.utc)
    assert aware.replace(tzinfo=None) == naive
    assert aware.utcoffset() == timedelta(0)

    # Which leaves the epoch clocks disagreeing by exactly tz_offset.
    # time.time() and the naive timestamp() are the instant that was
    # frozen; the aware one is four hours away from it. A test that stores
    # now(timezone.utc) and compares it against a value the code under test
    # built from time.time() is off by the offset, and the freeze looks
    # innocent while it happens.
    assert time.time() == 1326511294.0
    assert naive.timestamp() == time.time()
    assert aware.timestamp() - time.time() == -4 * 3600

    # date.today() takes the offset too, so an offset large enough to cross
    # midnight changes the date a report is filed under.
    assert date.today() == date(2012, 1, 13)

# The offset does not have to be whole hours, and a timedelta says so more
# clearly than a float.
with freeze_time("2012-01-14 03:21:34", tz_offset=timedelta(hours=5, minutes=30)):
    assert datetime.now() == datetime(2012, 1, 14, 8, 51, 34)
    assert datetime.utcnow() == datetime(2012, 1, 14, 3, 21, 34)

# --- one signal the freeze swallows -------------------------------------
# datetime.utcnow() is deprecated on 3.12 and says so. Under a freeze the
# call lands on freezegun's own classmethod, which warns about nothing, so a
# suite that runs everything frozen and turns DeprecationWarning into an
# error still will not find its utcnow calls.
with warnings.catch_warnings(record=True) as real_warnings:
    warnings.simplefilter("always")
    datetime.utcnow()
assert [type(w.message) for w in real_warnings] == [DeprecationWarning]

with freeze_time("1999-12-31 23:59:58.5"):
    with warnings.catch_warnings(record=True) as frozen_warnings:
        warnings.simplefilter("always")
        assert clock.utc_stamp_legacy() == FROZEN
    assert frozen_warnings == []

print("CONTRACT PASS: freezegun", freezegun.__version__,
      "froze datetime, time.time and monotonic together on python",
      ".".join(str(part) for part in sys.version_info[:2]))
