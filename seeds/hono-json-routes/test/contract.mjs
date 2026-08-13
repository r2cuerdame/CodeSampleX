import { strict as assert } from 'node:assert';
import { createApp } from '../src/index.mjs';

const app = createApp();

const got = await app.fetch(new Request('http://local/items/42'));
assert.equal(got.status, 200);
assert.deepEqual(await got.json(), { id: '42', ok: true });

const made = await app.fetch(new Request('http://local/items', {
  method: 'POST',
  headers: { 'content-type': 'application/json' },
  body: JSON.stringify({ name: 'sample' }),
}));
assert.equal(made.status, 201);
assert.deepEqual(await made.json(), { created: { name: 'sample' } });

const missing = await app.fetch(new Request('http://local/nope'));
assert.equal(missing.status, 404);

console.log('CONTRACT PASS: hono routed, parsed params and answered 404');
