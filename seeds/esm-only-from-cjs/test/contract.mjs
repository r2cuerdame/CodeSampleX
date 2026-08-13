import { strict as assert } from 'node:assert';
import { spawnSync } from 'node:child_process';
import { createRequire } from 'node:module';
import { fileURLToPath } from 'node:url';

// Load the sample the way a CommonJS project would: a genuine require() of a
// genuine CJS module. `require` does not exist inside an .mjs file, so
// createRequire hands us the real CommonJS loader.
const require = createRequire(import.meta.url);

// ---------------------------------------------------------------------------
// 1. The require() pattern really is broken on this Node.
// ---------------------------------------------------------------------------
const brokenPath = fileURLToPath(new URL('../src/broken-require.js', import.meta.url));
const broken = spawnSync(process.execPath, [brokenPath], { encoding: 'utf8' });

assert.notEqual(broken.status, 0, 'require() of an ESM-only package must still fail');
assert.match(broken.stderr, /ERR_REQUIRE_ASYNC_MODULE/);
assert.match(broken.stderr, /require\(\) cannot be used on an ESM graph with top-level await/);

// The quieter half: require() of an ESM-only package *without* top-level await
// no longer throws on Node >= 22.12 -- it hands back the module namespace, so
// the failure surfaces later as a TypeError at the call site.
assert.match(broken.stdout, /p-limit require\(\) returned: object/);
assert.match(broken.stdout, /calling it: TypeError: pLimit is not a function/);

// ---------------------------------------------------------------------------
// 2. The dynamic-import path loads the package and exposes the real default.
// ---------------------------------------------------------------------------
const { loadYoga, rowLayout, runLimited } = require('../src/index.js');

const ns = await loadYoga();
assert.equal(await loadYoga(), ns, 'the cached import() promise yields one namespace');
assert.equal(ns.Node, undefined, 'Yoga.Node is on the default export, not the namespace');
assert.equal(typeof ns.default.Node.create, 'function');

// ---------------------------------------------------------------------------
// 3. It computes a real flexbox layout: 3 flex-grow columns in 240x60 with a
//    12px gap => (240 - 2*12) / 3 = 72 wide each, stretched to full height.
// ---------------------------------------------------------------------------
const boxes = await rowLayout({ width: 240, height: 60, count: 3, gap: 12 });
assert.deepEqual(boxes, [
  { left: 0, top: 0, width: 72, height: 60 },
  { left: 84, top: 0, width: 72, height: 60 },
  { left: 168, top: 0, width: 72, height: 60 },
]);

// ---------------------------------------------------------------------------
// 4. The second ESM-only package works through the same door, and its
//    behaviour is observable: at most `concurrency` tasks are ever in flight.
// ---------------------------------------------------------------------------
let inFlight = 0;
let peak = 0;
const tasks = [1, 2, 3, 4, 5, 6].map((n) => async () => {
  inFlight += 1;
  peak = Math.max(peak, inFlight);
  await new Promise((resolve) => setTimeout(resolve, 10));
  inFlight -= 1;
  return n * 10;
});

const results = await runLimited(tasks, 2);
assert.deepEqual(results, [10, 20, 30, 40, 50, 60], 'results keep input order');
assert.equal(peak, 2, 'p-limit capped concurrency at 2 (it would be 6 uncapped)');
assert.equal(inFlight, 0);

console.log('CONTRACT PASS: await import() loads ESM-only packages from CommonJS; require() throws ERR_REQUIRE_ASYNC_MODULE');
