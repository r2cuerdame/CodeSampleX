import { nanoid, customAlphabet } from 'nanoid';

// nanoid is ESM-only and has no default export: `import nanoid from 'nanoid'`
// fails, and require('nanoid') throws ERR_REQUIRE_ESM on older Node.
// customAlphabet returns a generator, so build it once and reuse it.
const numericId = customAlphabet('0123456789', 10);

export function newId() {
  return nanoid();
}

export function newNumericId() {
  return numericId();
}
