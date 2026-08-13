import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from src.compat import fits, newest

assert fits("1.12.0", ">=1.0,<2.0")
assert not fits("2.0.0", ">=1.0,<2.0")

# The prerelease rule: excluded by default, included on request.
assert not fits("1.13.0rc1", ">=1.0,<2.0")
assert fits("1.13.0rc1", ">=1.0,<2.0", allow_prerelease=True)

# PEP 440 ordering, not lexicographic: 1.10 is newer than 1.9.
assert newest(["1.9.0", "1.10.0", "1.2.0"]) == "1.10.0"
assert newest(["2.0.0rc1", "2.0.0"]) == "2.0.0"

print("CONTRACT PASS: packaging applied PEP 440 ranges and ordering")
