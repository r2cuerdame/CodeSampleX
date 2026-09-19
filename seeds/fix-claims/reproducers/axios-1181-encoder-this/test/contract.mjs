import http from 'node:http';
import { strict as assert } from 'node:assert';
import axios from 'axios';
// Claim 1 (1.18.1): a custom encoder is called with `this` bound to the AxiosURLSearchParams instance.
let seenThis = 'unset';
const srv = http.createServer((req, res) => res.end('ok'));
await new Promise((r) => srv.listen(0, '127.0.0.1', r));
await axios.get(`http://127.0.0.1:${srv.address().port}/`, {
  params: { q: 'a b' },
  paramsSerializer: { encode(value) { seenThis = this; return encodeURIComponent(value); } },
});
srv.close();
assert.ok(seenThis !== undefined && seenThis !== null, 'encoder received no `this`');
assert.equal(typeof seenThis.toString, 'function');
assert.ok(Array.isArray(seenThis._pairs), 'encoder `this` is not the AxiosURLSearchParams instance');
console.log('CONTRACT PASS');
