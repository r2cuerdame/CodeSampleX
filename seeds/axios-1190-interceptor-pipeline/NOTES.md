# Axios 1.19.0 interceptor pipeline

Request interceptors are stacked, so the newest runs first. Response
interceptors are queued, so the oldest runs first. The `synchronous` option can
keep request preparation and adapter invocation in the same call stack, while
`runWhen` removes a skipped interceptor before Axios decides whether the chain
must be asynchronous.

The contract uses a custom adapter that returns a complete Axios response
without opening a socket. That isolates interceptor behavior and keeps the
contract deterministic with the verifier network disabled.
