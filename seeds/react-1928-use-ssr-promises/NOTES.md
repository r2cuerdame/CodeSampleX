# React 19.2.8 `use()` during server rendering

A native JavaScript promise does not expose its settled state synchronously.
On first use, React annotates the thenable as `pending`, attaches handlers, and
lets Suspense handle it. Even `Promise.resolve(value)` therefore falls back on
its first `renderToString` call. After a later task, React has changed the same
thenable to `fulfilled` or `rejected`, and a second render sees that state.

Use `renderToPipeableStream` when the server should actually wait for pending
content. The contract shows the fallback shell arriving first and the resolved
content arriving in a later chunk, all with the network disabled.
