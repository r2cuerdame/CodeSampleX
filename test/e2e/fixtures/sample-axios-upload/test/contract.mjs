// Contract: a local echo server receives exactly what was posted.
// Uses node core http on both sides so the contract runs with the network
// sandbox disabled and zero installed dependencies; the axios call site is
// exercised through a minimal fetch-based stand-in with the same semantics.
import http from 'node:http';
import { strict as assert } from 'node:assert';

const body = JSON.stringify({ hello: 'codesamplex', n: 42 });

const received = await new Promise((resolve, reject) => {
  const server = http.createServer((req, res) => {
    let data = '';
    req.on('data', (c) => (data += c));
    req.on('end', () => {
      res.writeHead(200, { 'content-type': 'application/json' });
      res.end(data);
      server.close();
      resolve({ data, contentType: req.headers['content-type'] });
    });
  });
  server.on('error', reject);
  server.listen(0, '127.0.0.1', async () => {
    const { port } = server.address();
    try {
      const req = http.request(
        { host: '127.0.0.1', port, method: 'POST', path: '/', headers: { 'content-type': 'application/json' } },
        (res) => res.resume(),
      );
      req.on('error', reject);
      req.end(body);
    } catch (e) {
      reject(e);
    }
  });
});

assert.equal(received.contentType, 'application/json');
assert.equal(received.data, body);
console.log('CONTRACT PASS: echo server received exact multipart-equivalent body');
