import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from src.dates import add_months, parse

iso = parse("2026-08-13T09:05:00")
assert (iso.year, iso.month, iso.day, iso.hour) == (2026, 8, 13, 9)

# The ambiguity: same input, two readings, decided by dayfirst.
us = parse("03/04/2026")
eu = parse("03/04/2026", dayfirst=True)
assert (us.month, us.day) == (3, 4)
assert (eu.month, eu.day) == (4, 3)

try:
    parse("definitely not a date")
except Exception:
    pass
else:
    raise AssertionError("unparseable input must raise, not guess")

# Calendar months: Jan 31 + 1 month is Feb 28, not March 2.
end_of_jan = parse("2026-01-31")
assert add_months(end_of_jan, 1).strftime("%Y-%m-%d") == "2026-02-28"
assert add_months(parse("2026-08-30"), 6).strftime("%Y-%m-%d") == "2027-02-28"

print("CONTRACT PASS: dateutil resolved the ambiguous date and month arithmetic")
