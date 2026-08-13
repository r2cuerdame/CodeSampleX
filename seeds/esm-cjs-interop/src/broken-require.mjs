// FAILURE MODE 2 — DO NOT COPY THIS.
//
// The reflex after failure mode 1 is to reach for require(). But `require` is a
// CommonJS-module-scope variable, and this file is an ES module, so the
// identifier simply does not exist. Importing this file throws at RUN time:
//
//   ReferenceError: require is not defined in ES module scope, you can use
//   import instead
//
// The same applies to `module`, `exports`, `__dirname` and `__filename`.
// The fix is not to rename the file to .cjs — it is createRequire(), see
// src/index.mjs.
const _ = require('lodash');

export const nope = _.chunk;
