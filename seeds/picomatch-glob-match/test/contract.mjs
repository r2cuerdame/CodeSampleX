import { strict as assert } from 'node:assert';
import { matcher, matcherExcluding } from '../src/index.mjs';

// 1. Separator crossing.
const shallow = matcher('src/*.js');
assert.equal(shallow('src/a.js'), true);
assert.equal(shallow('src/nested/a.js'), false, '* must not cross a separator');
assert.equal(matcher('src/**/*.js')('src/nested/deep/a.js'), true);

// 2. Dotfiles are invisible by default.
assert.equal(matcher('src/**/*.js')('src/.hidden/a.js'), false);
assert.equal(matcher('src/**/*.js', { dot: true })('src/.hidden/a.js'), true);

// 3. The trap: an array is OR-ed, so a "!" entry excludes NOTHING.
const arrayForm = matcher(['src/**/*.js', '!**/*.test.js']);
assert.equal(arrayForm('src/a.js'), true);
assert.equal(arrayForm('src/a.test.js'), true,
  'the negated entry does not subtract - the file still matches the positive pattern');

// The option that actually excludes.
const excluding = matcherExcluding('src/**/*.js', ['**/*.test.js']);
assert.equal(excluding('src/a.js'), true);
assert.equal(excluding('src/a.test.js'), false);

// A lone negated pattern inverts by itself, which is why the array form
// looks like it should work.
const loneNegation = matcher('!**/*.test.js');
assert.equal(loneNegation('src/a.test.js'), false);
assert.equal(loneNegation('src/a.js'), true);

// Braces still expand as expected.
assert.equal(matcher('src/*.{js,ts}')('src/a.ts'), true);

console.log('CONTRACT PASS: picomatch OR-ed the array and excluded only via ignore');
