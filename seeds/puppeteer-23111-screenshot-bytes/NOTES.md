# Puppeteer 23.11.1 screenshot bytes

The default `page.screenshot()` result is a plain `Uint8Array`, not a Node.js
`Buffer`. Its inherited `toString()` ignores the `"base64"` argument and emits
comma-separated byte values without throwing. That makes the mistake easy to
ship because the result is still a non-empty string.

For Node code, wrap the bytes with `Buffer.from(result)` before Base64 encoding,
or request `{ encoding: "base64" }` from Puppeteer. This contract compares both
correct forms against the same deterministic PNG and runs in a real offline
Chrome 134 process rather than inferring browser behavior from type metadata.
