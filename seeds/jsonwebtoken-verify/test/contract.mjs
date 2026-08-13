import { strict as assert } from 'node:assert';
import jwt from 'jsonwebtoken';
import { sign, verify } from '../src/index.mjs';

const secret = 'test-secret-not-a-real-key';
const token = sign({ sub: 'peer-1', role: 'seeder' }, secret);
const claims = verify(token, secret);
assert.equal(claims.sub, 'peer-1');
assert.equal(claims.role, 'seeder');
assert.ok(claims.exp > claims.iat);

// Tampering invalidates the signature.
const [h, p, s] = token.split('.');
const forged = [h, Buffer.from(JSON.stringify({ sub: 'admin' })).toString('base64url'), s].join('.');
assert.throws(() => verify(forged, secret), /invalid signature/);

// Expiry is enforced, with a distinguishable error.
const stale = sign({ sub: 'x' }, secret, { expiresIn: -10 });
assert.throws(() => verify(stale, secret), (e) => e.name === 'TokenExpiredError');

// alg:none is refused because the verifier pinned its algorithms.
const none = jwt.sign({ sub: 'x' }, null, { algorithm: 'none' });
assert.throws(() => verify(none, secret));

// decode does not verify anything — it is not a security check.
assert.equal(jwt.decode(forged).sub, 'admin');

console.log('CONTRACT PASS: jwt verification pinned algorithms and rejected forgeries');
