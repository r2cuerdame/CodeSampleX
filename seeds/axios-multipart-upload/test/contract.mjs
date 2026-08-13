import http from 'node:http';
import { strict as assert } from 'node:assert';
import { uploadFile } from '../src/index.mjs';

let seen = null;
const server = http.createServer((req, res) => {
  const chunks = [];
  req.on('data', (c) => chunks.push(c));
  req.on('end', () => {
    seen = { type: req.headers['content-type'], body: Buffer.concat(chunks).toString('utf8') };
    res.writeHead(200, { 'content-type': 'application/json' });
    res.end(JSON.stringify({ received: true }));
  });
});
await new Promise((r) => server.listen(0, '127.0.0.1', r));
const { port } = server.address();

const out = await uploadFile(`http://127.0.0.1:${port}/upload`, 'notes.txt', 'hello from csx');
server.close();

assert.equal(out.received, true);
assert.match(seen.type, /^multipart\/form-data; boundary=/);
assert.match(seen.body, /name="file"; filename="notes.txt"/);
assert.match(seen.body, /hello from csx/);
assert.match(seen.body, /name="kind"/);
console.log('CONTRACT PASS: axios posted native FormData as multipart with a boundary');
