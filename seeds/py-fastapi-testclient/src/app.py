"""A FastAPI app shaped so a test can prove what TestClient actually does.

TestClient is not a shortcut around the app. It runs the app on a
background event loop (anyio's blocking portal) behind a transport that
hands the ASGI scope straight to the app, so middleware, path-parameter
coercion, request validation and response serialisation all run the same
code they run behind a real server. Nothing binds a port and nothing
resolves a host name, which is why this whole sample runs with the network
switched off.

That distinction is the trap. Calling an endpoint function directly is a
different operation: no coercion, no validation, no response_model. It can
never return 422 and it cannot strip a field, so a "unit test" that calls
the function tests none of the behaviour people actually rely on.
read_item("abc") happily doubles the string — see test/contract.py.

Three deliberate shapes below: read_item takes an int the caller can only
send as text, create_user returns one field more than it declares so
response_model has something to remove, and the app has a lifespan, which
plain TestClient(app) never runs.
"""

from contextlib import asynccontextmanager

from fastapi import FastAPI, HTTPException
from pydantic import BaseModel
from starlette.datastructures import MutableHeaders
from starlette.types import ASGIApp, Message, Receive, Scope, Send


class UserIn(BaseModel):
    name: str
    age: int


class UserOut(BaseModel):
    name: str
    age: int


LIFECYCLE = []


@asynccontextmanager
async def lifespan(_: FastAPI):
    # Whatever a real app opens here — a pool, a cache, a client — stays
    # closed under a bare TestClient(app), because the lifespan scope is
    # only sent when the client is entered as a context manager. A suite
    # that never writes `with TestClient(app) as client` tests an app whose
    # startup never ran.
    LIFECYCLE.append("startup")
    yield
    LIFECYCLE.append("shutdown")


api = FastAPI(lifespan=lifespan)

USERS = {1: {"name": "ada", "age": 36}}


@api.get("/items/{item_id}")
def read_item(item_id: int):
    # item_id arrives as text on the wire. FastAPI converts it, or answers
    # 422 before this body runs. Called as a function, the annotation is
    # inert and item_id is whatever you passed.
    return {"item_id": item_id, "doubled": item_id * 2}


@api.post("/users", response_model=UserOut, status_code=201)
def create_user(payload: UserIn):
    # The extra key is the point: response_model is a filter on the way
    # out, not a description of the return value. Nothing here prevents a
    # handler from returning a secret; the declared model is what stops it
    # reaching the client.
    return {"name": payload.name, "age": payload.age,
            "password_hash": "sha256:0ddba11"}


@api.get("/users/{user_id}")
def read_user(user_id: int):
    if user_id not in USERS:
        raise HTTPException(status_code=404, detail="user not found")
    return USERS[user_id]


class AsgiMarker:
    """Pure ASGI middleware that can only run if a real request happens.

    It counts http scopes and stamps a header on every response. A test
    that called the endpoint function would leave the counter at zero, so
    the counter is the evidence that TestClient went through the app.
    """

    def __init__(self, app: ASGIApp) -> None:
        self.app = app
        self.http_calls = 0

    async def __call__(self, scope: Scope, receive: Receive, send: Send) -> None:
        if scope["type"] != "http":
            # A lifespan or websocket scope has no http.response.start to
            # stamp, so it is forwarded untouched. Swallowing it here is the
            # classic way a hand-written middleware silently disables an
            # app's startup; the contract checks the lifespan still fires.
            await self.app(scope, receive, send)
            return
        self.http_calls += 1
        seen = self.http_calls

        async def send_marked(message: Message) -> None:
            if message["type"] == "http.response.start":
                MutableHeaders(scope=message)["x-asgi-marker"] = str(seen)
            await send(message)

        await self.app(scope, receive, send_marked)


app = AsgiMarker(api)
