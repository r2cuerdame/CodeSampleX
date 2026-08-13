import { strict as assert } from 'node:assert';
import { createApp } from '../src/app.mjs';

const app = createApp();
const server = await new Promise((resolve) => {
  const s = app.listen(0, '127.0.0.1', () => resolve(s));
});
const { port } = server.address();

const body = { name: 'widget', qty: 3 };
const res = await fetch(`http://127.0.0.1:${port}/items`, {
  method: 'POST',
  headers: { 'content-type': 'application/json' },
  body: JSON.stringify(body),
});
const json = await res.json();
server.close();

assert.equal(res.status, 201);
assert.deepEqual(json.created, body, 'express.json() must populate req.body');
console.log('CONTRACT PASS: express route parsed and echoed the JSON body');
