import Ajv from 'ajv';

// ajv 8 turns strict mode ON by default: any keyword it does not recognize
// throws at compile time rather than being ignored as in ajv 6/7. Turning
// strict off hides real typos, so declaring the keyword is the safer fix.
// Errors also changed shape in v8 — dataPath became instancePath.
export function compileStrict(schema) {
  const ajv = new Ajv({ strict: true });
  return ajv.compile(schema);
}

export function compileWithKeyword(schema, keyword) {
  const ajv = new Ajv({ strict: true });
  ajv.addKeyword({ keyword, schemaType: 'string' });
  return ajv.compile(schema);
}
