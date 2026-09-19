import { strict as assert } from 'node:assert';
import { z } from 'zod';
// Claim (4.6.3): z.properties() is a check again, not a standalone schema.
class Thing { constructor(n) { this.n = n; } }
const shape = { n: z.number().int().positive() };
// The check form, unchanged across the boundary: it must reject a bad property and leave a good value untouched.
const viaMethod = z.instanceof(Thing).properties(shape);
const viaCheck = z.instanceof(Thing).check(z.properties(shape));
for (const s of [viaMethod, viaCheck]) {
  const ok = new Thing(3);
  assert.equal(s.parse(ok), ok);
  assert.equal(s.safeParse(new Thing(-1)).success, false);
}
// The standalone schema role shipped in 4.6.0 is gone: z.properties(shape) is a check, and a check has no parse.
const check = z.properties(shape);
assert.equal(typeof check.parse, 'undefined', 'z.properties() still returns a standalone schema with .parse');
console.log('CONTRACT PASS');
