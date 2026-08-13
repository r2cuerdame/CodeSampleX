// FAILURE MODE 1 — DO NOT COPY THIS.
//
// lodash is CommonJS-only (no "type":"module", no "module" field, no "exports"
// field). Node can still `import` it, but the named exports you get are
// whatever Node's static CJS lexer can detect by reading the source. lodash
// assigns its exports from inside a UMD closure, so the lexer detects nothing
// and the only export available is `default`.
//
// Importing this file throws at LINK time, before a single line runs:
//
//   SyntaxError: Named export 'chunk' not found. The requested module 'lodash'
//   is a CommonJS module, which may not support all module.exports as named
//   exports.
//
// Note this is package-shape dependent, not a blanket rule about CJS: the lexer
// *does* detect `module.exports = { ... }` object literals, so named imports
// from some CJS packages work fine. You cannot tell by looking at the import.
import { chunk } from 'lodash';

export const nope = chunk;
