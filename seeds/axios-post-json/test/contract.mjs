import http from 'node:http';
import { strict as assert } from 'node:assert';
import { postJSON } from '../src/index.mjs';

const body = { hello: 'codesamplex', n: 42 };
let received = null;

const server = http.createServer((req, res) => {
  let raw = '';
  req.on('data', (c) => (raw += c));
  req.on('end', () => {
    received = { contentType: req.headers['content-type'], raw, method: req.method };
    res.writeHead(200, { 'content-type': 'application/json' });
    res.end(JSON.stringify({ echoed: JSON.parse(raw) }));
  });
});

await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
const { port } = server.address();

const out = await postJSON(`http://127.0.0.1:${port}/echo`, body);
server.close();

assert.equal(received.method, 'POST');
assert.match(received.contentType, /^application\/json/);
assert.deepEqual(JSON.parse(received.raw), body);
assert.equal(out.status, 200);
assert.deepEqual(out.data.echoed, body);
console.log('CONTRACT PASS: axios.post sent JSON and parsed the JSON reply');
