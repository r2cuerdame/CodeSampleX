import { strict as assert } from 'node:assert';
import { lint } from '../src/index.mjs';

const bad = await lint('export const same = (a, b) => a == b;\n');
const ids = bad.messages.map((m) => m.ruleId);
assert.ok(ids.includes('eqeqeq'), JSON.stringify(bad.messages));
const eq = bad.messages.find((m) => m.ruleId === 'eqeqeq');
assert.equal(eq.severity, 2);
assert.equal(eq.line, 1);
assert.equal(bad.errorCount, 1);

const warn = await lint('const unused = 1;\nexport const ok = (a, b) => a === b;\n');
assert.equal(warn.errorCount, 0);
assert.ok(warn.warningCount >= 1, 'severity 1 is a warning, not an error');

const clean = await lint('export const ok = (a, b) => a === b;\n');
assert.equal(clean.errorCount, 0);
assert.equal(clean.warningCount, 0);

console.log('CONTRACT PASS: eslint flat config linted inline source');
