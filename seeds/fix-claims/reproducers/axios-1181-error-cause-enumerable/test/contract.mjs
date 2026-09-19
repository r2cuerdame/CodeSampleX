import { strict as assert } from 'node:assert';
import axios, { AxiosError } from 'axios';
// Claim (1.18.1): AxiosError#cause is non-enumerable, so an own-property walk of a
// network error (whose cause holds circular socket internals) can be JSON-serialised.
let err;
try {
  await axios.get('http://127.0.0.1:1/', { timeout: 2000 });
} catch (e) { err = e; }
assert.ok(err instanceof AxiosError, 'expected an AxiosError');
assert.ok(err.cause, 'the network error should carry a cause');
assert.equal(Object.prototype.propertyIsEnumerable.call(err, 'cause'), false, 'cause is enumerable');
// The logger-style walk from the upstream report.
JSON.stringify(Object.fromEntries(Object.entries(err)));
console.log('CONTRACT PASS');
