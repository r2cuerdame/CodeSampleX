"""Bounded retries with tenacity."""

from tenacity import (
    retry,
    retry_if_exception_type,
    stop_after_attempt,
    wait_fixed,
)


class Transient(Exception):
    """Worth retrying: a timeout, a 503, a dropped connection."""


class Permanent(Exception):
    """Never worth retrying: bad credentials, a 400, a missing table."""


def make_flaky(fail_times, error=Transient):
    calls = {"n": 0}

    @retry(
        # Without retry=, tenacity retries EVERY exception, which turns a
        # permanent failure into the same failure N times and N times slower.
        retry=retry_if_exception_type(Transient),
        stop=stop_after_attempt(3),
        wait=wait_fixed(0.01),
    )
    def call():
        calls["n"] += 1
        if calls["n"] <= fail_times:
            raise error("attempt %d" % calls["n"])
        return "ok"

    return call, calls
