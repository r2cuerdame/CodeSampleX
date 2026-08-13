import { strict as assert } from 'node:assert';
import { createApp } from '../src/index.mjs';

const server = createApp().listen(0, '127.0.0.1');
await new Promise((r) => server.on('listening', r));
const { port } = server.address();

const res = await fetch(`http://127.0.0.1:${port}/boom`, { signal: AbortSignal.timeout(5000) });
const body = await res.json();
server.close();

assert.equal(res.status, 500);
assert.equal(body.handled, true);
assert.equal(body.message, 'async failure');
console.log('CONTRACT PASS: express 5 routed an async rejection to the error middleware');
