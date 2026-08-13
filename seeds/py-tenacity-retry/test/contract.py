import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from tenacity import RetryError

from src.retry import Permanent, Transient, make_flaky

# Fails twice, succeeds on the third attempt.
call, calls = make_flaky(2)
assert call() == "ok"
assert calls["n"] == 3, calls

# A permanent error is not retried: raised on the first attempt.
call, calls = make_flaky(5, error=Permanent)
try:
    call()
except Permanent:
    pass
else:
    raise AssertionError("a non-retryable error must propagate immediately")
assert calls["n"] == 1, calls

# Exhausting the attempts raises RetryError wrapping the last failure.
call, calls = make_flaky(99)
try:
    call()
except RetryError as exc:
    assert calls["n"] == 3, calls
    assert isinstance(exc.last_attempt.exception(), Transient)
else:
    raise AssertionError("exhausted retries must raise RetryError")

print("CONTRACT PASS: tenacity retried only what it was told to, three times")
