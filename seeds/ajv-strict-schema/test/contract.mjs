import { strict as assert } from 'node:assert';
import { compileStrict, compileWithKeyword } from '../src/index.mjs';

const schema = {
  type: 'object',
  properties: { name: { type: 'string' }, port: { type: 'integer' } },
  required: ['name'],
  'x-unit': 'milliseconds', // annotation ajv does not know
};

assert.throws(() => compileStrict(schema), /strict mode/i);

const validate = compileWithKeyword(schema, 'x-unit');
assert.equal(validate({ name: 'csx', port: 8080 }), true);

assert.equal(validate({ port: 'not-an-integer' }), false);
const codes = validate.errors.map((e) => e.keyword);
assert.ok(codes.includes('required'));
assert.equal(typeof validate.errors[0].instancePath, 'string'); // v8 name
console.log('CONTRACT PASS: ajv 8 strict mode explained and the schema compiled');
