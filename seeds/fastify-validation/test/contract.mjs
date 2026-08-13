import { strict as assert } from 'node:assert';
import { createApp } from '../src/index.mjs';

const app = createApp();

const ok = await app.inject({ method: 'POST', url: '/peers',
  payload: { peerId: 'ed25519:abc', port: 48620 } });
assert.equal(ok.statusCode, 200);
assert.deepEqual(ok.json(), { registered: true, peerId: 'ed25519:abc' });

const bad = await app.inject({ method: 'POST', url: '/peers',
  payload: { peerId: 'ab', port: 99999 } });
assert.equal(bad.statusCode, 400);
assert.match(bad.json().message, /peerId|port/);

const missing = await app.inject({ method: 'POST', url: '/peers', payload: {} });
assert.equal(missing.statusCode, 400);

await app.close();
console.log('CONTRACT PASS: fastify rejected invalid bodies before the handler');
