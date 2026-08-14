import { strict as assert } from 'node:assert';
import { createRequire } from 'node:module';
import { createSecretKey } from 'node:crypto';
import { spawnSync } from 'node:child_process';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { SignJWT, jwtVerify, decodeJwt, UnsecuredJWT, errors } from 'jose';

import { hmacKey, signHS256, verifyHS256, isTokenRejection } from '../src/index.mjs';

const require = createRequire(import.meta.url);
const SECRET = 'a-32-byte-or-longer-test-secret-value';
const key = hmacKey(SECRET);

/** Captures the rejection so each assertion can name the exact error. */
async function rejection(promiseFn) {
  try {
    await promiseFn();
  } catch (err) {
    return err;
  }
  throw new Error('expected a rejection, got a resolved value');
}

// --- the happy path, and the return shape that trips migrations -------------
const token = await signHS256(
  { sub: 'peer-1', role: 'seeder' },
  key,
  { issuer: 'https://issuer.example', audience: 'csx-api' },
);
assert.equal(token.split('.').length, 3);

const result = await verifyHS256(token, key);
// jsonwebtoken returned the claims. jose returns a two-key envelope, so the
// claims live one level down and the signed header is handed back separately.
assert.deepEqual(Object.keys(result).sort(), ['payload', 'protectedHeader']);
assert.deepEqual(result.protectedHeader, { alg: 'HS256' });
assert.equal(result.payload.sub, 'peer-1');
assert.equal(result.payload.role, 'seeder');
assert.ok(result.payload.exp > result.payload.iat);
assert.equal(result.sub, undefined);

// --- the key is bytes, and the error for a string is outside the JOSE tree --
const stringKey = await rejection(() => jwtVerify(token, SECRET, { algorithms: ['HS256'] }));
assert.equal(stringKey.constructor, TypeError);
assert.match(
  stringKey.message,
  /^Key for the HS256 algorithm must be one of type CryptoKey, KeyObject, JSON Web Key, or Uint8Array\./,
);
// Measured, and the reason this assertion exists: there is no .code and it is
// not a JOSEError, so the catch block that classifies jose failures by code or
// by `instanceof JOSEError` treats the most common migration mistake as an
// unrelated crash.
assert.equal(stringKey.code, undefined);
assert.equal(isTokenRejection(stringKey), false);
assert.equal(stringKey instanceof errors.JOSEError, false);

// Signing refuses the same string, so the mistake cannot even produce a token.
const stringSign = await rejection(() => new SignJWT({}).setProtectedHeader({ alg: 'HS256' }).sign(SECRET));
assert.equal(stringSign.constructor, TypeError);

// All four forms that message names are real in v6, and all four verify the
// token that was signed with the Uint8Array. The webapi build did not drop
// node:crypto secret keys, which is the one people assume went away.
const secretBytes = Buffer.from(SECRET, 'utf8');
const viaKeyObject = await verifyHS256(token, createSecretKey(secretBytes));
assert.equal(viaKeyObject.payload.sub, 'peer-1');
const viaJwk = await verifyHS256(token, { kty: 'oct', k: secretBytes.toString('base64url') });
assert.equal(viaJwk.payload.sub, 'peer-1');
const viaCryptoKey = await verifyHS256(
  token,
  await crypto.subtle.importKey('raw', key, { name: 'HMAC', hash: 'SHA-256' }, false, ['verify']),
);
assert.equal(viaCryptoKey.payload.sub, 'peer-1');

// setProtectedHeader is not optional: no header, no signature.
const noHeader = await rejection(() => new SignJWT({ sub: 'x' }).sign(key));
assert.ok(noHeader instanceof errors.JWSInvalid);
assert.equal(noHeader.code, 'ERR_JWS_INVALID');

// --- expiry ------------------------------------------------------------------
const now = Math.floor(Date.now() / 1000);
const stale = await new SignJWT({ sub: 'x' })
  .setProtectedHeader({ alg: 'HS256' })
  .setIssuedAt(now - 600)
  .setExpirationTime(now - 300)
  .sign(key);

const expired = await rejection(() => verifyHS256(stale, key));
assert.ok(expired instanceof errors.JWTExpired);
assert.equal(expired.code, 'ERR_JWT_EXPIRED');
assert.equal(errors.JWTExpired.code, 'ERR_JWT_EXPIRED');
assert.equal(expired.claim, 'exp');
assert.equal(expired.reason, 'check_failed');
// jose attaches the decoded claims to the expiry error, which is how you tell
// *whose* session went stale without re-decoding the token yourself.
assert.equal(expired.payload.sub, 'x');
// The trap: JWTExpired is a sibling of JWTClaimValidationFailed, not a subclass
// of it, even though both describe a failed claim check and both carry .claim
// and .reason. A handler that catches JWTClaimValidationFailed to render "bad
// token" lets every expired token fall through to the generic 500 branch.
assert.equal(expired instanceof errors.JWTClaimValidationFailed, false);
assert.ok(errors.JWTExpired.prototype instanceof errors.JOSEError);
assert.ok(errors.JWTClaimValidationFailed.prototype instanceof errors.JOSEError);
// Expiry is a check against the clock, so a tolerance can wave it through.
const tolerated = await jwtVerify(stale, key, { algorithms: ['HS256'], clockTolerance: '10 minutes' });
assert.equal(tolerated.payload.sub, 'x');

// nbf is the other claim jwtVerify checks without being asked, and it lands on
// the other side of that split: a not-yet-valid token is a plain
// JWTClaimValidationFailed, the same class as a bad iss, while expiry is not.
const notYet = await new SignJWT({ sub: 'x' })
  .setProtectedHeader({ alg: 'HS256' })
  .setNotBefore(now + 600)
  .setExpirationTime(now + 1200)
  .sign(key);
const early = await rejection(() => verifyHS256(notYet, key));
assert.ok(early instanceof errors.JWTClaimValidationFailed);
assert.equal(early.code, 'ERR_JWT_CLAIM_VALIDATION_FAILED');
assert.equal(early.claim, 'nbf');
assert.equal(early.reason, 'check_failed');

// --- wrong key, and a tampered payload --------------------------------------
const wrongKey = await rejection(() => verifyHS256(token, hmacKey('a-32-byte-or-longer-WRONG-key-value!')));
assert.ok(wrongKey instanceof errors.JWSSignatureVerificationFailed);
assert.equal(wrongKey.code, 'ERR_JWS_SIGNATURE_VERIFICATION_FAILED');
assert.equal(wrongKey.message, 'signature verification failed');
assert.equal(isTokenRejection(wrongKey), true);

const [header, , signature] = token.split('.');
const forged = [header, Buffer.from(JSON.stringify({ sub: 'admin' })).toString('base64url'), signature].join('.');
const tampered = await rejection(() => verifyHS256(forged, key));
assert.ok(tampered instanceof errors.JWSSignatureVerificationFailed);
// decodeJwt is jose's jwt.decode: it reads the claims and verifies nothing, so
// it happily reports the forged subject. It is not a security check.
assert.equal(decodeJwt(forged).sub, 'admin');

// --- issuer and audience are checked only when you ask ----------------------
// Same token, same key, no options: the mismatched issuer and audience below
// are inside this payload and nothing complains.
const unchecked = await jwtVerify(token, key, { algorithms: ['HS256'] });
assert.equal(unchecked.payload.iss, 'https://issuer.example');
assert.equal(unchecked.payload.aud, 'csx-api');

const badIssuer = await rejection(() => verifyHS256(token, key, { issuer: 'https://attacker.example' }));
assert.ok(badIssuer instanceof errors.JWTClaimValidationFailed);
assert.equal(badIssuer.code, 'ERR_JWT_CLAIM_VALIDATION_FAILED');
assert.equal(badIssuer.claim, 'iss');
assert.equal(badIssuer.reason, 'check_failed');

const badAudience = await rejection(() => verifyHS256(token, key, { audience: 'other-api' }));
assert.equal(badAudience.claim, 'aud');
assert.equal(badAudience.reason, 'check_failed');

// A claim that is absent fails the same option with a different reason, which
// is the difference between a token for someone else and a token minted by
// something that never set the field.
const anonymous = await signHS256({ sub: 'x' }, key);
const missingIssuer = await rejection(() => verifyHS256(anonymous, key, { issuer: 'https://issuer.example' }));
assert.equal(missingIssuer.claim, 'iss');
assert.equal(missingIssuer.reason, 'missing');

// exp is in the same category. A token minted with no exp verifies forever;
// requiredClaims is the option that makes its absence an error, and it is why
// verifyHS256 sets it.
const immortal = await new SignJWT({ sub: 'forever' })
  .setProtectedHeader({ alg: 'HS256' })
  .setIssuedAt()
  .sign(key);
const acceptedForever = await jwtVerify(immortal, key, { algorithms: ['HS256'] });
assert.equal(acceptedForever.payload.sub, 'forever');
assert.equal(acceptedForever.payload.exp, undefined);
const missingExp = await rejection(() => verifyHS256(immortal, key));
assert.equal(missingExp.claim, 'exp');
assert.equal(missingExp.reason, 'missing');

// --- alg comes from the header unless you pin it -----------------------------
// A real HS512 token, signed with the same secret. Pinned, it is refused for
// the algorithm alone; unpinned, the header talks the verifier into HS512.
const hs512 = await new SignJWT({ sub: 'x' })
  .setProtectedHeader({ alg: 'HS512' })
  .setExpirationTime('1h')
  .sign(key);
const downgrade = await rejection(() => verifyHS256(hs512, key));
assert.ok(downgrade instanceof errors.JOSEAlgNotAllowed);
assert.equal(downgrade.code, 'ERR_JOSE_ALG_NOT_ALLOWED');
assert.equal(errors.JOSEAlgNotAllowed.code, 'ERR_JOSE_ALG_NOT_ALLOWED');
const followedHeader = await jwtVerify(hs512, key);
assert.deepEqual(followedHeader.protectedHeader, { alg: 'HS512' });

// alg:none. UnsecuredJWT is jose's only API for an unsigned token, and it is
// kept apart on purpose: its own encode, its own decode, and jwtVerify never
// opens it.
const unsecured = new UnsecuredJWT({ sub: 'admin' }).setIssuedAt().encode();
assert.equal(JSON.parse(Buffer.from(unsecured.split('.')[0], 'base64url')).alg, 'none');
assert.equal(unsecured.endsWith('.'), true);
assert.equal(UnsecuredJWT.decode(unsecured).payload.sub, 'admin');

const nonePinned = await rejection(() => verifyHS256(unsecured, key));
assert.ok(nonePinned instanceof errors.JOSEAlgNotAllowed);
assert.equal(nonePinned.code, 'ERR_JOSE_ALG_NOT_ALLOWED');
// Unpinned it still fails, with a different error: "none" is not an algorithm
// jose can implement, so an unsigned token has no path through jwtVerify even
// when the caller forgot to pin. Pinning is not what saves you from alg:none
// here — the HS512 case above is what an unpinned verifier actually gives away.
const noneUnpinned = await rejection(() => jwtVerify(unsecured, key));
assert.ok(noneUnpinned instanceof errors.JOSENotSupported);
assert.equal(noneUnpinned.code, 'ERR_JOSE_NOT_SUPPORTED');

// A header rewritten to claim RS256, handed to a verifier holding a symmetric
// key: the mirror of the RS256-to-HS256 confusion attack, and the shape an
// HS256 service actually receives. Pinned, the algorithm check fires. Unpinned,
// the only backstop left is the key type, and it is a TypeError — a rejected
// token arriving as a programming error, one more reason to pin.
const claimsRS256 = [
  Buffer.from(JSON.stringify({ alg: 'RS256' })).toString('base64url'),
  token.split('.')[1],
  signature,
].join('.');
const confusedPinned = await rejection(() => verifyHS256(claimsRS256, key));
assert.ok(confusedPinned instanceof errors.JOSEAlgNotAllowed);
const confusedUnpinned = await rejection(() => jwtVerify(claimsRS256, key));
assert.equal(confusedUnpinned.constructor, TypeError);
assert.match(confusedUnpinned.message, /^Key for the RS256 algorithm must be one of type CryptoKey, KeyObject, or JSON Web Key\./);

// --- jose does not enforce a minimum HMAC key size --------------------------
// RFC 7518 says an HS256 key must be at least as long as the hash output.
// Measured: jose signs and verifies with five bytes without complaint. The
// webapi build hands the key to crypto.subtle, and WebCrypto imports a 40-bit
// HMAC key just as willingly, so nothing in the stack below jose is going to
// catch a weak secret either. Key strength is the caller's job.
const weak = hmacKey('short');
const weakToken = await signHS256({ sub: 'x' }, weak);
assert.equal((await verifyHS256(weakToken, weak)).payload.sub, 'x');
const weakImported = await crypto.subtle.importKey('raw', weak, { name: 'HMAC', hash: 'SHA-256' }, false, ['verify']);
assert.equal(weakImported.algorithm.length, 40);

// --- async is the migration wall, measured ----------------------------------
// Not awaited: no throw, and the value is a truthy Promise rather than claims.
const floating = jwtVerify(forged, key, { algorithms: ['HS256'] });
assert.ok(floating instanceof Promise);
assert.ok(floating);
assert.equal(floating.payload, undefined);
// Given a handler in this same tick, so this one never counts as unhandled and
// cannot contaminate the measurement below.
floating.catch(() => {});

let syncThrow = null;
try {
  jwtVerify(forged, key, { algorithms: ['HS256'] }).catch(() => {});
} catch (err) {
  syncThrow = err;
}
assert.equal(syncThrow, null, 'jwtVerify never throws synchronously');

// And the rejection does not vanish: it reaches unhandledRejection, which on
// Node terminates the process unless something is listening. This is what a
// try/catch without await actually produces — the listener here is the only
// reason this contract survives its own demonstration, and the promise is
// deliberately never given a handler.
const unhandled = await new Promise((resolve) => {
  process.once('unhandledRejection', resolve);
  try {
    jwtVerify(forged, key, { algorithms: ['HS256'] });
  } catch {
    resolve(new Error('unreachable: it did not throw here'));
  }
});
assert.ok(unhandled instanceof errors.JWSSignatureVerificationFailed);
assert.equal(unhandled.code, 'ERR_JWS_SIGNATURE_VERIFICATION_FAILED');

// "Terminates the process" is the part that costs a service its uptime, so
// measure it instead of asserting it in a comment: the same unawaited verify in
// a child with no listener exits 1 and the pending timer never fires.
const orphan = [
  "import { SignJWT, jwtVerify } from 'jose';",
  "const k = new TextEncoder().encode('a-32-byte-or-longer-test-secret-value');",
  "const t = await new SignJWT({ sub: 'x' }).setProtectedHeader({ alg: 'HS256' }).setExpirationTime('1h').sign(k);",
  "jwtVerify(t, new TextEncoder().encode('the-wrong-key'), { algorithms: ['HS256'] });",
  "setTimeout(() => console.log('STILL_ALIVE'), 50);",
].join('\n');
const child = spawnSync(process.execPath, ['--input-type=module', '-e', orphan], {
  cwd: fileURLToPath(new URL('..', import.meta.url)),
  encoding: 'utf8',
});
assert.equal(child.status, 1);
assert.equal(child.stdout.includes('STILL_ALIVE'), false);
assert.match(child.stderr, /ERR_JWS_SIGNATURE_VERIFICATION_FAILED/);

// --- ESM-only, and what that does and does not mean on Node 22 --------------
const pkg = require('jose/package.json');
assert.equal(pkg.type, 'module');
// No CJS entry is declared at all: the "." export condition map has no
// "require" entry, only types and default.
assert.deepEqual(Object.keys(pkg.exports['.']).sort(), ['default', 'types']);
assert.equal(pkg.exports['.'].default, './dist/webapi/index.js');

// Refuted hypothesis, kept as the measurement: require('jose') does NOT throw
// ERR_REQUIRE_ESM here. Node unflagged require() of an ESM graph with no
// top-level await in 22.12, and "default" is the condition require falls back
// to, so on this runtime the CJS wall is not where the release notes put it.
// Older Node throws ERR_REQUIRE_ESM; the wall that never moves is the default
// export, below.
assert.ok(process.version.startsWith('v22.'));
const viaRequire = require('jose');
assert.equal(typeof viaRequire.jwtVerify, 'function');
assert.equal(typeof viaRequire.SignJWT, 'function');

const namespace = await import('jose');
assert.equal(namespace.default, undefined);
assert.equal(typeof namespace.jwtVerify, 'function');
assert.equal(Object.hasOwn(namespace, 'default'), false);

// `import jwt from 'jsonwebtoken'` is the shape everyone copies over, and the
// same line for jose fails at link time — before a single statement runs, so no
// try/catch in the module can see it. Bare specifiers do not resolve from a
// data: URL, so the entry is resolved first; the export being asked for, and
// the failure, are the ones `import jose from 'jose'` produces.
const entry = pathToFileURL(require.resolve('jose')).href;
const defaultImport = await rejection(() =>
  import('data:text/javascript,' + encodeURIComponent('import jose from ' + JSON.stringify(entry) + '; export default jose;')),
);
assert.ok(defaultImport instanceof SyntaxError);
assert.match(defaultImport.message, /does not provide an export named 'default'/);

console.log('CONTRACT PASS: jose HS256 sign/verify, typed error codes, pinned alg, opt-in claim checks');
