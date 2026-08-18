# Puppeteer 24.4 page evaluation boundaries

This contract runs in a verifier image that actually contains Chrome 134. It
does not infer browser behavior from Node-side types or Puppeteer metadata.
The contract container has networking disabled and injects its own HTML.

- `$eval` gives its callback one element and rejects a missing selector.
- `$$eval` gives its callback an array and permits an empty match set.
- A live DOM node is not serialized as its properties when returned from
  `evaluate`; it crosses as an empty plain object.
- `exposeFunction` always presents a Promise-returning function to page code,
  even when the Node implementation itself returns synchronously.

The launch flags disable Chrome's inner sandbox because the complete sample is
already isolated by the outer disposable Docker container with fixed memory,
PID, and network limits.
