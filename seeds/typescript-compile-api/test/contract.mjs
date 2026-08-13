import { strict as assert } from 'node:assert';
import { check, loadCompiler, hasCompilerAPI } from '../src/index.mjs';

// 1. The migration fact, proven rather than claimed: both majors are
//    installed, and only 5.x carries the compiler API.
const ts5 = loadCompiler('typescript');
const ts7 = loadCompiler('typescript7');
assert.match(ts5.version, /^5\./);
assert.match(ts7.version, /^7\./);
assert.equal(hasCompilerAPI(ts5), true, 'typescript 5 exposes the compiler API');
assert.equal(hasCompilerAPI(ts7), false, 'typescript 7 does not');
assert.equal(typeof ts7.createProgram, 'undefined');
assert.deepEqual(Object.keys(ts7).sort(), ['version', 'versionMajorMinor']);

// 2. With 5.x the in-memory type check works.
const bad = check('const n: number = "not a number";');
assert.ok(bad.length > 0, 'a type error must be reported');
assert.ok(bad.some((d) => d.code === 2322), // Type 'X' is not assignable to type 'Y'
  'the assignment error must be reported: ' + JSON.stringify(bad));
assert.match(bad.find((d) => d.code === 2322).message, /not assignable/);

assert.deepEqual(check('const n: number = 42; export {};'), []);

// 3. strict is not cosmetic: it decides whether this compiles at all.
const implicitAny = 'function f(x) { return x; } export {};';
assert.ok(check(implicitAny, { strict: true }).some((d) => d.code === 7006),
  'implicit any should be an error under strict');
assert.deepEqual(check(implicitAny, { strict: false }), []);

console.log('CONTRACT PASS: the compiler API works on 5.x and is absent from 7.x');
