import { strict as assert } from 'node:assert';
import Decimal from 'decimal.js';
import { sum, toCents } from '../src/index.mjs';

// The problem, stated:
assert.notEqual(0.1 + 0.2, 0.3);

// The fix:
assert.equal(sum(['0.1', '0.2']).toString(), '0.3');
assert.ok(sum(['0.1', '0.2']).equals(new Decimal('0.3')));

assert.equal(sum(['19.99', '5.01', '0.005']).toFixed(3), '25.005');
assert.equal(toCents('25.005'), '25.01'); // half-up, explicitly
assert.equal(toCents('25.004'), '25.00');

console.log('CONTRACT PASS: decimal.js summed and rounded money exactly');
