import { strict as assert } from 'node:assert';
import express from 'express';
import { createApp } from '../src/app.mjs';
import { V4_ROUTE_PATHS, registerOrThrow } from '../src/v4-habits.mjs';

// 1. The express 4 path strings are rejected at registration time, not at
//    request time. This is the failure the migration actually presents as.
const legacy = express();

const optErr = registerOrThrow(legacy, V4_ROUTE_PATHS.optionalParam);
assert.ok(optErr instanceof TypeError, "express 5 must reject '/users/:id?'");
assert.match(optErr.message, /Unexpected \? at index \d+: \/users\/:id\?/);

const bareErr = registerOrThrow(legacy, V4_ROUTE_PATHS.bareWildcard);
assert.ok(bareErr instanceof TypeError, "express 5 must reject '*'");
assert.match(bareErr.message, /Missing parameter name at index \d+/);

const trailErr = registerOrThrow(legacy, V4_ROUTE_PATHS.trailingWildcard);
assert.ok(trailErr instanceof TypeError, "express 5 must reject '/files/*'");
assert.match(trailErr.message, /Missing parameter name at index \d+/);

const rxErr = registerOrThrow(legacy, V4_ROUTE_PATHS.inlineRegex);
assert.ok(rxErr instanceof TypeError, "express 5 must reject '/rx/:id(\\d+)'");
assert.match(rxErr.message, /Unexpected \( at index \d+/);

// 2. The express 5 spellings register and serve.
const server = await new Promise((resolve) => {
  const s = createApp().listen(0, '127.0.0.1', () => resolve(s));
});
const { port } = server.address();
const get = async (path) => {
  const res = await fetch(`http://127.0.0.1:${port}${path}`);
  const text = await res.text();
  // A missed route answers with express's HTML 404 page, so parse defensively
  // and let the status assertions below report the real problem.
  let body = null;
  try {
    body = JSON.parse(text);
  } catch {
    body = null;
  }
  return { status: res.status, body };
};

const withParam = await get('/users/42');
const withoutParam = await get('/users');
const splat = await get('/files/deep/nested/report.pdf');
const query = await get('/search?q=widget&page=2');
server.close();

// '/users{/:id}' covers both shapes of the old ':id?' route.
assert.equal(withParam.status, 200);
assert.deepEqual(withParam.body, { route: 'users', id: '42' });
assert.equal(withoutParam.status, 200, "'/users{/:id}' must still match a bare /users");
assert.deepEqual(withoutParam.body, { route: 'users', id: null });

// '/files/*splat' captures the rest of the path as named segments.
assert.equal(splat.status, 200);
assert.deepEqual(splat.body.segments, ['deep', 'nested', 'report.pdf']);
assert.equal(splat.body.path, 'deep/nested/report.pdf');

// req.query is a read-only getter that re-parses on every access.
assert.equal(query.status, 200);
assert.equal(typeof query.body.assignMessage, 'string', 'assigning req.query must throw');
assert.match(query.body.assignMessage, /only a getter/);
assert.equal(query.body.mutateMessage, null, 'mutating the returned object does not throw');
assert.equal(query.body.writeSurvived, null, 'but the write is discarded on the next read');
assert.deepEqual(query.body.normalized, { q: 'widget', page: 2 });

console.log('CONTRACT PASS: express 5 rejects v4 route syntax; {/:id} and *splat route correctly');
