# React 18.3.1 `useId` and `startTransition`

This contract keeps both requested APIs in a Node-only environment:

- `startTransition` invokes its scope synchronously, returns `undefined`, and
  propagates a synchronous exception to its caller. It does not defer the
  callback or return the callback's value.
- `useId` produces distinct identifiers for multiple hooks in one component.
  Server rendering wires those identifiers through `for` and
  `aria-describedby`, repeats them deterministically for the same root, and
  applies `renderToString`'s `identifierPrefix` to namespace another root.

No browser DOM is required; `react-dom/server` supplies the hook dispatcher.
