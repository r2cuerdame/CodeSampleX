import http from 'node:http';
import { strict as assert } from 'node:assert';
import { postJSON } from '../src/index.mjs';

const payload = { hello: 'codesamplex', n: 7 };
let seen = null;
const server = http.createServer((req, res) => {
  let raw = '';
  req.on('data', (c) => (raw += c));
  req.on('end', () => {
    seen = { type: req.headers['content-type'], raw, method: req.method };
    res.writeHead(200, { 'content-type': 'application/json' });
    res.end(JSON.stringify({ echoed: JSON.parse(raw) }));
  });
});
await new Promise((r) => server.listen(0, '127.0.0.1', r));

const out = await postJSON(`http://127.0.0.1:${server.address().port}/echo`, payload);
server.close();

assert.equal(seen.method, 'POST');
assert.match(seen.type, /^application\/json/);
assert.deepEqual(JSON.parse(seen.raw), payload);
assert.equal(out.statusCode, 200);
assert.deepEqual(out.data.echoed, payload);
console.log('CONTRACT PASS: undici.request posted JSON and body.json() parsed the reply');
