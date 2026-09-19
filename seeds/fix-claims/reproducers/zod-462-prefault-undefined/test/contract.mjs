import { strict as assert } from 'node:assert';
import { z } from 'zod';
// Claim (4.6.2): prefault(undefined) on an object key yields the key with an undefined value.
const schema = z.object({ a: z.union([z.string(), z.undefined()]).prefault(() => undefined) });
const parsed = schema.parse({});
assert.ok(Object.prototype.hasOwnProperty.call(parsed, 'a'), `key "a" missing from ${JSON.stringify(parsed)}`);
assert.equal(parsed.a, undefined);
console.log('CONTRACT PASS');
