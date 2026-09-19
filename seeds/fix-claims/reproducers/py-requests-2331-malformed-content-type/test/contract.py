"""Claim (2.33.1): a Content-Type parameter without a value ("charset" and no "=") is ignored
instead of crashing get_encoding_from_headers."""
from requests.utils import get_encoding_from_headers

assert get_encoding_from_headers({"content-type": "text/html; charset=utf-8"}) == "utf-8"
malformed = get_encoding_from_headers({"content-type": "text/html; charset"})
assert malformed == "ISO-8859-1", f"malformed charset parameter gave {malformed!r}"
assert get_encoding_from_headers({"content-type": "application/json; charset; foo=bar"}) == "utf-8"
print("CONTRACT PASS")
