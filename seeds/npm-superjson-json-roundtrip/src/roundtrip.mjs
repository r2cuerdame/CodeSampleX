import superjson, { SuperJSON } from "superjson";

/**
 * What JSON.stringify does to a value that is not already JSON, and what
 * superjson puts back.
 *
 * JSON.stringify has exactly one extension point, `toJSON`, and of the types
 * in the fixture below only Date implements it. That is not a rule about
 * built-ins — Node's URL has one too, measured in the contract — it is just
 * absent from Map, Set, RegExp and BigInt. Everything else falls into one of
 * four buckets, and only the last one tells you anything went wrong:
 *
 *   - enumerated as a plain object. Map, Set and RegExp keep their contents
 *     in internal slots rather than in enumerable own properties, so all
 *     three serialise to `{}`. The data is not truncated, it was never
 *     reached.
 *   - deleted. An `undefined`, a function or a symbol *value* makes
 *     JSON.stringify skip the key entirely, so the key stops existing.
 *   - coerced to null. The same three values in an *array* become null
 *     instead of vanishing, because an array cannot have a hole punched in it
 *     without changing every later index. NaN and Infinity become null
 *     everywhere, since JSON has no spelling for either.
 *   - thrown. A BigInt is the only one that raises, and it raises a TypeError
 *     from the stringifier itself, not from your code.
 *
 * The first three are the expensive ones. You get back a well-formed JSON
 * document, no error is reported anywhere, and the types are gone. The bug
 * surfaces later as `date.getTime is not a function`.
 *
 * superjson does not invent a wire format to fix this. It emits ordinary JSON
 * plus a sidecar of type annotations keyed by path — the `{ json, meta }`
 * split — so the payload stays readable by any JSON parser, and the receiving
 * end replays the annotations to rebuild the types.
 *
 * That split is two steps, and the contract measures the seam between them
 * because it is not where you would guess. `serialize` only rewrites the
 * values it recognises; anything it has no annotation for — a function, a
 * symbol, an Invalid Date — is copied into `json` untouched, still a live
 * reference. The dropping happens in the plain JSON.stringify that `stringify`
 * runs over that result. So `json` is typed JSONValue but is not always a JSON
 * value, and the losses above are still JSON.stringify's, one layer down.
 */

/**
 * One fixture holding every value the two serialisers disagree about. It is a
 * factory rather than a constant because half of these are mutable and the
 * contract compares object identity in places.
 *
 * `big` is 2^53 + 1, the smallest integer a double cannot represent. Reaching
 * for Number() to dodge the BigInt throw silently rounds it down to 2^53,
 * which is the reason the throw is there.
 *
 * `holes` carries the second half of the undefined story: the same value that
 * gets deleted from an object survives as null inside an array.
 */
export function makeFixture() {
  return {
    when: new Date("2024-03-01T12:00:00.000Z"),
    tags: new Map([
      ["release", 3],
      [7, "seven"],
    ]),
    seen: new Set(["a", "b"]),
    big: 9007199254740993n,
    missing: undefined,
    ratio: NaN,
    ceiling: Infinity,
    pattern: /ab+c/gi,
    holes: [1, undefined, 3],
  };
}

export function throughJson(value) {
  return JSON.parse(JSON.stringify(value));
}

export function throughSuperjson(value) {
  return superjson.parse(superjson.stringify(value));
}

/**
 * A class instance is the case superjson cannot solve by inspection. Nothing
 * in the serialised form records which constructor a plain-looking object came
 * from, so unless the class is registered by name on both ends the instance
 * comes back as a plain object: the data survives, the prototype does not, and
 * `format()` is gone.
 */
export class Money {
  constructor(cents) {
    this.cents = cents;
  }

  format() {
    return `$${(this.cents / 100).toFixed(2)}`;
  }
}

/**
 * `registerClass` on the default export mutates a module-level singleton that
 * every importer of superjson shares. That is what makes registration work
 * across files, and also what makes the order of these checks load-bearing:
 * once the contract registers Money, no later assertion can observe the
 * unregistered behaviour again. A private instance is the way to keep a
 * registry to yourself.
 */
export function privateInstance() {
  return new SuperJSON();
}
