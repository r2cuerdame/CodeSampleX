import assert from "node:assert/strict";

import { buildApp, listen } from "../src/app.mjs";

const { server, base } = await listen(buildApp());

try {
  const got = await fetch(`${base}/items/7?page=2`);
  assert.equal(got.status, 200);
  assert.deepEqual(await got.json(), { id: "7", q: { page: "2" } });

  const posted = await fetch(`${base}/echo`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ hello: "world" }),
  });
  assert.equal(posted.status, 201);
  assert.deepEqual(await posted.json(), { hello: "world" });

  // A handler that throws must become a 500, not an unhandled rejection
  // and not a dead process. This is the one most compatibility layers get
  // wrong, because it depends on how the request is dispatched.
  const boom = await fetch(`${base}/boom`);
  assert.equal(boom.status, 500);

  // And an unmatched path is Express's own 404, not the server's.
  const missing = await fetch(`${base}/nope`);
  assert.equal(missing.status, 404);

  // The server really is bound to loopback: the sandbox runs this stage
  // with no network at all, so anything else could not have answered.
  assert.match(base, /^http:\/\/127\.0\.0\.1:\d+$/);

  console.log("contract ok");
} finally {
  server.close();
}
