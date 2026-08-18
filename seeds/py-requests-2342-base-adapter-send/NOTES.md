# Requests 2.34.2 `BaseAdapter.send` boundary

`BaseAdapter` is a transport interface, not a usable default transport. Its
`send` implementation raises `NotImplementedError`, including when a subclass
inherits it unchanged and is mounted on a `Session`.

`Session.send` selects the mounted adapter and supplies the prepared request
together with `stream`, `timeout`, `verify`, `cert`, and `proxies` keyword
arguments. An override shaped only as `send(self, request)` therefore fails
before its body runs. A custom adapter must accept the documented transport
arguments (or compatible keyword arguments) and return a `Response`.

The contract uses a `mock://` mounted adapter and never opens a socket. It
checks the exact transport options received by the adapter and the response
association completed by `Session.send`.
