import { strict as assert } from 'node:assert';
import { runLimited } from '../src/index.mjs';

let active = 0;
let peak = 0;
const task = (i) => async () => {
  active += 1;
  peak = Math.max(peak, active);
  await new Promise((r) => setTimeout(r, 20));
  active -= 1;
  return i;
};

const results = await runLimited([0, 1, 2, 3, 4, 5].map(task), 2);

assert.deepEqual(results, [0, 1, 2, 3, 4, 5]);
assert.equal(peak, 2, `peak concurrency was ${peak}, expected the limit of 2`);
assert.equal(active, 0);
console.log('CONTRACT PASS: p-limit held concurrency at 2 and preserved result order');
