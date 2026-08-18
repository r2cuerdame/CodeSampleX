import assert from "node:assert/strict";
import { GlobalWindow, Window } from "happy-dom";

const hostDelay = (milliseconds) =>
  new Promise((resolve) => globalThis.setTimeout(resolve, milliseconds));
const probe = "__happyDOMGlobalWindowProbe";
delete globalThis[probe];

const shared = new GlobalWindow({ url: "https://example.com/" });
const isolated = new Window({ url: "https://example.com/" });

try {
  assert.equal(shared.Array, globalThis.Array);
  assert.equal(shared.Promise, globalThis.Promise);
  assert.equal(shared.Buffer, Buffer);
  assert.equal(shared.global, globalThis);

  const sharedArray = shared.eval(
    `globalThis.${probe} = "shared"; [1, 2, 3]`,
  );
  assert.equal(globalThis[probe], "shared");
  assert.ok(sharedArray instanceof Array);
  delete globalThis[probe];

  const isolatedArray = isolated.eval(
    `globalThis.${probe} = "isolated"; [1, 2, 3]`,
  );
  assert.equal(globalThis[probe], undefined);
  assert.equal(isolated.eval(`globalThis.${probe}`), "isolated");
  assert.notEqual(isolated.Array, globalThis.Array);
  assert.equal(isolatedArray instanceof Array, false);
  assert.ok(isolatedArray instanceof isolated.Array);

  let closeTimerFired = false;
  shared.setTimeout(() => {
    closeTimerFired = true;
  }, 10);
  shared.close();
  await hostDelay(40);
  assert.equal(shared.closed, false);
  assert.equal(closeTimerFired, true);

  const cleaned = new GlobalWindow();
  let cleanupTimerFired = false;
  cleaned.setTimeout(() => {
    cleanupTimerFired = true;
  }, 10);
  await cleaned.happyDOM.close();
  await hostDelay(40);
  assert.equal(cleaned.closed, true);
  assert.equal(cleanupTimerFired, false);

  console.log("CONTRACT PASS: happy-dom 20.11.2 GlobalWindow isolation and cleanup");
} finally {
  delete globalThis[probe];
  await shared.happyDOM.close();
  await isolated.happyDOM.close();
}
