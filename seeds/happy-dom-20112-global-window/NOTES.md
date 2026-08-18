# happy-dom 20.11.2 `GlobalWindow`

`GlobalWindow` intentionally reuses Node's global constructors and evaluates
scripts against the host `globalThis`. That preserves cross-realm identity for
`Array`, `Promise`, `Buffer`, and similar values, but it is not an isolation
boundary: evaluated code can mutate the test process's globals. Use `Window`
when a separate VM realm is required.

For a detached root, `window.close()` follows browser rules and is a no-op
because the window was not opened by another window. Timers continue to run and
`closed` remains false. Test teardown should await `window.happyDOM.close()`;
that closes the detached page, cancels its timers, and marks the window closed.
