import { strict as assert } from 'node:assert';
import { typeFor, extensionFor } from '../src/index.mjs';

assert.equal(typeFor('reports/data.json'), 'application/json');
assert.equal(typeFor('site.css'), 'text/css');
assert.equal(typeFor('archive.tar.gz'), 'application/gzip');
assert.equal(typeFor('mystery.zzz'), null);

assert.equal(extensionFor('text/plain'), 'txt');
assert.equal(extensionFor('application/json'), 'json');
console.log('CONTRACT PASS: mime v4 getType/getExtension resolved both directions');
