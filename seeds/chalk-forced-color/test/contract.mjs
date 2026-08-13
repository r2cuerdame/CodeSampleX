import { strict as assert } from 'node:assert';
import { warn, plain } from '../src/index.mjs';

const painted = warn('disk full');
assert.equal(painted, '\u001b[31mdisk full\u001b[39m');
assert.ok(painted.includes('\u001b['));

// Level 0 is the state a piped process normally lands in.
assert.equal(plain.red('disk full'), 'disk full');
console.log('CONTRACT PASS: an explicit chalk level decides colour, not the TTY');
