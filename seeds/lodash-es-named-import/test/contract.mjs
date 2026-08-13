import { strict as assert } from 'node:assert';
import { pageRows, byStatus, summarize } from '../src/index.mjs';

assert.deepEqual(pageRows([1, 2, 3, 4, 5], 2), [[1, 2], [3, 4], [5]]);

const rows = [
  { id: 1, status: 'ok', note: 'x' },
  { id: 2, status: 'fail', note: 'y' },
  { id: 3, status: 'ok', note: 'z' },
];
const grouped = byStatus(rows);
assert.deepEqual(Object.keys(grouped).sort(), ['fail', 'ok']);
assert.equal(grouped.ok.length, 2);

assert.deepEqual(summarize(rows[0]), { id: 1, status: 'ok' });
console.log('CONTRACT PASS: lodash-es named imports worked in ESM');
