# fast-glob 3.3.3 async, sync, and stream modes

The same overlapping patterns run through all three public entry points:

- the default `fg(...)` returns a Promise of path strings;
- `fg.sync(...)` returns the path array immediately;
- `fg.stream(...)` returns a Node readable stream, and `objectMode` with
  `stats` yields Entry objects whose `stats` describe the matched file.

All modes apply `ignore`, skip dot directories by default, and de-duplicate
paths matched by both input patterns. Results are sorted before comparison
because traversal order is not part of the contract.
