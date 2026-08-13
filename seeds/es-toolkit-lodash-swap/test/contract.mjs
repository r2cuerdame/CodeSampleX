import { strict as assert } from 'node:assert';
import { pageRows, byStatus, summarize, makeDebounced } from '../src/index.mjs';

assert.deepEqual(pageRows([1, 2, 3, 4, 5], 2), [[1, 2], [3, 4], [5]]);

const rows = [{ id: 1, status: 'ok', note: 'x' }, { id: 2, status: 'fail' },
              { id: 3, status: 'ok' }];
const grouped = byStatus(rows);
assert.deepEqual(Object.keys(grouped).sort(), ['fail', 'ok']);
assert.equal(grouped.ok.length, 2);

assert.deepEqual(summarize(rows[0]), { id: 1, status: 'ok' });
// A key that is not there is omitted, not set to undefined.
assert.deepEqual(Object.keys(summarize({ id: 9 })), ['id']);

let calls = 0;
const d = makeDebounced(() => { calls += 1; }, 20);
d(); d(); d();
assert.equal(calls, 0, 'debounced calls do not run immediately');
await new Promise((r) => setTimeout(r, 60));
assert.equal(calls, 1, 'three rapid calls coalesce into one');

console.log('CONTRACT PASS: es-toolkit matched the lodash helpers it replaces');
