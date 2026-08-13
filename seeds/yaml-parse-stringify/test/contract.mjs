import { strict as assert } from 'node:assert';
import { load, roundTrip } from '../src/index.mjs';

const doc = load(`
name: codesamplex
port: 8080
debug: true
legacy: yes
tags:
  - compat
  - evidence
nested:
  retries: 3
`);

assert.equal(doc.name, 'codesamplex');
assert.equal(doc.port, 8080);
assert.equal(doc.debug, true);
assert.equal(doc.legacy, 'yes'); // YAML 1.2: not a boolean
assert.deepEqual(doc.tags, ['compat', 'evidence']);
assert.equal(doc.nested.retries, 3);

assert.deepEqual(roundTrip(doc), doc);
console.log('CONTRACT PASS: yaml parsed 1.2 types and round-tripped losslessly');
