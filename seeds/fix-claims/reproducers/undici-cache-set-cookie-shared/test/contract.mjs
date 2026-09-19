import http from 'node:http';
import { strict as assert } from 'node:assert';
import { Agent, interceptors, request } from 'undici';
// Claim (8.10.2): a shared cache (the interceptor's default) does not store or replay a
// cacheable response that carries Set-Cookie, so one caller's cookie is never re-served
// to the next caller hitting the same key.
let hits = 0;
const server = http.createServer((req, res) => {
  hits++;
  res.writeHead(200, {
    'cache-control': 'public, max-age=60',
    'set-cookie': `session=user${hits}; Path=/`,
    'content-type': 'text/plain',
  });
  res.end(`user${hits}`);
});
await new Promise((r) => server.listen(0, '127.0.0.1', r));
const origin = `http://127.0.0.1:${server.address().port}`;
const dispatcher = new Agent().compose(interceptors.cache());
const seen = [];
for (let i = 0; i < 2; i++) {
  const res = await request(`${origin}/me`, { dispatcher });
  await res.body.text();
  seen.push(String(res.headers['set-cookie']));
}
await dispatcher.close();
server.close();
try {
  assert.equal(hits, 2, `the origin saw ${hits} request(s); the second caller was served from the shared cache`);
  assert.notEqual(seen[0], seen[1], `both callers received ${seen[0]}`);
} catch (e) {
  console.error(e.message);
  process.exit(1);
}
console.log('CONTRACT PASS');
