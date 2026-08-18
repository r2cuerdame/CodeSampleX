# Marked 13.0.2 parsing boundaries

`marked.parse` is synchronous by default. Passing `async: true` changes the
return type to a Promise even without an asynchronous extension. An extension
registered with `async: true` also forces Promise-based parsing and awaits its
`walkTokens` work. In 13.0.2, a per-call `async: false` override only emits a
warning: it is ignored and parsing still returns a Promise.

Marked is a Markdown parser, not an HTML sanitizer. Embedded HTML, including
event-handler attributes, passes through to its output. Untrusted input needs a
separate sanitization boundary before the generated HTML reaches a browser.
