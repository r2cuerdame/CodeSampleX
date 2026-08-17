import assert from "node:assert/strict";
import fg from "fast-glob";


const patterns = ["fixtures/**/*.js", "fixtures/src/**/*.js"];
const options = {
  onlyFiles: true,
  ignore: ["**/*.test.js"],
};
const expected = ["fixtures/src/a.js", "fixtures/src/nested/c.js"];

const asyncResult = fg(patterns, options);
assert.equal(typeof asyncResult.then, "function");
assert.deepEqual((await asyncResult).sort(), expected);

const syncResult = fg.sync(patterns, options);
assert.ok(Array.isArray(syncResult));
assert.deepEqual(syncResult.sort(), expected);

const entryStream = fg.stream(patterns, {
  ...options,
  objectMode: true,
  stats: true,
});
assert.equal(typeof entryStream.on, "function");
assert.equal(typeof entryStream.then, "undefined");

const entries = [];
for await (const entry of entryStream) {
  assert.equal(typeof entry.path, "string");
  assert.equal(entry.stats.isFile(), true);
  entries.push(entry.path);
}
assert.deepEqual(entries.sort(), expected);

assert.equal(new Set(entries).size, entries.length);
assert.ok(entries.every((path) => !path.includes(".hidden")));

console.log("CONTRACT PASS: fast-glob 3.3.3 async, sync, and stream modes agree");
