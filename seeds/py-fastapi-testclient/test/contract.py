import importlib
import sys
import warnings
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

import httpx
import httpx2
import starlette
import starlette.testclient
from fastapi import __version__ as fastapi_version
from fastapi.testclient import TestClient
from starlette.exceptions import StarletteDeprecationWarning

from src.app import LIFECYCLE, UserIn, api, app, create_user, read_item

client = TestClient(app)

# TestClient talks to a host name that exists nowhere. No socket, no port,
# no DNS: this file runs with the network disabled.
assert str(client.base_url) == "http://testserver"

# --- a real request went through the app, not into the function ----------
before = app.http_calls
ok = client.get("/items/7")
assert ok.status_code == 200
assert ok.json() == {"item_id": 7, "doubled": 14}
assert app.http_calls == before + 1
assert ok.headers["x-asgi-marker"] == str(app.http_calls)

# Calling the same endpoint as a plain function skips every layer. The int
# annotation does nothing, so "abc" * 2 is the answer instead of a 422.
assert read_item("abc") == {"item_id": "abc", "doubled": "abcabc"}
assert app.http_calls == before + 1

# --- an int path parameter rejects text with 422 -------------------------
# 422, not 400 and not 404: FastAPI treats an unparseable path parameter as
# a validation failure. The body is a list under "detail", and each entry
# carries exactly these four keys — a client that reads exc["detail"]["msg"]
# is indexing a list with a string.
bad = client.get("/items/abc")
assert bad.status_code == 422
detail = bad.json()["detail"]
assert len(detail) == 1
assert set(detail[0]) == {"type", "loc", "msg", "input"}
assert detail[0]["type"] == "int_parsing"
assert detail[0]["loc"] == ["path", "item_id"]
assert detail[0]["input"] == "abc"
assert detail[0]["msg"] == (
    "Input should be a valid integer, unable to parse string as an integer")

# --- a missing body field is 422, and the loc says where ----------------
missing = client.post("/users", json={"name": "ada"})
assert missing.status_code == 422
assert set(missing.json()["detail"][0]) == {"type", "loc", "msg", "input"}
assert missing.json()["detail"][0]["type"] == "missing"
assert missing.json()["detail"][0]["loc"] == ["body", "age"]
assert missing.json()["detail"][0]["msg"] == "Field required"

# The 422 echoes the offending input straight back to the caller. For a
# body error that is the whole submitted object, so a route that validates
# a payload containing a password answers with the password in it. Replace
# the default handler for RequestValidationError if that matters.
assert missing.json()["detail"][0]["input"] == {"name": "ada"}

# --- a valid body is accepted, and response_model filters the reply -----
created = client.post("/users", json={"name": "ada", "age": 36})
assert created.status_code == 201
assert created.json() == {"name": "ada", "age": 36}

# The handler really did return the extra field. response_model removed it
# on the way out; the function is untouched by the declaration.
leaked = create_user(UserIn(name="ada", age=36))
assert leaked["password_hash"] == "sha256:0ddba11"

# An unexpected field in the request body is ignored, not rejected:
# pydantic v2 models default to extra="ignore". The 201 on its own does not
# show that the field was dropped, since response_model would have hidden a
# carried-through field either way, so the model is checked directly too.
extra = client.post("/users", json={"name": "ada", "age": 36, "role": "admin"})
assert extra.status_code == 201
assert extra.json() == {"name": "ada", "age": 36}
loaded = UserIn.model_validate({"name": "ada", "age": 36, "role": "admin"})
assert loaded.model_dump() == {"name": "ada", "age": 36}
assert loaded.model_extra is None

# --- HTTPException is a JSON object with one key ------------------------
absent = client.get("/users/999")
assert absent.status_code == 404
assert absent.json() == {"detail": "user not found"}
assert client.get("/users/1").json() == {"name": "ada", "age": 36}

# --- the lifespan only runs inside a with block -------------------------
# Every request above went through the app, and startup still has not run.
# TestClient sends the lifespan scope from __enter__, so a suite that keeps
# a module-level client tests an app whose pools were never opened.
assert LIFECYCLE == []
with TestClient(app) as started:
    assert LIFECYCLE == ["startup"]
    assert started.get("/items/2").json() == {"item_id": 2, "doubled": 4}
assert LIFECYCLE == ["startup", "shutdown"]

# --- which httpx TestClient is holding ---------------------------------
# starlette 1.6 imports httpx2 for TestClient and only falls back to httpx
# with a StarletteDeprecationWarning. With both installed httpx2 wins, so
# the response is an httpx2.Response: annotating a test helper with
# httpx.Response, or catching httpx.HTTPStatusError around a TestClient
# call, is wrong in a way no import error will tell you about.
assert starlette.testclient.httpx is httpx2
assert isinstance(ok, httpx2.Response)
assert not isinstance(ok, httpx.Response)

# The exception classes are separate hierarchies, so the except clause a
# 2024 test suite already has does not catch what raise_for_status raises.
try:
    absent.raise_for_status()
except httpx2.HTTPStatusError as exc:
    assert not isinstance(exc, httpx.HTTPStatusError)
else:
    raise AssertionError("404 did not raise")

# TestClient is starlette's, re-exported. FastAPI adds nothing to it.
assert TestClient is starlette.testclient.TestClient

# Prove the fallback rather than describing it: hide httpx2 from the import
# machinery and load the module again. It warns and binds plain httpx, at
# which point every response is an httpx.Response instead — the same test
# suite, different response class, no error anywhere.


class BlockHttpx2:
    def find_spec(self, name, path=None, target=None):
        if name == "httpx2":
            raise ModuleNotFoundError("No module named 'httpx2'", name="httpx2")
        return None


sys.meta_path.insert(0, BlockHttpx2())
del sys.modules["httpx2"], sys.modules["starlette.testclient"]
with warnings.catch_warnings(record=True) as caught:
    warnings.simplefilter("always")
    legacy = importlib.import_module("starlette.testclient")
sys.meta_path.pop(0)
sys.modules["httpx2"] = httpx2

assert [type(w.message) for w in caught] == [StarletteDeprecationWarning]
assert "httpx2" in str(caught[0].message)
assert legacy.httpx is httpx

# And the warning is a UserWarning, not a DeprecationWarning. Test suites
# that turn DeprecationWarning into an error to catch exactly this kind of
# drift will not see it.
assert issubclass(StarletteDeprecationWarning, UserWarning)
assert not issubclass(StarletteDeprecationWarning, DeprecationWarning)

print("contract ok:", fastapi_version, "starlette", starlette.__version__,
      "testclient on httpx2", httpx2.__version__)
