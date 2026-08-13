import { strict as assert } from 'node:assert';
import { parse, build } from '../src/index.mjs';

const parsed = parse('filter[status]=open&filter[tags][]=a&filter[tags][]=b&page=2');
assert.deepEqual(parsed.filter.status, 'open');
assert.deepEqual(parsed.filter.tags, ['a', 'b']);
assert.equal(parsed.page, '2'); // values stay strings

// The flat alternative cannot express this.
const flat = new URLSearchParams('filter[status]=open');
assert.equal(flat.get('filter[status]'), 'open');
assert.equal(flat.get('filter'), null);

const round = parse(build({ filter: { status: 'open', tags: ['a', 'b'] } }));
assert.deepEqual(round.filter.tags, ['a', 'b']);

// Depth is bounded by default rather than unbounded.
const deep = parse('a[b][c][d][e][f][g][h]=x');
assert.equal(JSON.stringify(deep).includes('[g][h]'), true,
  'past the default depth the remainder is kept as a literal key');

console.log('CONTRACT PASS: qs parsed nested queries URLSearchParams cannot');
