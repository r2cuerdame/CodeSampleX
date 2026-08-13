import { strict as assert } from 'node:assert';
import { loggerTo } from '../src/index.mjs';

const lines = [];
const collector = { write: (chunk) => lines.push(chunk) };

const log = loggerTo(collector);
log.info({ sampleId: 'sha256:abc', attempt: 2 }, 'verification started');

assert.equal(lines.length, 1);
const entry = JSON.parse(lines[0]);
assert.equal(entry.level, 30); // info
assert.equal(entry.msg, 'verification started');
assert.equal(entry.sampleId, 'sha256:abc');
assert.equal(entry.attempt, 2);
assert.equal(entry.pid, undefined); // base: null
assert.equal(typeof entry.time, 'number');
console.log('CONTRACT PASS: pino wrote one structured JSON line to a custom stream');
