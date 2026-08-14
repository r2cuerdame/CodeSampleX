import { SignJWT, jwtVerify, errors } from 'jose';

/**
 * jose is the modern replacement for jsonwebtoken, and the parts that break a
 * migration are not cryptographic. Three of them, in the order people hit them:
 *
 * 1. Every operation is async. jwt.verify() threw synchronously, so a
 *    try/catch around it worked. Around a jose call it catches nothing unless
 *    the call is awaited: the failure arrives as a rejected promise, the
 *    "verified claims" you assigned are a Promise object (truthy, so an
 *    `if (claims)` guard passes), and the rejection escapes to
 *    process.unhandledRejection, which terminates the process on Node.
 *
 * 2. The key is bytes, never a string. jwt.verify(token, 'secret') is the
 *    normal jsonwebtoken call; jose rejects it with a plain TypeError. Rejects,
 *    not throws, because of 1. Plain, as in not a JOSEError and with no .code,
 *    so error handling written around `err instanceof jose.errors.JOSEError`
 *    silently misses the one mistake every migration makes. Encode the secret:
 *    new TextEncoder().encode(s).
 *
 * 3. jwtVerify resolves to { payload, protectedHeader }, not to the claims.
 *    `const claims = await jwtVerify(...)` leaves claims.sub undefined.
 *
 * What jose does not change is the thing that matters: the token's own header
 * chooses the algorithm unless you pin `algorithms`. jose is safe against
 * alg:none regardless — "none" is not an algorithm it can implement, so an
 * unsecured token fails even unpinned — but an unpinned verifier will happily
 * follow the header from HS256 to HS512, and pinning is also what turns a token
 * with a rewritten alg into one clear ERR_JOSE_ALG_NOT_ALLOWED instead of a
 * TypeError about key types.
 */

/** HMAC keys are bytes. jose rejects a string, and this is the whole fix. */
export function hmacKey(secret) {
  return new TextEncoder().encode(secret);
}

export async function signHS256(payload, key, { issuer, audience, expiresIn = '1h' } = {}) {
  // setProtectedHeader is mandatory. Without it sign() rejects with JWSInvalid
  // rather than defaulting to anything, because the header is signed data and
  // jose will not invent signed data for you.
  const jwt = new SignJWT(payload).setProtectedHeader({ alg: 'HS256' }).setIssuedAt();
  if (issuer) jwt.setIssuer(issuer);
  if (audience) jwt.setAudience(audience);
  return jwt.setExpirationTime(expiresIn).sign(key);
}

/**
 * jwtVerify checks the signature, and checks exp/nbf when they are present.
 * Everything else is opt-in: issuer and audience are validated only if you
 * pass them, and a token with no exp at all verifies forever. requiredClaims
 * is what closes that hole, so it belongs in the default and not in a comment.
 */
export async function verifyHS256(token, key, { issuer, audience } = {}) {
  return jwtVerify(token, key, {
    algorithms: ['HS256'],
    requiredClaims: ['exp'],
    ...(issuer === undefined ? {} : { issuer }),
    ...(audience === undefined ? {} : { audience }),
  });
}

/**
 * Every failure jose raises on purpose descends from errors.JOSEError and
 * carries a stable .code. Anything else — a TypeError about key types, above
 * all — is a bug in the calling code, not a rejected token, and should not be
 * reported to a client as "invalid token".
 */
export function isTokenRejection(err) {
  return err instanceof errors.JOSEError;
}
