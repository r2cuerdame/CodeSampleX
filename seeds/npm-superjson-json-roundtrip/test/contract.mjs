import assert from "node:assert/strict";
import superjson from "superjson";

import {
  makeFixture,
  Money,
  privateInstance,
  throughJson,
  throughSuperjson,
} from "../src/roundtrip.mjs";

// ---------------------------------------------------------------------------
// What plain JSON.stringify destroys
// ---------------------------------------------------------------------------

const fixture = makeFixture();

// The BigInt is the only member that announces itself, and it does so by
// taking the whole document down: one 64-bit id anywhere in a response body
// and JSON.stringify throws instead of returning a partial string. The message
// comes from the stringifier, so it names no key and gives no path.
assert.throws(
  () => JSON.stringify(fixture),
  (err) => {
    assert.ok(err instanceof TypeError);
    assert.equal(err.message, "Do not know how to serialize a BigInt");
    return true;
  },
);

// Removing it is the only way to ask what JSON.stringify does to the rest, and
// the answer is that it succeeds. Every remaining loss below is silent.
const { big, ...jsonSafe } = fixture;
const flat = throughJson(jsonSafe);

// Date is the one type in this fixture with a toJSON, so it survives as data
// and dies as a type: what comes back is a string that will not answer
// getTime().
assert.equal(flat.when, "2024-03-01T12:00:00.000Z");
assert.equal(typeof flat.when, "string");
assert.equal(flat.when instanceof Date, false);

// Map, Set and RegExp keep everything in internal slots, and JSON.stringify
// only walks enumerable own properties. All three come back as an empty
// object — not a truncated one, an unvisited one.
assert.deepEqual(flat.tags, {});
assert.deepEqual(flat.seen, {});
assert.deepEqual(flat.pattern, {});

// NaN and Infinity have no JSON spelling, so both become null. A number field
// that was "not measured yet" and one that was "over the limit" arrive
// indistinguishable from each other and from a real null.
assert.equal(flat.ratio, null);
assert.equal(flat.ceiling, null);

// The asymmetry, which is the part that makes a partial fix look correct: the
// same undefined is DELETED from an object and COERCED TO NULL in an array.
// Code that recovers records by filling in missing keys is wrong for lists,
// and code that filters nulls out of lists is wrong for records.
assert.equal("missing" in flat, false);
assert.equal(Object.keys(flat).includes("missing"), false);
assert.deepEqual(flat.holes, [1, null, 3]);
assert.equal(flat.holes.length, 3);

// Stated once more as the single line worth remembering.
assert.equal(JSON.stringify({ v: undefined }), "{}");
assert.equal(JSON.stringify([undefined]), "[null]");

// A top-level undefined does not even produce a string. JSON.stringify returns
// the value undefined, so `res.send(JSON.stringify(x))` sends the four
// characters "undefined" rather than valid JSON.
assert.equal(JSON.stringify(undefined), undefined);

// ---------------------------------------------------------------------------
// What superjson restores
// ---------------------------------------------------------------------------

const back = throughSuperjson(makeFixture());

// Every type in the fixture comes back as its own type, including the BigInt
// that stopped JSON.stringify outright.
assert.ok(back.when instanceof Date);
assert.equal(back.when.getTime(), Date.UTC(2024, 2, 1, 12, 0, 0));
assert.ok(back.tags instanceof Map);
assert.ok(back.seen instanceof Set);
assert.equal(typeof back.big, "bigint");
assert.ok(back.pattern instanceof RegExp);

// Values, not just types. The BigInt is the reason to care: 2^53 + 1 is not
// representable as a double, so the "just use Number()" workaround for the
// throw above returns 2^53 and loses the last digit without complaining.
assert.equal(back.big, 9007199254740993n);
assert.equal(Number(9007199254740993n), 9007199254740992);

assert.ok(Number.isNaN(back.ratio));
assert.equal(back.ceiling, Infinity);
assert.equal(back.pattern.source, "ab+c");
assert.equal(back.pattern.flags, "gi");

// A Map keeps non-string keys. This is what separates superjson from the
// hand-rolled fix: Object.fromEntries stringifies every key, so a number key
// becomes "7" and an object key becomes the literal string "[object Object]",
// collapsing distinct entries onto one slot.
assert.deepEqual([...back.tags.entries()], [["release", 3], [7, "seven"]]);
assert.equal(back.tags.get(7), "seven");
assert.deepEqual(Object.fromEntries(new Map([[7, "seven"], [{}, "obj"]])), {
  7: "seven",
  "[object Object]": "obj",
});

assert.deepEqual([...back.seen], ["a", "b"]);

// Both halves of the undefined asymmetry are repaired: the key exists again
// and holds undefined, and the array slot is a real undefined rather than a
// null — it is present, so it is a set element and not a sparse hole.
assert.equal("missing" in back, true);
assert.equal(back.missing, undefined);
assert.deepEqual(back.holes, [1, undefined, 3]);
assert.equal(1 in back.holes, true);

// Restoration rebuilds, it does not alias. The Date is a new object with the
// same instant, so mutating what came off the wire cannot reach the original.
const source = makeFixture();
const restored = throughSuperjson(source);
assert.equal(restored.when === source.when, false);
assert.equal(restored.when.getTime(), source.when.getTime());

// Nesting is not special-cased: types inside arrays, and inside a Map's own
// values, are annotated by path and come back too.
const nested = throughSuperjson({ a: { b: [new Date(7)] } });
assert.ok(nested.a.b[0] instanceof Date);
assert.equal(throughSuperjson(new Map([["k", new Date(7)]])).get("k").getTime(), 7);

// Negative zero survives, which JSON cannot express at all. Same mechanism as
// NaN and Infinity, and the same tag: all three ride as strings annotated
// "number", so -0 crosses as the two characters "-0".
assert.equal(JSON.stringify({ z: -0 }), '{"z":0}');
assert.ok(Object.is(throughSuperjson({ z: -0 }).z, -0));
assert.deepEqual(superjson.serialize({ z: -0 }), {
  json: { z: "-0" },
  meta: { values: { z: ["number"] }, v: 1 },
});

// ---------------------------------------------------------------------------
// The { json, meta } split, which is how it does it
// ---------------------------------------------------------------------------

const result = superjson.serialize(makeFixture());

// `json` is ordinary JSON with the awkward values rewritten into strings, so
// it survives JSON.stringify without throwing. That is the design: the wire
// format is still JSON and any consumer can read it, typed or not.
assert.deepEqual(Object.keys(result).sort(), ["json", "meta"]);
assert.deepEqual(result.json, {
  when: "2024-03-01T12:00:00.000Z",
  tags: [["release", 3], [7, "seven"]],
  seen: ["a", "b"],
  big: "9007199254740993",
  missing: null,
  ratio: "NaN",
  ceiling: "Infinity",
  pattern: "/ab+c/gi",
  holes: [1, null, 3],
});
assert.doesNotThrow(() => JSON.stringify(result.json));

// `json` is a rebuilt tree rather than a view of the input, so writing to it
// cannot reach back into the object you serialised.
const shared = { id: 1, nested: { deep: 2 } };
assert.equal(superjson.serialize(shared).json.nested === shared.nested, false);

// `meta.values` is the sidecar: one entry per value that needed rebuilding,
// keyed by dotted path, valued as a tag array. A tag is whatever string the
// transformer was declared with, so the casing is not derivable and not worth
// guessing at: Date is capitalised where map, set, bigint, undefined, number
// and regexp are not, and Date is not the exception — URL just below and Error
// further down are capitalised too.
assert.deepEqual(result.meta.values, {
  when: ["Date"],
  tags: ["map"],
  seen: ["set"],
  big: ["bigint"],
  missing: ["undefined"],
  ratio: ["number"],
  ceiling: ["number"],
  pattern: ["regexp"],
  "holes.1": ["undefined"],
});
assert.equal(result.meta.v, 1);

// URL carries the second capitalised tag, and is the other type here with a
// toJSON of its own — so plain JSON.stringify degrades it the way it degrades
// a Date, to a string you could rebuild by hand, rather than to {} or null.
// Having a toJSON is not a Date-specific property.
const url = new URL("https://example.com/a?b=1");
assert.deepEqual(superjson.serialize({ u: url }).meta.values, { u: ["URL"] });
assert.ok(throughSuperjson({ u: url }).u instanceof URL);
assert.equal(JSON.stringify({ u: url }), '{"u":"https://example.com/a?b=1"}');

// A path segment containing a dot is escaped, so the dotted keys are not
// ambiguous and such a key round-trips intact.
assert.deepEqual(superjson.serialize({ "a.b": new Date(0) }).meta.values, {
  "a\\.b": ["Date"],
});
assert.deepEqual(Object.keys(throughSuperjson({ "a.b": new Date(0) })), ["a.b"]);

// When nothing needed annotating there is no `meta` key at all — not an empty
// object, absent. Reading `result.meta.values` on already-plain data throws.
const plain = superjson.serialize({ a: 1 });
assert.deepEqual(plain, { json: { a: 1 } });
assert.equal("meta" in plain, false);
assert.throws(() => plain.meta.values, TypeError);

// "Nothing needed annotating" is not the same condition as "the data was
// already JSON", which is the trap in reading the check above as a type test.
// Reaching one object by two paths brings `meta` back by itself, holding
// `referentialEqualities` and no `values` at all — so here `meta` exists,
// `meta.values` is undefined rather than absent, and code that branches on
// `meta` being present has misread a plain payload as a typed one. This is not
// the dedupe option, which is off: `json` still carries both copies. The
// annotation is what re-links them, so identity survives the wire.
const twice = { a: 1 };
const twiceSplit = superjson.serialize({ x: twice, y: twice });
assert.deepEqual(twiceSplit.json, { x: { a: 1 }, y: { a: 1 } });
assert.deepEqual(twiceSplit.meta, { referentialEqualities: { x: ["y"] }, v: 1 });
assert.equal(twiceSplit.meta.values, undefined);
const relinked = throughSuperjson({ x: twice, y: twice });
assert.equal(relinked.x === relinked.y, true);

// By identity, not by value: two objects that merely look alike are not linked
// and get no annotation.
assert.equal("meta" in superjson.serialize({ x: { a: 1 }, y: { a: 1 } }), false);

// deserialize is the other half, and takes the split back as an object, so the
// two pieces can travel separately.
const viaSplit = superjson.deserialize(superjson.serialize({ d: new Date(5) }));
assert.ok(viaSplit.d instanceof Date);
assert.equal(viaSplit.d.getTime(), 5);

// stringify is exactly JSON.stringify of that split, which is why a superjson
// payload is inspectable with any JSON tool.
assert.equal(superjson.stringify({ d: new Date(5) }), JSON.stringify(superjson.serialize({ d: new Date(5) })));

// A bare undefined does become valid JSON here, unlike JSON.stringify above.
assert.equal(superjson.stringify(undefined), '{"json":null,"meta":{"values":["undefined"],"v":1}}');
assert.equal(superjson.parse(superjson.stringify(undefined)), undefined);

// ---------------------------------------------------------------------------
// What superjson does NOT restore
//
// registerClass below mutates the singleton shared by every importer, so the
// unregistered checks have to come first — they cannot be observed again once
// the registration has happened.
// ---------------------------------------------------------------------------

const money = new Money(1250);
const looseMoney = throughSuperjson({ money }).money;

// An unregistered class instance keeps its data and loses everything that made
// it that class. It is not annotated at all: superjson has no way to tell this
// object apart from an object literal, so there is no meta entry to miss.
assert.equal(looseMoney instanceof Money, false);
assert.equal(Object.getPrototypeOf(looseMoney), Object.prototype);
assert.deepEqual(looseMoney, { cents: 1250 });
assert.equal(typeof looseMoney.format, "undefined");
assert.equal("meta" in superjson.serialize({ money }), false);

// A function is dropped by both, and superjson annotates nothing for it — but
// it is not serialize() that drops it. Measured, against the obvious guess:
// serialize leaves the function sitting in `json` by reference, and the
// JSON.stringify inside superjson.stringify is what removes it. `json` is
// typed JSONValue and is not always a JSON value, so a pipeline that takes the
// split and sends it through anything other than JSON.stringify still carries
// the function.
const fn = () => 42;
const withFn = superjson.serialize({ id: 1, run: fn });
assert.equal(withFn.json.run, fn);
assert.deepEqual(Object.keys(withFn.json), ["id", "run"]);
assert.equal("meta" in withFn, false);

// After stringify it is gone, and gone the same asymmetric way an undefined
// is: missing from an object, null in an array.
assert.equal(superjson.stringify({ id: 1, run: fn }), '{"json":{"id":1}}');
assert.equal(superjson.stringify([fn]), '{"json":[null]}');
assert.deepEqual(throughSuperjson({ id: 1, run: fn }), { id: 1 });
assert.deepEqual(JSON.parse(JSON.stringify({ id: 1, run: fn })), { id: 1 });

// An unregistered symbol value takes the same two-step path, and the step that
// drops it is visibly the plain one: JSON.stringify treats a symbol exactly the
// way it treats undefined and a function, deleted from an object and null in an
// array.
const sym = Symbol("tag");
assert.equal(superjson.serialize({ id: 1, s: sym }).json.s, sym);
assert.deepEqual(Object.keys(throughSuperjson({ id: 1, s: sym })), ["id"]);
assert.equal(JSON.stringify({ id: 1, s: sym }), '{"id":1}');
assert.equal(JSON.stringify([sym]), "[null]");

// Measured, and the opposite of what the fixture above suggests: an Invalid
// Date is NOT restored, and it is lost by the same two-step route as the
// function. superjson's Date test rejects a NaN timestamp, so the value is
// never annotated; serialize hands the Date object straight through into
// `json`, and then the JSON.stringify inside stringify calls
// Date.prototype.toJSON, which answers null for an invalid date. What arrives
// is null, not a Date. A date built from unvalidated user input lands here.
const invalid = new Date("not a date");
assert.ok(invalid instanceof Date);
assert.ok(Number.isNaN(invalid.getTime()));

const invalidSplit = superjson.serialize({ d: invalid });
assert.equal("meta" in invalidSplit, false);
assert.ok(invalidSplit.json.d instanceof Date);
assert.equal(superjson.stringify({ d: invalid }), '{"json":{"d":null}}');
assert.equal(throughSuperjson({ d: invalid }).d, null);

// An Error survives as an Error but not as its subclass: a TypeError comes
// back as a plain Error carrying the name as data, and without its stack.
// JSON.stringify, for contrast, reports an Error as {}, because message and
// stack are own properties but not enumerable ones and name is not an own
// property at all — so the walk finds nothing to write.
assert.deepEqual(JSON.parse(JSON.stringify({ e: new TypeError("boom") })), { e: {} });
assert.deepEqual(Object.keys(new TypeError("boom")), []);
assert.equal(Object.hasOwn(new TypeError("boom"), "name"), false);
const errBack = throughSuperjson({ e: new TypeError("boom") }).e;
assert.ok(errBack instanceof Error);
assert.equal(errBack instanceof TypeError, false);
assert.equal(errBack.constructor.name, "Error");
assert.equal(errBack.message, "boom");
assert.equal(errBack.stack, undefined);

// "Carrying the name as data" is literal: the tag is "Error" for every Error,
// and the subclass survives only as a `name` string that was copied out and
// assigned back onto a base Error. So `err.name` still reads "TypeError" while
// every instanceof narrowing against a subclass is now false — the pair that
// makes a catch block look right and behave wrong.
assert.equal(errBack.name, "TypeError");
assert.deepEqual(superjson.serialize({ e: new TypeError("boom") }), {
  json: { e: { name: "TypeError", message: "boom" } },
  meta: { values: { e: ["Error"] }, v: 1 },
});

// The missing stack is opt-in rather than policy, and the opt-in is per
// instance: allowErrorProps copies the named property into json and assigns it
// back on the way out. Done on a private instance, because the default export
// is the singleton the assertion above just measured without it.
const keepsStack = privateInstance();
keepsStack.allowErrorProps("stack");
const withStack = keepsStack.parse(keepsStack.stringify({ e: new TypeError("boom") })).e;
assert.equal(typeof withStack.stack, "string");
assert.match(withStack.stack, /^TypeError: boom/);

// Registering by name is the fix, and the name is what crosses the wire, not
// the class. This is the mutation the section header warns about.
superjson.registerClass(Money, "Money");
const typedMoney = throughSuperjson({ money }).money;
assert.ok(typedMoney instanceof Money);
assert.equal(typedMoney.format(), "$12.50");
assert.deepEqual(superjson.serialize({ money }).meta.values, {
  money: [["class", "Money"]],
});

// Both ends have to agree on that name. A payload naming a class this end has
// never registered throws rather than degrading to a plain object, so a
// deploy that ships the producer before the consumer fails loudly.
assert.throws(
  () =>
    superjson.deserialize({
      json: { money: { cents: 1 } },
      meta: { values: { money: [["class", "Coupon"]] }, v: 1 },
    }),
  (err) => {
    assert.ok(err instanceof Error);
    assert.match(err.message, /unknown class 'Coupon'/);
    return true;
  },
);

// The registry lives on the default export's singleton, so registering Money
// above changed it for every module in the process. An instance of your own
// keeps its own registry and never saw the registration.
assert.equal("meta" in privateInstance().serialize({ money }), false);

console.log("contract ok");
