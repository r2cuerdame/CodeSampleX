import json
import sys
from pathlib import Path

from django.conf import settings


sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

if not settings.configured:
    settings.configure(
        ALLOWED_HOSTS=["testserver"],
        DEBUG=False,
        INSTALLED_APPS=[],
        MIDDLEWARE=["src.app.MarkerMiddleware"],
        ROOT_URLCONF="src.app",
        SECRET_KEY="public-sample-secret-not-for-production",
    )

import django

django.setup()

from django.http import JsonResponse
from django.test import Client, RequestFactory

from src.app import item_view


def decoded(response):
    return json.loads(response.content.decode("utf-8"))


# JsonResponse defaults to dict-only input and application/json with the
# default encoder's ASCII escaping and ordinary json.dumps spacing.
ordinary = JsonResponse({"text": "한글", "count": 2})
assert ordinary.status_code == 200
assert ordinary.headers["Content-Type"] == "application/json"
assert decoded(ordinary) == {"text": "한글", "count": 2}
assert all(byte < 128 for byte in ordinary.content)
assert ordinary.content.startswith(b'{"text": "')
assert ordinary.content.endswith(b'", "count": 2}')

try:
    JsonResponse([1, 2])
except TypeError as error:
    assert "safe parameter to False" in str(error)
else:
    raise AssertionError("JsonResponse accepted a list with safe=True")

unsafe_list = JsonResponse([1, 2], safe=False)
assert unsafe_list.content == b"[1, 2]"

# RequestFactory only constructs a request. It does not resolve the URL,
# apply the <int:...> converter, run middleware, or add Client's .json helper
# to the response returned by a directly called view.
factory_request = RequestFactory().get("/items/7/")
assert getattr(factory_request, "resolver_match", None) is None
direct = item_view(factory_request, item_id="7")
assert decoded(direct) == {
    "item_id": "7",
    "item_id_type": "str",
    "middleware": False,
}
assert "X-Middleware" not in direct.headers
assert not hasattr(direct, "json")

# Client follows django.urls.path, applies the int converter and middleware,
# and annotates the response with resolver_match and a content-type-checked
# .json() helper.
client = Client()
routed = client.get("/items/7/")
assert routed.status_code == 200
assert routed.json() == {
    "item_id": 7,
    "item_id_type": "int",
    "middleware": True,
}
assert routed.headers["X-Middleware"] == "applied"
assert routed.resolver_match.url_name == "item-detail"
assert client.get("/items/not-an-int/").status_code == 404

listed = client.get("/items/")
assert listed.json() == ["first", "second"]
assert listed.resolver_match.url_name == "item-list"

# json_dumps_params is passed to json.dumps. Here it changes key order,
# whitespace, and Unicode escaping all at once.
compact = client.get("/compact/")
assert compact.content.decode("utf-8") == '{"a":"한글","z":1}'
assert compact.json() == {"a": "한글", "z": 1}

plain = client.get("/text/")
try:
    plain.json()
except ValueError as error:
    assert "Content-Type header is \"text/plain\"" in str(error)
else:
    raise AssertionError("Client response.json parsed a text/plain response")

print("Django JsonResponse and path contract passed")
