import assert from "node:assert/strict";
import { z } from "zod";

import {
  Contact,
  fieldErrors,
  Order,
  Payment,
  Profile,
  TaggedPayment,
} from "../src/schema.mjs";

assert.deepEqual(z.core.version, { major: 4, minor: 4, patch: 3 });

// ---------------------------------------------------------------------------
// The result object
// ---------------------------------------------------------------------------

const ok = Order.safeParse({ id: "A1", items: [{ sku: "s", qty: 2 }] });
assert.equal(ok.success, true);
assert.deepEqual(Object.keys(ok), ["success", "data"]);

// The failing branch carries no `data` key at all, so `result.data` after a
// failure is undefined rather than a partial object. There is no partial
// object to reach for: zod either returns the whole parsed value or none of it.
const bad = Order.safeParse({ id: 7, items: "none" });
assert.equal(bad.success, false);
assert.deepEqual(Object.keys(bad), ["success", "error"]);
assert.equal("data" in bad, false);

// The error is a ZodError and a real Error, so `catch (e)` narrowing works and
// so does rethrowing it through code that only understands Error.
assert.ok(bad.error instanceof z.ZodError);
assert.ok(bad.error instanceof Error);
assert.equal(bad.error.name, "ZodError");

// Every issue carries these four. `code` is a stable string, `path` is the
// walk to the value, `message` is already human-readable English.
assert.equal(bad.error.issues.length, 2);
for (const issue of bad.error.issues) {
  assert.equal(typeof issue.code, "string");
  assert.equal(typeof issue.message, "string");
  assert.ok(Array.isArray(issue.path));
}

// Both bad fields are reported. An object schema does not stop at the first
// failure, which is what makes rendering a whole form in one pass possible.
assert.deepEqual(
  bad.error.issues.map((i) => i.path[0]).sort(),
  ["id", "items"],
);

// A type mismatch is `invalid_type` and states what it wanted. What it got is
// only in the prose message: zod 4 dropped `received` from the issue object
// for an ordinary wrong-type input, so a zod 3 renderer that prints
// `issue.received` prints undefined for every mismatch of this kind. The
// narrow set of issues that do still carry it is measured further down.
const idIssue = bad.error.issues.find((i) => i.path[0] === "id");
assert.equal(idIssue.code, "invalid_type");
assert.equal(idIssue.expected, "string");
assert.deepEqual(Object.keys(idIssue), ["expected", "code", "path", "message"]);
assert.equal(idIssue.received, undefined);
assert.equal(idIssue.message, "Invalid input: expected string, received number");

// error.message is the issues array serialised as JSON, not a one-line
// summary. Logging `err.message` dumps the whole array into your log line.
assert.deepEqual(JSON.parse(bad.error.message), bad.error.issues);

// ---------------------------------------------------------------------------
// Paths through arrays
// ---------------------------------------------------------------------------

const nested = Order.safeParse({
  id: "A1",
  items: [{ sku: "s", qty: 1 }, { sku: "t", qty: "two" }],
});
const deep = nested.error.issues[0];

// The index is a number in the path, not the string "1". Everything else is a
// string key, so a path is (string | number)[] and typing it as string[] is
// wrong.
assert.deepEqual(deep.path, ["items", 1, "qty"]);
assert.equal(typeof deep.path[1], "number");
assert.deepEqual(fieldErrors(nested.error), {
  "items.1.qty": ["Invalid input: expected number, received string"],
});

// Why keeping the index matters: dropping the non-string segments to get a
// "field name" maps both bad rows onto items.qty, and the form shows one error
// where there were two.
const twoRows = Order.safeParse({
  id: "A1",
  items: [{ sku: "s", qty: "one" }, { sku: "t", qty: "two" }],
});
assert.equal(twoRows.error.issues.length, 2);
assert.deepEqual(Object.keys(fieldErrors(twoRows.error)), ["items.0.qty", "items.1.qty"]);
assert.deepEqual(
  [...new Set(twoRows.error.issues.map((i) =>
    i.path.filter((s) => typeof s === "string").join(".")))],
  ["items.qty"],
);

// A value that is the right type but out of range fails differently: the code
// is a bound name rather than invalid_type, and the issue carries the bound it
// checked plus an `origin` naming the datatype it applies to. too_small covers
// a short string and a small number alike, so `origin` is what tells the two
// apart when you are writing the message.
const small = Order.safeParse({ id: "A1", items: [{ sku: "s", qty: 0 }] })
  .error.issues[0];
assert.equal(small.code, "too_small");
assert.equal(small.origin, "number");
assert.equal(small.minimum, 0);
assert.equal(small.inclusive, false);
assert.deepEqual(small.path, ["items", 0, "qty"]);

// A failure with nothing to blame has an empty path, which is why joining it
// needs a fallback rather than producing "".
const rootIssue = Order.safeParse("not an object").error.issues[0];
assert.deepEqual(rootIssue.path, []);
assert.deepEqual(Object.keys(fieldErrors(Order.safeParse("x").error)), ["(root)"]);

// ---------------------------------------------------------------------------
// z.coerce.number()
// ---------------------------------------------------------------------------

// The half everyone knows: a query-string "42" fails a number and passes a
// coerced one.
assert.equal(z.number().safeParse("42").success, false);
assert.equal(z.number().safeParse("42").error.issues[0].code, "invalid_type");
assert.deepEqual(z.coerce.number().safeParse("42"), { success: true, data: 42 });

// The half that bites: z.coerce.number() is `Number(input)` followed by the
// number check, not a numeric parser. Number("") is 0, Number(null) is 0,
// Number(false) is 0, Number([]) is 0 — so every one of these is accepted as
// a valid number and a missing field silently becomes zero. Coerce at the
// edge you actually control, and check for emptiness before you coerce.
for (const input of ["", "   ", null, false, []]) {
  const r = z.coerce.number().safeParse(input);
  assert.equal(r.success, true, `expected ${JSON.stringify(input)} to coerce`);
  assert.equal(r.data, 0);
}
assert.equal(z.coerce.number().safeParse(true).data, 1);
assert.equal(z.coerce.number().safeParse("0x10").data, 16);

// It fails when Number() gives something the number type cannot represent as a
// finite value, which is NaN *or* Infinity — "1e999" overflows and is rejected,
// not accepted as a big number.
const nan = z.coerce.number().safeParse("abc");
assert.equal(nan.success, false);
assert.equal(nan.error.issues[0].code, "invalid_type");
assert.equal(nan.error.issues[0].expected, "number");
assert.equal(nan.error.issues[0].received, "NaN");
assert.equal(z.coerce.number().safeParse("1e999").error.issues[0].received, "Infinity");

// These are the issues that keep `received`, and coercion is not what decides
// it. The rule is narrower and worth knowing before you write the renderer:
// `received` appears only when the input is already the right JS type but
// holds a value that type cannot validly express — a number that is NaN or
// Infinity, a Date that is Invalid Date. Nothing else in zod 4 sets it, so
// `received` is a description of a bad value, never of a wrong type.
assert.equal(z.number().safeParse(NaN).error.issues[0].received, "NaN");
assert.equal(z.number().safeParse(Infinity).error.issues[0].received, "Infinity");
assert.equal(z.date().safeParse(new Date("nope")).error.issues[0].received, "Invalid Date");
assert.equal(z.date().safeParse("2020-01-01").error.issues[0].received, undefined);

// A symbol is the case that shows coercion alone does not add `received`:
// Number(Symbol()) throws, zod swallows it, and the still-uncoerced symbol
// fails as an ordinary wrong type with no `received` at all.
const sym = z.coerce.number().safeParse(Symbol("s"));
assert.equal(sym.success, false);
assert.equal(sym.error.issues[0].received, undefined);
assert.deepEqual(Object.keys(sym.error.issues[0]), ["expected", "code", "path", "message"]);

// ---------------------------------------------------------------------------
// optional vs nullable vs default
// ---------------------------------------------------------------------------

// Absent key: optional passes and stays absent, default fires, nullable fails.
// nullable is not "not required" — it accepts null and still demands the key.
const absent = Profile.safeParse({});
assert.equal(absent.success, false);
assert.deepEqual(absent.error.issues.map((i) => i.path), [["bio"]]);
assert.equal(absent.error.issues[0].expected, "string");

const minimal = Profile.safeParse({ bio: null });
assert.deepEqual(minimal.data, { bio: null, locale: "en" });
// The optional key is genuinely missing from the output, not present-as-undefined,
// so `"nickname" in profile` is false and JSON.stringify omits it.
assert.equal(Object.hasOwn(minimal.data, "nickname"), false);

// Pass the key explicitly as undefined and zod 4 keeps it. The output shape
// therefore mirrors the input shape, which matters if you diff parsed objects
// or hand them to something that distinguishes missing from undefined.
const explicit = Profile.safeParse({ nickname: undefined, bio: null });
assert.equal(Object.hasOwn(explicit.data, "nickname"), true);
assert.equal(explicit.data.nickname, undefined);
assert.deepEqual(Object.keys(explicit.data).sort(), ["bio", "locale", "nickname"]);

// default() replaces undefined, however the undefined arrived, and never null.
assert.equal(Profile.safeParse({ bio: null, locale: undefined }).data.locale, "en");
assert.equal(Profile.safeParse({ bio: null, locale: "ko" }).data.locale, "ko");

// The symmetry, stated as the failures: optional rejects null, nullable
// rejects undefined, default rejects null. Only nullable admits null.
assert.equal(z.string().optional().safeParse(null).success, false);
assert.equal(z.string().nullable().safeParse(undefined).success, false);
assert.equal(z.string().default("d").safeParse(null).success, false);
assert.equal(z.string().nullable().safeParse(null).data, null);
assert.equal(z.string().optional().safeParse(undefined).data, undefined);
assert.equal(z.string().default("d").safeParse(undefined).data, "d");

// ---------------------------------------------------------------------------
// Which union member failed
// ---------------------------------------------------------------------------

// Nothing survived: every member aborted on a type check, so there is no
// branch to prefer. One invalid_union issue at the root, one issue group per
// member, in declaration order.
const noMatch = Contact.safeParse({});
assert.equal(noMatch.error.issues.length, 1);

const unionIssue = noMatch.error.issues[0];
assert.equal(unionIssue.code, "invalid_union");
assert.deepEqual(unionIssue.path, []);
assert.equal(unionIssue.errors.length, 2);
assert.ok(unionIssue.errors.every(Array.isArray));

// zod 3 called this `unionErrors` and filled it with ZodError objects. In 4 it
// is `errors` and the entries are plain issue arrays, so the ZodError methods
// people call on them are not there to call.
assert.equal("unionErrors" in unionIssue, false);
assert.ok(!(unionIssue.errors[0] instanceof z.ZodError));

// The index is what identifies the branch: group 0 is the email member missing
// its key, group 1 the phone member missing its key. Nothing else says so.
assert.deepEqual(unionIssue.errors[0].map((i) => [i.path[0], i.code]), [
  ["email", "invalid_type"],
]);
assert.deepEqual(unionIssue.errors[1].map((i) => [i.path[0], i.code]), [
  ["phone", "invalid_type"],
]);

// Exactly one member survived. The expectation here was another invalid_union
// wrapper, since neither member parsed. Measured otherwise: the phone member
// aborts on the missing key, leaving the email member as the only one whose
// failure was continuable, so zod reports its issue directly, at the real
// field path, with no wrapper and no `errors` to unpack.
const oneMatch = Contact.safeParse({ email: "nope" });
assert.equal(oneMatch.error.issues.length, 1);
assert.equal(oneMatch.error.issues[0].code, "invalid_format");
assert.deepEqual(oneMatch.error.issues[0].path, ["email"]);
assert.equal(oneMatch.error.issues[0].errors, undefined);

// Two members survived, so the tie is unresolvable and the wrapper is back.
// Same schema, same kind of bad input, different error shape — this is why the
// union branch of an error renderer needs both cases.
const twoMatch = Contact.safeParse({ email: "nope", phone: "12" });
assert.equal(twoMatch.error.issues[0].code, "invalid_union");
assert.equal(twoMatch.error.issues[0].errors.length, 2);

// Which failures leave a member standing, measured against a member that can
// only ever fail one check. A length check is continuable, so that member is
// the sole survivor and its issue is returned bare. A wrong literal is not: it
// aborts its member exactly the way a wrong type does, nobody survives, and
// the wrapper comes back. Counting a literal among the continuable checks
// predicts the opposite result for this input, and getting it right is what
// explains the tag: a tag narrows by eliminating the members it does not
// match, not by ranking them.
const survivor = z.union([
  z.object({ kind: z.literal("card"), last4: z.string().length(4) }),
  z.object({ other: z.number() }),
]);
assert.equal(survivor.safeParse({ kind: "card", last4: "1" }).error.issues[0].code, "too_small");

const noSurvivor = z.union([
  z.object({ kind: z.literal("card"), last4: z.string() }),
  z.object({ other: z.number() }),
]);
const litFail = noSurvivor.safeParse({ kind: "cash", last4: "1234" }).error.issues[0];
assert.equal(litFail.code, "invalid_union");
assert.deepEqual(litFail.errors[0].map((i) => i.code), ["invalid_value"]);

// A shared literal tag is the dependable way to leave exactly one member
// standing, so a plain z.union of tagged objects already narrows: one issue,
// at the field that failed inside the member the tag names.
const union = Payment.safeParse({ kind: "card", last4: "12" });
assert.equal(union.error.issues.length, 1);
assert.equal(union.error.issues[0].code, "too_small");
assert.deepEqual(union.error.issues[0].path, ["last4"]);

// Declaring the discriminator produces exactly the same issue, and both
// schemas are the same union type underneath.
const tagged = TaggedPayment.safeParse({ kind: "card", last4: "12" });
assert.deepEqual(tagged.error.issues, union.error.issues);
assert.equal(TaggedPayment._zod.def.type, "union");
assert.equal(Payment._zod.def.type, "union");

// The one input that separates them: a tag matching no member. The plain union
// has nothing to narrow to and falls back to every member's issues, which is
// the unreadable output people blame unions for.
const looseUnknown = Payment.safeParse({ kind: "cash" }).error.issues[0];
assert.equal(looseUnknown.code, "invalid_union");
assert.deepEqual(looseUnknown.path, []);
assert.equal(looseUnknown.errors.length, 2);

// The declared version answers the actual question instead: one issue, aimed
// at the discriminator, carrying the tags it accepts and no member groups.
const unknownTag = TaggedPayment.safeParse({ kind: "cash" }).error.issues[0];
assert.equal(unknownTag.code, "invalid_union");
assert.deepEqual(unknownTag.path, ["kind"]);
assert.equal(unknownTag.discriminator, "kind");
assert.deepEqual(unknownTag.options, ["card", "iban"]);
assert.deepEqual(unknownTag.errors, []);
assert.equal(unknownTag.message, "Invalid discriminator value. Expected 'card' | 'iban'");

// A literal that does not match is `invalid_value` carrying the allowed
// `values`. zod 3 called it `invalid_literal` and carried `expected`.
const literalIssue = looseUnknown.errors[0].find((i) => i.path[0] === "kind");
assert.equal(literalIssue.code, "invalid_value");
assert.deepEqual(literalIssue.values, ["card"]);

// ---------------------------------------------------------------------------
// The two zod 3 to 4 migration answers, measured
// ---------------------------------------------------------------------------

// error.errors is gone. It is not a deprecated alias for issues, it is not
// defined at all, so `error.errors.map(...)` throws TypeError on undefined and
// `error.errors?.length` quietly reports zero problems. Rename to .issues.
assert.equal("errors" in bad.error, false);
assert.equal(bad.error.errors, undefined);
assert.equal(bad.error.issues.length, 2);

// z.string().email() has NOT been removed. Both spellings exist in 4.4.3 and
// produce the identical issue, so the top-level z.email() is the preferred
// form rather than a required rewrite. What did change is the issue: zod 3
// reported code "invalid_string" with validation "email"; zod 4 reports
// "invalid_format" with format "email" and an `origin`.
assert.equal(typeof z.email, "function");
assert.equal(typeof z.string().email, "function");
const viaMethod = z.string().email().safeParse("nope").error.issues[0];
const viaTopLevel = z.email().safeParse("nope").error.issues[0];
assert.equal(viaMethod.code, "invalid_format");
assert.equal(viaMethod.format, "email");
assert.equal(viaMethod.origin, "string");
assert.equal(viaMethod.message, "Invalid email address");
// Identical down to the compiled `pattern`, so the two spellings are the same
// validator and not merely similar ones.
assert.deepEqual(viaMethod, viaTopLevel);
assert.equal(z.email().safeParse("orders@example.com").success, true);
assert.equal(z.string().email().safeParse("orders@example.com").success, true);

// "The string methods all moved to the top level" is too strong as a rule, and
// too weak as a reassurance. Some were kept as aliases and at least one really
// was deleted: .ip() is gone, replaced by z.ipv4() and z.ipv6(), so that one
// is a hard break while .email() is not.
assert.equal(z.string().ip, undefined);
assert.equal(typeof z.ipv4, "function");
assert.equal(typeof z.ipv6, "function");

console.log("contract ok: zod", Object.values(z.core.version).join("."));
