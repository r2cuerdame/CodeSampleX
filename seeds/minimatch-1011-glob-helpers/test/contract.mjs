import { strict as assert } from 'node:assert';
import {
  describeMagic,
  escapeWindowsLiteral,
  isPartialMatch,
  isStrictMatch,
  keepMatching,
  matchesBracePattern,
  matchesEscapedWindowsLiteral,
  unescapeWindowsLiteral
} from '../src/index.mjs';

assert.equal(matchesBracePattern('src/app.js'), true);
assert.equal(matchesBracePattern('src/app.ts'), true);
assert.equal(matchesBracePattern('src/app.mjs'), false);

assert.equal(isPartialMatch('src'), true);
assert.equal(isPartialMatch('src/lib'), true);
assert.equal(isStrictMatch('src'), false);

assert.equal(describeMagic('{a,b}'), false);
assert.equal(describeMagic('{a,b}', { magicalBraces: true }), true);

assert.deepEqual(
  keepMatching(['alpha.js', 'beta.ts', 'gamma.js'], '*.js'),
  ['alpha.js', 'gamma.js']
);

const literalWindowsPath = 'dir\\*.txt';
const escapedLiteral = escapeWindowsLiteral(literalWindowsPath);
assert.equal(escapedLiteral, 'dir\\[*].txt');
assert.equal(matchesEscapedWindowsLiteral(literalWindowsPath), true);
assert.equal(unescapeWindowsLiteral(escapedLiteral), literalWindowsPath);

console.log('CONTRACT PASS: minimatch 10 matched, filtered, and escaped patterns as expected');
