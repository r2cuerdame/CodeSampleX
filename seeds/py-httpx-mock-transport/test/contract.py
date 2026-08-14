import inspect
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

import httpx

from src.api import build_client, fetch_items


def handler(request: httpx.Request) -> httpx.Response:
    if request.url.path == "/boom":
        return httpx.Response(500, json={"error": "nope"})
    return httpx.Response(200, json={"path": request.url.path,
                                     "q": dict(request.url.params)})


client = build_client(handler)

# The client is real: base_url, params and json decoding all run.
assert fetch_items(client, 2) == {"path": "/items", "q": {"page": "2"}}

# raise_for_status raises HTTPStatusError, and the body is still readable
# afterwards — the response is not consumed by the check.
failed = client.get("/boom")
try:
    failed.raise_for_status()
    raise AssertionError("500 should raise")
except httpx.HTTPStatusError as exc:
    assert exc.response.status_code == 500
assert failed.json() == {"error": "nope"}

# On success it returns the response, so it chains.
assert isinstance(client.get("/items").raise_for_status(), httpx.Response)

# The app= shortcut is gone as of 0.28. Passing it now is a TypeError, not
# a deprecation, and the transports are the replacement.
assert "app" not in inspect.signature(httpx.Client.__init__).parameters
assert hasattr(httpx, "WSGITransport") and hasattr(httpx, "ASGITransport")

# The two defaults that differ from requests, stated as assertions because
# both change behaviour without changing any of your code.
assert httpx.Client().timeout == httpx.Timeout(5.0)
assert httpx.Client().follow_redirects is False

print("contract ok:", httpx.__version__)
