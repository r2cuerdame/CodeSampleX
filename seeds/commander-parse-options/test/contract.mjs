import { strict as assert } from 'node:assert';
import { parseArgs } from '../src/index.mjs';

const explicit = parseArgs(['--port', '8080', '--verbose', 'server']);
assert.equal(explicit.options.port, '8080');
assert.equal(explicit.options.verbose, true);
assert.deepEqual(explicit.args, ['server']);

const defaults = parseArgs([]);
assert.equal(defaults.options.port, '3000'); // declared default
assert.equal(defaults.options.verbose, undefined);
console.log('CONTRACT PASS: commander parsed an explicit argv with from: user');
