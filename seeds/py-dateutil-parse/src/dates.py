"""Date parsing with dateutil, including the ambiguity nobody expects."""

from dateutil import parser
from dateutil.relativedelta import relativedelta


def parse(text, dayfirst=False):
    """Parse a date string.

    "03/04/2026" is March 4th in the US reading and April 3rd elsewhere, and
    dateutil picks the US one unless told otherwise. It is not a bug and it
    will not warn — pass dayfirst explicitly whenever the input is not ISO.
    """
    return parser.parse(text, dayfirst=dayfirst)


def add_months(dt, months):
    """Add calendar months. timedelta(days=30*n) drifts; relativedelta does not."""
    return dt + relativedelta(months=months)
