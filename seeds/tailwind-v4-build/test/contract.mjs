import fs from 'node:fs';
import { strict as assert } from 'node:assert';
import { build } from '../src/index.mjs';

// Tailwind scans files for class names; give it one to find.
fs.writeFileSync('markup.html', '<div class="p-4 text-brand"></div>');

const css = await build(`
@import "tailwindcss" source(none);
@source "./markup.html";
@theme { --color-brand: #ff5722; }
`);

assert.ok(css.includes('padding'), 'p-4 should emit a padding utility');
assert.ok(css.includes('#ff5722') || css.includes('--color-brand'),
  'the @theme token must reach the output');
// Nothing scanned uses it, so it must not be emitted.
assert.ok(!css.includes('.rotate-45'), 'unused utilities must not be emitted');

console.log('CONTRACT PASS: tailwind v4 built CSS from CSS-first config');
