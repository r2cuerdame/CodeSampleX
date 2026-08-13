import { strict as assert } from 'node:assert';
import { hydratableHTML, staticHTML, escaped } from '../src/index.mjs';

const props = { user: 'codesamplex', count: 3 };
const hydratable = hydratableHTML(props);
const staticOnly = staticHTML(props);

// Both carry the same visible text.
for (const html of [hydratable, staticOnly]) {
  assert.match(html, /<p>/);
  assert.match(html, /signed in as/);
  assert.match(html, /codesamplex/);
}

// The difference: separators between adjacent text nodes.
assert.ok(hydratable.includes('<!-- -->'),
  `renderToString should separate adjacent text nodes: ${hydratable}`);
assert.ok(!staticOnly.includes('<!-- -->'),
  `renderToStaticMarkup should omit them: ${staticOnly}`);
assert.notEqual(hydratable, staticOnly);

// Strip the separators and the two agree exactly — the markup is the same
// document, only the hydration boundaries differ.
assert.equal(hydratable.split('<!-- -->').join(''), staticOnly);

// React escapes interpolated text; it is not a raw template engine.
const xss = escaped('<script>alert(1)</script>');
assert.ok(!xss.includes('<script>'), xss);
assert.match(xss, /&lt;script&gt;/);

console.log('CONTRACT PASS: renderToString kept text boundaries that renderToStaticMarkup drops');
