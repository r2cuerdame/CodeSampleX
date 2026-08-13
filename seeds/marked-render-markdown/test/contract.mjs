import { strict as assert } from 'node:assert';
import { render } from '../src/index.mjs';

const html = render('# Title\n\nSome **bold** text.\n\n```js\nconst a = 1;\n```\n');
assert.match(html, /<h1[^>]*>Title<\/h1>/);
assert.match(html, /<strong>bold<\/strong>/);
assert.match(html, /<code class="language-js">/);

// The part people get wrong: raw HTML passes straight through.
const raw = render('Hello <img src=x onerror="alert(1)">');
assert.ok(raw.includes('onerror'), 'marked does not sanitize; it never claimed to');

console.log('CONTRACT PASS: marked rendered markdown and left HTML untouched, as documented');
