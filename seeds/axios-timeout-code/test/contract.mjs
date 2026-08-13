import http from 'node:http';
import { strict as assert } from 'node:assert';
import { getWithTimeout } from '../src/index.mjs';

// Never answers: the socket stays open until axios gives up.
const silent = http.createServer(() => {});
await new Promise((r) => silent.listen(0, '127.0.0.1', r));
const slow = await getWithTimeout(`http://127.0.0.1:${silent.address().port}/`, 150);
silent.closeAllConnections();
silent.close();

assert.equal(slow.timedOut, true);
assert.equal(slow.code, 'ECONNABORTED');
assert.equal(slow.hasResponse, false);

const quick = http.createServer((_req, res) => res.end('ok'));
await new Promise((r) => quick.listen(0, '127.0.0.1', r));
const fast = await getWithTimeout(`http://127.0.0.1:${quick.address().port}/`, 5000);
quick.close();

assert.equal(fast.timedOut, false);
assert.equal(fast.status, 200);
console.log('CONTRACT PASS: axios timeout is identifiable by code ECONNABORTED');
