// Importing a CommonJS-only package (lodash) from an ES module.
//
// See src/broken-named-import.mjs and src/broken-require.mjs for the two ways
// this normally blows up. This file is the pattern that works.

// PATTERN 1 — default-import interop.
// A CJS module's `module.exports` always arrives as the ESM default export, so
// take the default and destructure from it. This is exactly what Node's own
// error message tells you to do. It costs nothing at runtime: `_` is the same
// object `require('lodash')` would have returned.
import _ from 'lodash';
// Same package, namespace form — kept only to demonstrate the difference below.
import * as lodashNamespace from 'lodash';

const { chunk, groupBy, merge } = _;

// PATTERN 2 — createRequire, for when you truly need require() semantics.
// `require` does not exist in ESM scope, but you can build a real one bound to
// this file's URL. Use it for things `import` cannot do: requiring a JSON file
// from inside a dependency, or resolving a package to a filesystem path.
// import.meta.url is the ESM stand-in for __filename.
import { createRequire } from 'node:module';

const require = createRequire(import.meta.url);

export function pageItems(items, size) {
  return chunk(items, size);
}

export function bucketByParity(nums) {
  return groupBy(nums, (n) => (n % 2 ? 'odd' : 'even'));
}

export function deepMergeConfig(base, override) {
  // merge() mutates its first argument, so start from a fresh object to avoid
  // clobbering the caller's config.
  return merge({}, base, override);
}

export function lodashVersion() {
  // `import pkg from 'lodash/package.json'` needs an import attribute and is
  // still awkward across runtimes; require() of a JSON subpath just works.
  return require('lodash/package.json').version;
}

export function resolvedFromESM() {
  // No ESM equivalent of require.resolve() that returns a plain path.
  return require.resolve('lodash');
}

export function namespaceShape() {
  // `import * as ns from 'lodash'` does NOT spread the CJS exports onto the
  // namespace — every function hangs off ns.default instead. Reported as
  // typeof strings so the contract can assert the shape.
  return {
    directChunk: typeof lodashNamespace.chunk, // undefined — the trap
    viaDefault: typeof lodashNamespace.default.chunk, // function — the payload
  };
}
