import assert from "node:assert/strict";
import { Marked, marked } from "marked";

const markdown = "# Heading\n\nSome **bold** text.\n";

const synchronous = marked.parse(markdown);
assert.equal(typeof synchronous, "string");
assert.match(synchronous, /<h1>Heading<\/h1>/);
assert.match(synchronous, /<strong>bold<\/strong>/);

const explicitlyAsync = marked.parse(markdown, { async: true });
assert.ok(explicitlyAsync instanceof Promise);
assert.equal(await explicitlyAsync, synchronous);

const delayed = new Marked();
delayed.use({
  async: true,
  walkTokens(token) {
    if (token.type !== "heading") return undefined;
    return Promise.resolve().then(() => {
      token.depth = 3;
    });
  },
});

const delayedResult = delayed.parse("# Delayed\n");
assert.ok(delayedResult instanceof Promise);
assert.match(await delayedResult, /<h3>Delayed<\/h3>/);

const warnings = [];
const originalWarn = console.warn;
let ignoredOverride;
try {
  console.warn = (...parts) => warnings.push(parts.join(" "));
  ignoredOverride = delayed.parse("# Still async\n", { async: false });
} finally {
  console.warn = originalWarn;
}
assert.ok(ignoredOverride instanceof Promise);
assert.match(await ignoredOverride, /<h3>Still async<\/h3>/);
assert.equal(warnings.length, 1);
assert.match(warnings[0], /async option was set to true/i);
assert.match(warnings[0], /async: false option.*will be ignored/i);

const raw = marked.parse('Hello <img src=x onerror="alert(1)">');
assert.match(raw, /<img src=x onerror="alert\(1\)">/);

console.log("CONTRACT PASS: marked 13.0.2 sync, async override, and raw HTML behavior");
