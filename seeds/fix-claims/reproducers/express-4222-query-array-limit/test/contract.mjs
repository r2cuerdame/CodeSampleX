import http from 'node:http';
import { strict as assert } from 'node:assert';
import express from 'express';
const app = express();
app.get('/', (req, res) => res.json(req.query));
const srv = app.listen(0, '127.0.0.1');
await new Promise((r) => srv.once('listening', r));
const qs = Array.from({ length: 25 }, (_, i) => `a=${i}`).join('&');
const body = await new Promise((resolve, reject) => {
  http.get(`http://127.0.0.1:${srv.address().port}/?${qs}`, (res) => {
    let data = ''; res.setEncoding('utf8'); res.on('data', (c) => (data += c)); res.on('end', () => resolve(JSON.parse(data)));
  }).on('error', reject);
});
srv.close();
assert.ok(Array.isArray(body.a), `req.query.a is ${typeof body.a}, not an array: ${JSON.stringify(body.a).slice(0, 80)}`);
assert.equal(body.a.length, 25);
console.log('CONTRACT PASS');
