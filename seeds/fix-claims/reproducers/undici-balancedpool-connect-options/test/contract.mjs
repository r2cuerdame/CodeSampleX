import http from 'node:http';
import dns from 'node:dns';
import { strict as assert } from 'node:assert';
import { BalancedPool, Pool } from 'undici';
// Claim (8.10.2): BalancedPool forwards function-valued connect options to each upstream.
// A custom lookup (the same shape as a custom checkServerIdentity) used to be dropped by
// the JSON clone of the options, so the connection was made without it.
let viaBalanced = 0;
let viaPool = 0;
const server = http.createServer((req, res) => res.end('ok'));
await new Promise((r) => server.listen(0, '127.0.0.1', r));
const origin = `http://localhost:${server.address().port}`;
const lookupFor = (counter) => (hostname, options, cb) => { counter(); return dns.lookup(hostname, options, cb); };
const balanced = new BalancedPool([origin], { connect: { lookup: lookupFor(() => viaBalanced++) } });
const pool = new Pool(origin, { connect: { lookup: lookupFor(() => viaPool++) } });
for (const d of [balanced, pool]) {
  const res = await d.request({ path: '/', method: 'GET' });
  await res.body.text();
  await d.close();
}
server.close();
try {
  assert.ok(viaPool > 0, 'Pool did not use the custom lookup; the reproducer is wrong, not the package');
  assert.ok(viaBalanced > 0, 'BalancedPool connected without the custom lookup: the function-valued connect option was dropped');
} catch (e) {
  console.error(e.message);
  process.exit(1);
}
console.log('CONTRACT PASS');
