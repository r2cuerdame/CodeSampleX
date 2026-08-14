import assert from "node:assert/strict";
import { createHash, randomBytes, scryptSync, timingSafeEqual } from "node:crypto";
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import * as bcrypt from "bcryptjs";

import {
  bcryptHash,
  bcryptVerify,
  constantTimeEqual,
  fastestMillis,
  scryptHash,
  scryptVerify,
  sha256Hex,
  SCRYPT_KEYLEN,
  SCRYPT_PARAMS,
  unguardedEqual,
} from "../src/passwords.mjs";

// bcryptjs 3 declares an exports map with only "." in it, so the subpath the
// rest of the ecosystem reaches for when it wants a version at runtime is not
// reachable at all. Resolve the ESM entry point and read the manifest sitting
// next to it instead.
assert.throws(() => createRequire(import.meta.url)("bcryptjs/package.json"), {
  code: "ERR_PACKAGE_PATH_NOT_EXPORTED",
});
const manifest = JSON.parse(
  readFileSync(new URL("./package.json", import.meta.resolve("bcryptjs")), "utf8"),
);
assert.equal(manifest.version, "3.0.3");

const PASSWORD = "correct horse battery staple";

// ---------------------------------------------------------------------------
// scrypt: the salt is yours to store
// ---------------------------------------------------------------------------

const salt = randomBytes(16);
const key = scryptSync(PASSWORD, salt, SCRYPT_KEYLEN);

// Same password, same salt, same bytes — that is what makes verification
// possible at all.
assert.ok(key.equals(scryptSync(PASSWORD, salt, SCRYPT_KEYLEN)));

// Same password, a different salt, and nothing about the two results relates.
// This is the property that defeats a precomputed table, and it is also the
// reason the salt cannot be thrown away: without the exact bytes used at
// signup there is no way back to this key.
const otherSalt = randomBytes(16);
assert.equal(otherSalt.equals(salt), false);
assert.equal(key.equals(scryptSync(PASSWORD, otherSalt, SCRYPT_KEYLEN)), false);

// And the key does not carry the salt anywhere inside it. scrypt returns
// derived bytes, not a record — there is no field to parse it out of, which is
// the whole difference from bcrypt further down.
assert.equal(key.includes(salt), false);
assert.equal(key.length, SCRYPT_KEYLEN);

// A string salt is taken as its UTF-8 bytes, not decoded as hex. Storing the
// salt as hex and passing the string straight back derives a different key
// than passing the bytes it encodes, so a signup path and a login path that
// disagree about this reject every correct password.
const saltHex = salt.toString("hex");
assert.ok(scryptSync(PASSWORD, saltHex, 32).equals(scryptSync(PASSWORD, Buffer.from(saltHex, "utf8"), 32)));
assert.equal(scryptSync(PASSWORD, saltHex, 32).equals(scryptSync(PASSWORD, Buffer.from(saltHex, "hex"), 32)), false);

// Round-tripped through the storage format, which has to carry the salt and
// the cost because nothing else will.
const stored = scryptHash(PASSWORD);
assert.match(stored, /^scrypt\$16384\$8\$1\$[0-9a-f]{32}\$[0-9a-f]{128}$/);
assert.equal(scryptVerify(PASSWORD, stored), true);
assert.equal(scryptVerify("correct horse battery stapl", stored), false);

// Two accounts that chose the same password store different records, because
// each gets its own salt. Cracking one says nothing about the other.
assert.notEqual(scryptHash(PASSWORD), scryptHash(PASSWORD));

// Which is precisely what a bare digest cannot do. sha256 has nowhere to put a
// salt, so the same password is the same string in every row: the table itself
// tells an attacker which accounts share a password, and one guess tested
// against one row is a guess tested against all of them.
assert.equal(sha256Hex(PASSWORD), sha256Hex(PASSWORD));
assert.equal(sha256Hex(PASSWORD).length, 64);
assert.equal(createHash("sha256").update(PASSWORD).digest("hex"), sha256Hex(PASSWORD));

// ---------------------------------------------------------------------------
// timingSafeEqual throws on a length mismatch — it does not return false
// ---------------------------------------------------------------------------

const wrongLength = createHash("sha256").update(PASSWORD).digest(); // 32 bytes
assert.equal(wrongLength.length, 32);
assert.notEqual(wrongLength.length, key.length);

assert.throws(
  () => unguardedEqual(key, wrongLength),
  (err) => {
    assert.ok(err instanceof RangeError);
    assert.equal(err.code, "ERR_CRYPTO_TIMING_SAFE_EQUAL_LENGTH");
    assert.equal(err.message, "Input buffers must have the same byte length");
    return true;
  },
);

// That is the shape of a real outage: a row still holding a 32-byte digest
// from an older scheme, compared against a fresh 64-byte scrypt key, throws
// out of the login handler as a 500 instead of answering "wrong password".
// Checking the lengths first turns it back into an answer. Length is not
// secret — it is fixed by the algorithm, not by the password.
assert.equal(constantTimeEqual(key, wrongLength), false);

// The guard changes nothing about the equal-length case, which is the one that
// actually needs to be constant time.
assert.equal(constantTimeEqual(key, Buffer.from(key)), true);
const flipped = Buffer.from(key);
flipped[0] ^= 0x01;
assert.equal(constantTimeEqual(key, flipped), false);
assert.equal(timingSafeEqual(key, flipped), false);

// Strings are rejected outright, so hex-comparing with === is not a shortcut
// past this — it is a different, timing-leaky comparison.
assert.throws(() => timingSafeEqual(saltHex, saltHex), { code: "ERR_INVALID_ARG_TYPE" });

// ---------------------------------------------------------------------------
// bcryptjs carries its own salt and cost
// ---------------------------------------------------------------------------

const hash = bcryptHash(PASSWORD, 10);
assert.equal(hash.length, 60);
assert.ok(hash.startsWith("$2b$10$"));
assert.equal(bcrypt.getRounds(hash), 10);
assert.equal(bcrypt.getSalt(hash), hash.slice(0, 29));

// Every call produces a different string, because genSalt runs per call — and
// both verify against the same password with no salt passed in, because the
// salt each one used is the first 29 characters of itself.
const hashAgain = bcryptHash(PASSWORD, 10);
assert.notEqual(hash, hashAgain);
assert.equal(bcryptVerify(PASSWORD, hash), true);
assert.equal(bcryptVerify(PASSWORD, hashAgain), true);
assert.equal(bcryptVerify("wrong", hash), false);

// This is literally how compare works: re-hash with the salt read back out of
// the stored string and you reproduce the stored string exactly.
assert.equal(bcrypt.hashSync(PASSWORD, bcrypt.getSalt(hash)), hash);

// Published jBCrypt vectors, written by a different implementation years ago
// at cost 6. They verify here, so replacing the native bcrypt addon with this
// pure-JavaScript package does not invalidate a single stored row — which is
// the only reason the swap is safe to make on a live table.
const VECTORS = [
  ["", "$2a$06$DCq7YPn5Rq63x1Lad4cll.TV4S6ytwfsfvkgY8jIucDrjc8deX1s."],
  ["a", "$2a$06$m0CrhHm10qJ3lXRY.5zDGO3rS2KdeeWLuGmsfGlMfOxih58VYVfxe"],
  ["abc", "$2a$06$If6bvum7DFjUnE9p2uDeDu0YHzrHM6tf.iqN8.yx.jNN1ILEf7h0i"],
  ["abcdefghijklmnopqrstuvwxyz", "$2a$06$.rCVZVOThsIa97pEDOxvGuRRgzG64bvtJ0938xuqzv18d3ZpQhstC"],
  ["~!@#$%^&*()      ~!@#$%^&*()PNBFRD", "$2a$06$fPIsBO8qRqkjj273rfaOI.HtSV9jLDpTbZn782DC6/t7qT67P6FfO"],
];
for (const [plain, vector] of VECTORS) {
  assert.equal(bcryptVerify(plain, vector), true, `vector for ${JSON.stringify(plain)}`);
  assert.equal(bcrypt.hashSync(plain, bcrypt.getSalt(vector)), vector);
  // The cost is readable without verifying anything, so a login can notice a
  // row is below current policy and rehash it while it holds the plaintext.
  assert.equal(bcrypt.getRounds(vector), 6);
}

// A garbage or empty stored hash answers false. A null one does not: bcryptjs
// type-checks its arguments and throws, so a user row whose password column is
// NULL crashes the login path rather than failing it.
assert.equal(bcryptVerify(PASSWORD, ""), false);
assert.equal(bcryptVerify(PASSWORD, "$2b$10$short"), false);
assert.throws(() => bcryptVerify(PASSWORD, null), { message: "Illegal arguments: string, object" });

// ---------------------------------------------------------------------------
// bcrypt silently truncates at 72 bytes
// ---------------------------------------------------------------------------

// The highest-consequence trap here, and it is silent in both directions:
// nothing warns at hash time and nothing warns at verify time. Two different
// passwords sharing their first 72 bytes are the same password to bcrypt, so
// each one logs in as the other.
const prefix = "A".repeat(72);
const longA = `${prefix}-account-recovery-passphrase`;
const longB = `${prefix}-something-else-entirely!!`;
assert.notEqual(longA, longB);

const longHash = bcryptHash(longA, 10);
assert.equal(bcryptVerify(longA, longHash), true);
assert.equal(bcryptVerify(longB, longHash), true); // <- different password, accepted

// The cut is at exactly 72 bytes: the bare 72-byte prefix opens the account,
// 71 bytes does not.
assert.equal(bcryptVerify(prefix, longHash), true);
assert.equal(bcryptVerify("A".repeat(71), longHash), false);

// Bytes, not characters. 36 accented characters are 72 bytes of UTF-8 and
// survive; one more character is 74 bytes and loses the tail. A length check
// written against String.length passes both.
const fits = "é".repeat(36);
const cut = "é".repeat(37);
assert.equal(fits.length, 36);
assert.equal(Buffer.byteLength(fits, "utf8"), 72);
assert.equal(cut.length, 37);
assert.equal(Buffer.byteLength(cut, "utf8"), 74);
assert.equal(bcrypt.truncates(fits), false);
assert.equal(bcrypt.truncates(cut), true);
assert.equal(bcrypt.truncates(longA), true);
assert.equal(bcrypt.truncates(prefix), false);

// truncates() is the check to run at signup, since nothing else will tell you.
// The control for all of the above: below the limit the tail is honoured, so
// two passwords sharing a 60-byte prefix and differing after it do not
// cross-verify. The collision further up is truncation, not a broken compare.
const shortA = `${"B".repeat(60)}-one`;
const shortB = `${"B".repeat(60)}-two`;
assert.equal(bcrypt.truncates(shortA), false);
assert.equal(bcrypt.truncates(shortB), false);
assert.equal(bcryptVerify(shortB, bcryptHash(shortA, 10)), false);

// scrypt has no such limit: the same two passwords derive different keys, so
// a long generated passphrase keeps all of its entropy.
const longStored = scryptHash(longA);
assert.equal(scryptVerify(longA, longStored), true);
assert.equal(scryptVerify(longB, longStored), false);
assert.equal(scryptVerify(prefix, longStored), false);

// ---------------------------------------------------------------------------
// scrypt's default N is slow on purpose
// ---------------------------------------------------------------------------

// The defaults, confirmed by deriving the same key with them written out.
// Worth pinning: the stored record has to name them, and a future Node that
// changed them would break verification of every existing row.
assert.ok(
  scryptSync(PASSWORD, salt, SCRYPT_KEYLEN).equals(
    scryptSync(PASSWORD, salt, SCRYPT_KEYLEN, SCRYPT_PARAMS),
  ),
);
assert.deepEqual(SCRYPT_PARAMS, { N: 16384, r: 8, p: 1 });

// Measured as a lower bound, never an exact figure — the number depends on the
// machine, and only the floor is a fact about the algorithm. N=16384 with r=8
// means touching 16 MB of memory in a chain that cannot be shortcut, and the
// fastest of several runs still cannot get under a few milliseconds. That cost
// is the feature; a password hash that is fast is a password hash that is fast
// to attack.
const scryptMs = fastestMillis(() => scryptSync(PASSWORD, salt, SCRYPT_KEYLEN));
const sha256Ms = fastestMillis(() => createHash("sha256").update(PASSWORD).digest());
assert.ok(scryptMs >= 5, `scrypt at default cost took ${scryptMs.toFixed(2)}ms`);
assert.ok(
  scryptMs > sha256Ms * 50,
  `scrypt ${scryptMs.toFixed(2)}ms vs sha256 ${sha256Ms.toFixed(4)}ms`,
);

// Lowering N lowers the cost, which is how you would confirm the time above is
// the work factor rather than call overhead. Only the direction is asserted:
// the ratio is a fact about this machine, not about scrypt.
const cheapMs = fastestMillis(() => scryptSync(PASSWORD, salt, SCRYPT_KEYLEN, { N: 1024 }));
assert.ok(cheapMs < scryptMs, `N=1024 ${cheapMs.toFixed(2)}ms vs N=16384 ${scryptMs.toFixed(2)}ms`);

// Raising N is where it bites back. maxmem defaults to 32 MiB and scrypt's
// memory grows with N, so Node refuses — with a RangeError quoting OpenSSL
// about a memory limit, an error that names neither N nor maxmem. Raise maxmem
// alongside N, or a cost increase fails only in production, wherever the config
// is set higher than the test suite's.
assert.throws(
  () => scryptSync(PASSWORD, salt, SCRYPT_KEYLEN, { N: 2 ** 17 }),
  (err) => {
    assert.ok(err instanceof RangeError);
    assert.equal(err.code, "ERR_CRYPTO_INVALID_SCRYPT_PARAMS");
    assert.match(err.message, /memory limit exceeded/);
    return true;
  },
);

// And it does not take a large jump to get there. A single doubling of the
// default cost is already over the default maxmem, so the very next setting
// anyone would reach for is the one that fails.
assert.throws(
  () => scryptSync(PASSWORD, salt, SCRYPT_KEYLEN, { N: 2 ** 15 }),
  { code: "ERR_CRYPTO_INVALID_SCRYPT_PARAMS" },
);

// The message is not diagnostic either. An N that is not a power of two is a
// different fault entirely and produces the byte-identical string, so reading
// "memory limit exceeded" as a memory problem sends you off to raise maxmem
// when the cost itself is malformed.
const failureMessage = (options) => {
  try {
    scryptSync(PASSWORD, salt, 32, options);
    return null;
  } catch (err) {
    return err.message;
  }
};
assert.match(failureMessage({ N: 3 }), /memory limit exceeded/);
assert.equal(failureMessage({ N: 3 }), failureMessage({ N: 2 ** 17 }));

// How much to raise it to is not the 128 * N * r that gets quoted for scrypt:
// budget exactly that and it is still rejected, as the last assertion here
// shows. Bisecting maxmem puts the accepted minimum at exactly
// 128 * r * (N + p + 2) — OpenSSL also charges for the p input blocks and two
// blocks of scratch. The quoted figure is 2 KB short at the defaults, and being
// short by any amount produces the same opaque error.
const requiredBytes = (cost, blockSize, parallelism) =>
  128 * blockSize * (cost + parallelism + 2);
const { N, r, p } = SCRYPT_PARAMS;
assert.equal(requiredBytes(N, r, p), 16780288);
assert.equal(
  scryptSync(PASSWORD, salt, SCRYPT_KEYLEN, { N, r, p, maxmem: requiredBytes(N, r, p) }).length,
  SCRYPT_KEYLEN,
);
assert.throws(
  () => scryptSync(PASSWORD, salt, SCRYPT_KEYLEN, { N, r, p, maxmem: requiredBytes(N, r, p) - 1 }),
  { code: "ERR_CRYPTO_INVALID_SCRYPT_PARAMS" },
);
assert.throws(
  () => scryptSync(PASSWORD, salt, SCRYPT_KEYLEN, { N, r, p, maxmem: 128 * N * r }),
  { code: "ERR_CRYPTO_INVALID_SCRYPT_PARAMS" },
);

// One parameter set cannot tell "+ p + 2" apart from a flat "+ 3", and two
// sets that both hold r at 8 and p at 1 cannot either. These vary each term on
// its own — p alone, r alone, then all three at once — and the boundary lands
// on the formula every time, which is what makes it a rule rather than a
// coincidence fitted to the defaults.
for (const [cost, blockSize, parallelism, expected] of [
  [2 ** 17, 8, 1, 134220800],
  [16384, 8, 5, 16784384],
  [16384, 2, 1, 4195072],
  [1024, 4, 3, 526848],
  [32768, 8, 1, 33557504],
]) {
  const options = { N: cost, r: blockSize, p: parallelism };
  const need = requiredBytes(cost, blockSize, parallelism);
  assert.equal(need, expected, `N=${cost} r=${blockSize} p=${parallelism}`);
  assert.equal(
    scryptSync(PASSWORD, salt, SCRYPT_KEYLEN, { ...options, maxmem: need }).length,
    SCRYPT_KEYLEN,
  );
  assert.throws(
    () => scryptSync(PASSWORD, salt, SCRYPT_KEYLEN, { ...options, maxmem: need - 1 }),
    { code: "ERR_CRYPTO_INVALID_SCRYPT_PARAMS" },
  );
}

// N=32768 is where the quoted formula is at its most convincing and its most
// wrong. 128 * N * r comes to 33554432 there, which is exactly the 32 MiB
// default maxmem — so the folklore says the next cost up fits, with nothing to
// spare and nothing to configure. It is 3072 bytes short, which is why the
// single doubling asserted above is rejected.
assert.equal(128 * 32768 * 8, 33554432);
assert.equal(requiredBytes(32768, 8, 1) - 128 * 32768 * 8, 3072);

// That the default really is 32 MiB and not merely somewhere in between:
// spelling it out changes neither side of the boundary.
assert.equal(
  scryptSync(PASSWORD, salt, SCRYPT_KEYLEN, { N, r, p, maxmem: 33554432 }).length,
  SCRYPT_KEYLEN,
);
assert.throws(
  () => scryptSync(PASSWORD, salt, SCRYPT_KEYLEN, { N: 2 ** 15, r: 8, p: 1, maxmem: 33554432 }),
  { code: "ERR_CRYPTO_INVALID_SCRYPT_PARAMS" },
);

// N=1 and N=3 are rejected for not being a power of two greater than one, but
// zero is not rejected — it means "use the default", and not only for N: r, p
// and maxmem each behave the same way. Number("") is 0, so a cost that arrives
// from an empty config value does not fail loudly, it silently is not the cost
// you configured, and the only symptom is that logins got faster.
assert.throws(() => scryptSync(PASSWORD, salt, 32, { N: 1 }), { code: "ERR_CRYPTO_INVALID_SCRYPT_PARAMS" });
assert.throws(() => scryptSync(PASSWORD, salt, 32, { N: 3 }), { code: "ERR_CRYPTO_INVALID_SCRYPT_PARAMS" });
assert.equal(Number(""), 0);
const atDefaults = scryptSync(PASSWORD, salt, 32);
for (const option of ["N", "r", "p", "maxmem"]) {
  assert.ok(scryptSync(PASSWORD, salt, 32, { [option]: 0 }).equals(atDefaults), `${option}: 0`);
}

console.log("contract ok");
