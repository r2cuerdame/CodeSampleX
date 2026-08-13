'use strict';

// The pattern this sample exists to replace. Run it directly to see both
// failure modes on one Node:
//
//     node src/broken-require.js
//
// Node < 22.12 threw ERR_REQUIRE_ESM on BOTH requires below. Node >= 22.12
// can require() an ESM module graph, so the two now fail in different ways --
// and only one of them is still loud.

// (1) p-limit is ESM-only ("type": "module", no require condition in exports)
//     but its graph has no top-level await, so on Node >= 22.12 require()
//     SUCCEEDS. It returns the ES module *namespace object*, not the default
//     export, so the idiomatic CommonJS binding below gets an object where the
//     function should be and the crash moves to the call site.
const pLimit = require('p-limit');
console.log('p-limit require() returned:', typeof pLimit);
try {
  pLimit(2);
} catch (err) {
  console.log('calling it: ' + err.constructor.name + ': ' + err.message);
}

// (2) yoga-layout is ESM-only AND awaits its WebAssembly instantiation at the
//     top level of its module graph. No Node version can require() that: a
//     synchronous require() has nothing to block on. This one still throws,
//     and it is the throw the contract asserts.
//
//     `node --experimental-print-required-tla src/broken-require.js` names it:
//       unexpected top-level await at node_modules/yoga-layout/dist/src/index.js:13
//       const Yoga = wrapAssembly(await loadYoga());
const Yoga = require('yoga-layout');
console.log('unreachable', typeof Yoga);
