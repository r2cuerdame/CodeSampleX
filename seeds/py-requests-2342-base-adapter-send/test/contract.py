from importlib.metadata import version

import requests
from requests import PreparedRequest, Request, Response, Session
from requests.adapters import BaseAdapter


EXPECTED_VERSION = "2.34.2"
URL = "mock://example.test/items/42"


class InheritedAdapter(BaseAdapter):
    """Deliberately inherits the unimplemented BaseAdapter.send boundary."""

    def close(self) -> None:
        pass


class RequestOnlyAdapter(BaseAdapter):
    """The common incorrect override: Session supplies more than request."""

    def send(self, request):
        raise AssertionError("Session should reject this signature before the body runs")

    def close(self) -> None:
        pass


class RecordingAdapter(BaseAdapter):
    """A socket-free transport with the public BaseAdapter.send shape."""

    def __init__(self) -> None:
        self.calls = []

    def send(
        self,
        request,
        stream=False,
        timeout=None,
        verify=True,
        cert=None,
        proxies=None,
    ):
        self.calls.append(
            {
                "request": request,
                "stream": stream,
                "timeout": timeout,
                "verify": verify,
                "cert": cert,
                "proxies": proxies,
            }
        )
        response = Response()
        response.status_code = 202
        response.url = request.url
        response.request = request
        response.headers["Content-Type"] = "application/json"
        response._content = b'{"accepted":true}'
        return response

    def close(self) -> None:
        pass


def prepared_request() -> PreparedRequest:
    return Request("POST", URL, json={"name": "Ada"}).prepare()


def assert_raises(exc_type, call):
    try:
        call()
    except exc_type as exc:
        return exc
    raise AssertionError(f"expected {exc_type.__name__}")


def assert_base_boundary() -> None:
    request = prepared_request()
    assert isinstance(request, PreparedRequest)

    error = assert_raises(
        NotImplementedError,
        lambda: BaseAdapter().send(
            request,
            stream=False,
            timeout=None,
            verify=True,
            cert=None,
            proxies={},
        ),
    )
    assert str(error) == ""

    session = Session()
    session.mount("mock://", InheritedAdapter())
    assert_raises(NotImplementedError, lambda: session.send(request))
    session.close()


def assert_override_signature_boundary() -> None:
    session = Session()
    session.mount("mock://", RequestOnlyAdapter())
    error = assert_raises(TypeError, lambda: session.send(prepared_request()))
    assert "unexpected keyword argument" in str(error)
    session.close()


def assert_complete_adapter_contract() -> None:
    session = Session()
    adapter = RecordingAdapter()
    session.mount("mock://", adapter)
    request = prepared_request()

    response = session.send(
        request,
        stream=True,
        timeout=(1.5, 4.0),
        verify="/virtual/ca.pem",
        cert=("/virtual/client.pem", "/virtual/client.key"),
        proxies={"mock": "http://proxy.invalid:8080"},
        allow_redirects=False,
    )

    assert len(adapter.calls) == 1
    call = adapter.calls[0]
    assert call == {
        "request": request,
        "stream": True,
        "timeout": (1.5, 4.0),
        "verify": "/virtual/ca.pem",
        "cert": ("/virtual/client.pem", "/virtual/client.key"),
        "proxies": {"mock": "http://proxy.invalid:8080"},
    }
    assert isinstance(call["request"], PreparedRequest)
    assert response.status_code == 202
    assert response.request is request
    assert response.url == URL
    assert response.json() == {"accepted": True}
    assert response.elapsed.total_seconds() >= 0
    session.close()


def main() -> None:
    assert version("requests") == EXPECTED_VERSION
    assert requests.__version__ == EXPECTED_VERSION
    assert_base_boundary()
    assert_override_signature_boundary()
    assert_complete_adapter_contract()
    print(
        "CONTRACT PASS: requests 2.34.2 BaseAdapter.send is an explicit "
        "transport boundary and custom adapters receive all Session options"
    )


if __name__ == "__main__":
    main()
