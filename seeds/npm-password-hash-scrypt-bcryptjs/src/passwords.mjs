import { createHash, randomBytes, scryptSync, timingSafeEqual } from "node:crypto";
import * as bcrypt from "bcryptjs";

/**
 * Two ways to store a password on Node without a native module: node:crypto's
 * built-in scrypt, and bcryptjs, which is bcrypt reimplemented in plain
 * JavaScript.
 *
 * The reason usually given for reaching for bcryptjs is that the native
 * `bcrypt` package needs node-gyp and a toolchain and therefore cannot be
 * installed on an Alpine image. That is out of date, and believing it is how
 * you end up choosing a password hash for a reason that stopped being true:
 * `npm i bcrypt` in node:22-alpine installs bcrypt 6.0.0 in under a second
 * with no compiler present, and the result hashes and verifies. bcrypt 6
 * ships prebuildify N-API binaries tagged by libc — prebuilds/linux-x64
 * carries both bcrypt.glibc.node and bcrypt.musl.node — and node-gyp-build
 * picks one at require time, so it survives --ignore-scripts too. Measured in
 * this image; the contract below does not assert it, because pulling bcrypt in
 * to prove a negative is not worth making it a dependency of the sample.
 *
 * What is still true is narrower, and it is a property of the artifact rather
 * than of one release: bcryptjs has zero dependencies and ships no binary, so
 * there is no libc, architecture or Node-ABI matrix to match and nothing to
 * re-prebuild for a platform outside the seven bcrypt 6 covers. scrypt ships
 * nothing at all, being already in node:crypto.
 *
 * The two differ in one way that decides most of the code you write around
 * them: bcrypt writes the salt and the cost into the hash string, and scrypt
 * does not. A scrypt derived key is 64 opaque bytes and nothing else, so the
 * salt is yours to generate, yours to store, and yours to hand back at
 * verification time. Lose it and every account is locked out.
 */

export const SCRYPT_SALT_BYTES = 16;
export const SCRYPT_KEYLEN = 64;

/**
 * scrypt's parameters, spelled out rather than left to the default, because
 * they have to be written into the stored record anyway: a row hashed at one
 * cost must still verify after you raise the cost for new signups. These are
 * the values Node uses when you pass no options at all, which the contract
 * confirms by deriving the same key both ways.
 */
export const SCRYPT_PARAMS = { N: 16384, r: 8, p: 1 };

/**
 * The storage format is invented here, and having to invent it is the point.
 * bcrypt's `$2b$10$...` is this same idea standardised twenty years ago; with
 * scrypt you are writing the parser yourself, so the cost parameters go in
 * beside the salt or you can never change them.
 *
 * The salt is stored as hex and decoded back to bytes before use. That
 * round-trip is not cosmetic: scryptSync takes a string salt as its UTF-8
 * bytes, so handing it the 32-character hex string derives a different key
 * than handing it the 16 bytes the string encodes. Mix the two up across
 * signup and login and every password is wrong, with nothing in the logs to
 * say why.
 */
export function scryptHash(password, salt = randomBytes(SCRYPT_SALT_BYTES)) {
  const { N, r, p } = SCRYPT_PARAMS;
  const key = scryptSync(password, salt, SCRYPT_KEYLEN, { N, r, p });
  return `scrypt$${N}$${r}$${p}$${salt.toString("hex")}$${key.toString("hex")}`;
}

export function scryptVerify(password, stored) {
  const [scheme, N, r, p, saltHex, keyHex] = stored.split("$");
  if (scheme !== "scrypt") return false;
  const key = scryptSync(password, Buffer.from(saltHex, "hex"), keyHex.length / 2, {
    N: Number(N),
    r: Number(r),
    p: Number(p),
  });
  return constantTimeEqual(key, Buffer.from(keyHex, "hex"));
}

/**
 * timingSafeEqual is the whole reason to reach for node:crypto here, and it
 * has one edge that turns a failed login into a 500: given two buffers of
 * different byte lengths it throws a RangeError instead of returning false.
 * It cannot do otherwise — the guarantee it makes is that the comparison takes
 * the same time whatever the contents are, and it has no way to compare
 * different lengths without the duration leaking which is which.
 *
 * So the length check goes first and returns false. Length is not a secret:
 * a stored hash's length is fixed by the algorithm, so revealing that a row
 * is 32 bytes rather than 64 reveals which algorithm wrote it, not anything
 * about the password. That case is exactly how this fires in production —
 * rows left over from an older sha256 scheme meeting freshly derived
 * 64-byte scrypt keys.
 */
export function constantTimeEqual(a, b) {
  if (a.length !== b.length) return false;
  return timingSafeEqual(a, b);
}

/** The same comparison without the guard, so the contract can measure the throw. */
export function unguardedEqual(a, b) {
  return timingSafeEqual(a, b);
}

/**
 * What not to do, kept here to be measured rather than asserted about. A bare
 * digest is unsalted and fast: unsalted means two accounts with the same
 * password store the same string, so one cracked row cracks all of them and a
 * precomputed table answers the rest; fast means a GPU walks the whole
 * candidate space. Both failures are properties of the primitive, so no amount
 * of care in the calling code fixes them.
 */
export function sha256Hex(password) {
  return createHash("sha256").update(password, "utf8").digest("hex");
}

export function bcryptHash(password, cost = 10) {
  return bcrypt.hashSync(password, cost);
}

/**
 * No salt argument, and there is nowhere to put one: compareSync reads the
 * version, the cost and the salt back out of the stored string, re-runs the
 * hash with them and compares. One column in the database, and a cost upgrade
 * is a matter of rehashing on next successful login while old rows keep
 * verifying at their old cost.
 */
export function bcryptVerify(password, stored) {
  return bcrypt.compareSync(password, stored);
}

/**
 * Minimum of several runs. For a lower bound on work this is the statistic to
 * use: scheduling noise and a cold cache can only make a run slower, never
 * faster, so the fastest run is the closest thing to the real cost, and a
 * loaded machine cannot turn the assertion into a flake.
 */
export function fastestMillis(fn, runs = 5) {
  let best = Infinity;
  for (let i = 0; i < runs; i++) {
    const started = process.hrtime.bigint();
    fn();
    const elapsed = Number(process.hrtime.bigint() - started) / 1e6;
    if (elapsed < best) best = elapsed;
  }
  return best;
}
