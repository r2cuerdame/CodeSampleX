import { strict as assert } from 'node:assert';
import { staticHTML, hydratableHTML } from '../src/index.mjs';

const html = staticHTML({ title: 'CodeSampleX', note: 'evidence, not guesses' });
assert.match(html, /<div class="card">/);
assert.match(html, /<h2>CodeSampleX<\/h2>/);
assert.match(html, /evidence, not guesses/);

// React escapes interpolated text; it is not a raw template engine.
const escaped = staticHTML({ title: '<script>alert(1)</script>', note: 'x' });
assert.ok(!escaped.includes('<script>'), escaped);
assert.match(escaped, /&lt;script&gt;/);

// The two renderers differ in exactly one way that matters.
const hydratable = hydratableHTML({ title: 'a', note: 'b' });
assert.ok(hydratable.includes('<!--'), 'renderToString emits hydration markers');
assert.ok(!staticHTML({ title: 'a', note: 'b' }).includes('<!--'));

console.log('CONTRACT PASS: react-dom/server rendered, escaped and distinguished both renderers');
