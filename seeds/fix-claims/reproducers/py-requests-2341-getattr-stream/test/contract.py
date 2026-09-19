"""Claim (2.34.1): a body whose file-like interface is proxied through __getattr__ is detected
as a stream, so its position is recorded and it can be rewound on a 307/308 redirect."""
import io
import requests


class Wrapped:
    """A tqdm.wrapattr-style proxy: no __iter__ on the class, everything via __getattr__."""

    def __init__(self, inner):
        self._inner = inner

    def __getattr__(self, name):
        return getattr(self._inner, name)


raw = io.BytesIO(b"hello world " * 10)
raw.seek(4)
prepared = requests.Request("PUT", "http://127.0.0.1:9/upload", data=Wrapped(raw)).prepare()
assert prepared.body is not None
assert getattr(prepared, "_body_position", None) == 4, (
    f"_body_position is {getattr(prepared, '_body_position', None)!r}; the __getattr__ proxy "
    "was not detected as a stream, so the body cannot be rewound on redirect"
)
print("CONTRACT PASS")
