"""Testing HTTP code with httpx without touching the network, and the two
defaults that differ from requests.

MockTransport takes a plain function from Request to Response, so the whole
client — base_url, params, headers, raise_for_status — is exercised for
real and nothing is monkeypatched. That is the part worth copying.

The `app=` shortcut people reach for first was removed in httpx 0.28;
WSGITransport and ASGITransport are the replacements, and they are what
MockTransport sits beside.

Two defaults that silently change behaviour when porting from requests:
httpx applies a 5 second timeout where requests waits forever, and httpx
does NOT follow redirects where requests does.
"""

import httpx


def build_client(handler) -> httpx.Client:
    """A client that never opens a socket, with everything else intact."""
    return httpx.Client(
        transport=httpx.MockTransport(handler),
        base_url="https://api.example.com",
    )


def fetch_items(client: httpx.Client, page: int) -> dict:
    response = client.get("/items", params={"page": str(page)})
    response.raise_for_status()
    return response.json()
