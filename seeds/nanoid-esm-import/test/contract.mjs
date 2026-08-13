import { strict as assert } from 'node:assert';
import { newId, newNumericId } from '../src/index.mjs';

const id = newId();
assert.equal(id.length, 21);
assert.match(id, /^[A-Za-z0-9_-]{21}$/);

const numeric = newNumericId();
assert.match(numeric, /^[0-9]{10}$/);

const seen = new Set();
for (let i = 0; i < 1000; i++) seen.add(newId());
assert.equal(seen.size, 1000);
console.log('CONTRACT PASS: nanoid named exports produced unique URL-safe ids');
