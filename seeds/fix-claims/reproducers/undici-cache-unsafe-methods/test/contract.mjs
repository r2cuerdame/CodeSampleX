import http from 'node:http';
import { strict as assert } from 'node:assert';
import { Agent, interceptors, request } from 'undici';
// Claim (8.10.2): the cache interceptor neither stores nor replays responses to unsafe
// methods. A heuristically cacheable 404 with an explicit max-age to a POST used to be
// served from the cache on the next identical POST, so the origin never saw it.
let posts = 0;
const server = http.createServer((req, res) => {
  if (req.method === 'POST') posts++;
  res.writeHead(404, { 'cache-control': 'public, max-age=60', 'content-type': 'text/plain' });
  res.end(`hit ${posts}`);
});
await new Promise((r) => server.listen(0, '127.0.0.1', r));
const origin = `http://127.0.0.1:${server.address().port}`;
const dispatcher = new Agent().compose(interceptors.cache());
const bodies = [];
for (let i = 0; i < 2; i++) {
  const res = await request(`${origin}/order`, { dispatcher, method: 'POST', body: 'x' });
  bodies.push(await res.body.text());
}
await dispatcher.close();
server.close();
try {
  assert.equal(posts, 2, `the origin saw ${posts} POST(s); the second was served from the cache (bodies: ${bodies.join(' / ')})`);
  assert.notEqual(bodies[0], bodies[1]);
} catch (e) {
  console.error(e.message);
  process.exit(1);
}
console.log('CONTRACT PASS');
