import { strict as assert } from 'node:assert';
import {
  pageItems,
  bucketByParity,
  deepMergeConfig,
  lodashVersion,
  resolvedFromESM,
  namespaceShape,
} from '../src/index.mjs';

// Helper: import a module that is expected to fail, and hand back the error.
async function importExpectingFailure(spec) {
  try {
    await import(spec);
  } catch (err) {
    return err;
  }
  throw new Error(`expected ${spec} to throw, but it imported cleanly`);
}

// --- 1. Named imports from a CJS-only package fail at link time -------------
const namedErr = await importExpectingFailure('../src/broken-named-import.mjs');
assert.ok(
  namedErr instanceof SyntaxError,
  `expected SyntaxError, got ${namedErr.constructor.name}`,
);
assert.match(namedErr.message, /Named export 'chunk' not found/);
assert.match(namedErr.message, /is a CommonJS module/);

// --- 2. require() does not exist in ES module scope -------------------------
const requireErr = await importExpectingFailure('../src/broken-require.mjs');
assert.ok(
  requireErr instanceof ReferenceError,
  `expected ReferenceError, got ${requireErr.constructor.name}`,
);
assert.match(requireErr.message, /require is not defined in ES module scope/);

// --- 3. The default-import interop pattern returns correct values -----------
assert.deepEqual(pageItems([1, 2, 3, 4, 5], 2), [[1, 2], [3, 4], [5]]);
assert.deepEqual(bucketByParity([1, 2, 3, 4]), { odd: [1, 3], even: [2, 4] });

// merge is a *deep* merge; a spread would drop base.retry.max entirely.
const base = { retry: { max: 3, backoffMs: 250 }, tags: ['a'] };
const merged = deepMergeConfig(base, { retry: { backoffMs: 1000 } });
assert.deepEqual(merged, { retry: { max: 3, backoffMs: 1000 }, tags: ['a'] });
// ...and the helper must not have mutated the caller's object.
assert.equal(base.retry.backoffMs, 250);

// --- 4. createRequire gives genuine require semantics -----------------------
// A version string proves a subpath JSON file was really required.
assert.match(lodashVersion(), /^4\.\d+\.\d+$/);
// require.resolve returns a real filesystem path to the CJS entry point.
assert.match(resolvedFromESM(), /lodash[\\/]lodash\.js$/);

// --- 5. The namespace object is not the module ------------------------------
// `import * as ns` on a CJS-only package yields a namespace whose only useful
// key is `default`; ns.chunk is undefined. This trips people right after they
// give up on named imports.
const shape = namespaceShape();
assert.equal(shape.directChunk, 'undefined');
assert.equal(shape.viaDefault, 'function');

console.log(
  'CONTRACT PASS: named import throws SyntaxError, require throws ReferenceError, ' +
    'default-import + createRequire produce correct lodash results',
);
