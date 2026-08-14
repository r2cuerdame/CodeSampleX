import assert from "node:assert/strict";

import { startVitest, version as vitestVersion } from "vitest/node";

/**
 * Runs the suite in-process through vitest's node API instead of shelling out
 * to `npx vitest run`.
 *
 * Both work with no network, and that was measured rather than assumed:
 * `npm ci --ignore-scripts` still writes node_modules/.bin, so npx finds the
 * local binary and never reaches the registry, and a node-environment vitest
 * run makes no network call of its own. `npx vitest run` is the honest answer
 * to "does my suite run offline".
 *
 * This file exists for the two things an exit code cannot express.
 *
 * The first is a skipped test. With one of the five tests changed to it.skip,
 * `npx vitest run` prints "4 passed | 1 skipped" and exits 0 — a green run,
 * and a receipt claiming five behaviours were verified when four were. The
 * same file through this runner exits 1 on `actual: 'skipped', expected:
 * 'passed'`.
 *
 * The second is the negative run at the bottom. What vitest does with an
 * assertion whose await was forgotten cannot be asserted from inside a passing
 * test, so a suite that forgets one is run on purpose and its failure is what
 * gets asserted.
 *
 * The empty case needs no help: startVitest prints "No test files found,
 * exiting with code 1" and sets process.exitCode to 1 before it returns —
 * measured both with a filter that matches nothing and with an include that
 * matches nothing — so a renamed suite file already fails either way.
 */

const vitest = await startVitest("test", [], { watch: false });

// getTestModules/allTests is the reported-task API; it is populated after the
// run finishes, and startVitest has already closed the server by this point
// because watch is off.
const modules = vitest.state.getTestModules();
const tests = modules.flatMap((module) => [...module.children.allTests()]);
const states = tests.map((test) => [test.fullName, test.result().state]);

assert.equal(modules.length, 1, `expected one test module, got ${modules.length}`);
assert.equal(tests.length, 5, `expected five tests, got ${tests.length}`);

for (const [name, state] of states) {
  assert.equal(state, "passed", `${name}: ${state}`);
}

// vitest's own accounting and the module-level state, read independently of
// the per-test results above. A test that fails inside a hook, or a suite that
// errors while collecting, shows up here rather than in a test's result state.
assert.equal(vitest.state.getCountOfFailedTests(), 0);
assert.equal(modules[0].state(), "passed");

// A failing test sets process.exitCode from inside vitest, so leaving it dirty
// here would make node exit non-zero and this stage fail. Asserting it keeps
// the two signals from disagreeing silently.
assert.equal(process.exitCode ?? 0, 0);

// The negative run. A forgotten await is usually described as passing while
// asserting nothing; on vitest 4.1.10 it does not, and the two halves of
// test/not-awaited-rejects.mjs are red in two different ways.
console.log("\n### negative run: test/not-awaited-rejects.mjs is supposed to fail");

const negative = await startVitest("test", [], {
  watch: false,
  include: ["test/not-awaited-rejects.mjs"],
});
const negativeTests = negative.state
  .getTestModules()
  .flatMap((module) => [...module.children.allTests()]);
assert.equal(negativeTests.length, 2, `expected two tests, got ${negativeTests.length}`);

const syncTest = negativeTests.find((test) => test.fullName.startsWith("sync"));
const asyncTest = negativeTests.find((test) => test.fullName.startsWith("async"));
assert.ok(syncTest && asyncTest, "expected one sync and one async test");

// .rejects registers its promise on the running task. A sync test function
// returns before that promise settles, so it is still registered when the
// runner awaits the task's promises, and its rejection is reported as this
// test's failure. The real rejection message is inside the error, which is the
// proof that the comparison ran rather than being skipped.
assert.equal(syncTest.result().state, "failed", "sync test should have failed");
const [syncError] = syncTest.result().errors ?? [];
assert.match(syncError.message, /not the real message/);
assert.match(syncError.message, /name is required/);

// The async test function takes enough microtask turns that the assertion
// settles and unregisters itself first, so the runner finds nothing to await:
// the test is reported PASSED and the failure arrives as the run's unhandled
// error instead. Green test, red run — and any tooling that reads only test
// states calls this a pass.
assert.equal(asyncTest.result().state, "passed", "async test should have been reported passed");
const unhandled = negative.state.getUnhandledErrors();
assert.equal(unhandled.length, 1, `expected one unhandled error, got ${unhandled.length}`);
assert.match(String(unhandled[0].message), /not the real message either/);

// The failing suite set process.exitCode itself, which is the only reason the
// async half is visible from outside at all. Clearing it is what lets this
// stage report on the assertions above rather than on the suite that was
// supposed to fail.
assert.equal(process.exitCode, 1);
process.exitCode = 0;

console.log(`\ncontract ok: vitest ${vitestVersion}, ${tests.length} tests passed`);
for (const [name] of states) console.log(`  - ${name}`);
console.log("  - a forgotten await fails the test when the test function is sync");
console.log("  - a forgotten await in an async test function leaves the test green and the run red");
