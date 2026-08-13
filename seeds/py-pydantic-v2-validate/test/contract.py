import os
import sys
import warnings

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from src.models import Peer, errors_for, validate

peer = validate({"peer_id": "ed25519:abc", "port": 48620, "tags": ["blob"]})
assert peer.peer_id == "ed25519:abc"
assert peer.port == 48620

# v2 returns plain data from model_dump; .dict() is gone.
assert validate({"peer_id": "abc", "port": 1}).model_dump() == {
    "peer_id": "abc", "port": 1, "tags": []
}
# The v1 names still EXIST in v2 as deprecated shims — they do not raise
# AttributeError, they warn. That is why a v1 codebase keeps working after
# the upgrade while quietly accruing deprecation debt, and why "it still
# runs" is not evidence that the migration is done.
with warnings.catch_warnings(record=True) as caught:
    warnings.simplefilter("always")
    legacy = Peer.parse_obj({"peer_id": "ed25519:ghi", "port": 2})
    assert legacy.port == 2
    assert any(issubclass(w.category, DeprecationWarning) for w in caught),         "parse_obj should warn in v2"

errs = errors_for({"peer_id": "ab", "port": 99999})
codes = sorted(e["type"] for e in errs)
assert len(errs) == 2, errs
assert all("loc" in e and "msg" in e for e in errs)
assert codes == ["less_than_equal", "string_too_short"], codes

print("CONTRACT PASS: pydantic v2 validated, dumped and reported typed errors")
