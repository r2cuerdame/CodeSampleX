import { v4 as uuidv4, validate as uuidValidate, version as uuidVersion } from 'uuid';

// uuid dropped its default export: `import uuid from 'uuid'` gives undefined
// and `uuid.v4()` then throws "uuid.v4 is not a function". The named exports
// are the supported surface, and validate/version let you check input
// without a hand-written regexp.
export function newId() {
  return uuidv4();
}

export function describe(id) {
  return { valid: uuidValidate(id), version: uuidValidate(id) ? uuidVersion(id) : null };
}
