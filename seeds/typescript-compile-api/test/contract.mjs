import { strict as assert } from 'node:assert';
import { check } from '../src/index.mjs';

const bad = check('const n: number = "not a number";');
assert.ok(bad.length > 0, 'a type error must be reported');
assert.equal(bad[0].code, 2322); // Type 'X' is not assignable to type 'Y'
assert.match(bad[0].message, /not assignable/);

assert.deepEqual(check('const n: number = 42; export {};'), []);

// strict is not cosmetic: it decides whether this compiles at all.
const src = 'function f(x) { return x; } export {};';
assert.ok(check(src, { strict: true }).some((d) => d.code === 7006),
  'implicit any should be an error under strict');
assert.deepEqual(check(src, { strict: false }), []);

console.log('CONTRACT PASS: TypeScript compiler API reported diagnostics from memory');
