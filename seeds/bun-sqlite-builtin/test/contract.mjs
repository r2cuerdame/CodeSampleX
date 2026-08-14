import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { builtinModules } from "node:module";

import { Database, SQLiteError } from "bun:sqlite";

import {
  addAccount,
  findByOwner,
  findByOwnerNamed,
  listOwners,
  openLedger,
  readBalance,
} from "../src/ledger.mjs";

// The capability itself. "bun:sqlite" is not an npm package that happens to
// be installed: node:module lists it as a builtin of this runtime, and this
// project resolved zero dependencies to get it. On Node the import above is
// an unresolvable specifier, so the file cannot even load.
assert.equal(typeof Bun, "object");
assert.ok(process.versions.bun.startsWith("1."), process.versions.bun);
assert.ok(builtinModules.includes("bun:sqlite"));
const pkg = JSON.parse(readFileSync(new URL("../package.json", import.meta.url), "utf8"));
assert.deepEqual(Object.keys(pkg.dependencies ?? {}), []);

// And the portable-looking alternative is not one on this runtime: Bun
// 1.3.14 has no node:sqlite, so bun:sqlite is not a preference here.
await assert.rejects(import("node:sqlite"), /No such built-in module/);

// Opening ":memory:" gives a private database, not a shared one. Two calls
// are two databases, and the second cannot see the first one's rows.
const db = openLedger();
assert.equal(db.filename, ":memory:");
const inserted = addAccount(db, "ada", 100);
const other = openLedger();
assert.deepEqual(other.query("SELECT owner FROM accounts").all(), []);
other.close();

// The options argument replaces the open flags rather than overriding
// defaults, so an empty object is not the same as no object: it opens with
// no access mode and SQLite refuses. Anything that builds options
// programmatically hits this on the day the object comes out empty.
assert.throws(
  () => new Database(":memory:", {}),
  (err) => {
    assert.ok(err instanceof SQLiteError);
    assert.equal(err.code, "SQLITE_MISUSE");
    return true;
  },
);
assert.equal(new Database(":memory:").filename, ":memory:");
// Naming a strict or safeIntegers key is enough to put the default access
// mode back, at either value, which is why src/ledger.mjs always passes both.
assert.equal(new Database(":memory:", { safeIntegers: false }).filename, ":memory:");
assert.equal(new Database(":memory:", { strict: false }).filename, ":memory:");
// An access-mode key only counts when it is true, so the options object that
// looks most deliberate is also one of the broken ones: { create: false }
// reads as "open an existing file, do not create it" and lands on the same
// zero flags as {}.
assert.throws(
  () => new Database(":memory:", { create: false }),
  (err) => {
    assert.equal(err.code, "SQLITE_MISUSE");
    return true;
  },
);
assert.equal(new Database(":memory:", { create: true }).filename, ":memory:");

// .run() returns the write receipt and never rows: changes counts affected
// rows, lastInsertRowid is a rowid.
assert.deepEqual(inserted, { changes: 1, lastInsertRowid: 1 });
assert.equal(typeof inserted.changes, "number");
assert.equal(typeof inserted.lastInsertRowid, "number");
assert.deepEqual(db.prepare("SELECT owner FROM accounts").run(), {
  changes: 0,
  lastInsertRowid: 1,
});

// Binding is binding, not string building. This owner name closes a quote,
// ends the statement and starts a DROP TABLE; it comes back byte for byte as
// one row's worth of text, and the table is still there.
const injection = "Bobby'); DROP TABLE accounts; --";
addAccount(db, injection, 5);
assert.deepEqual(findByOwner(db, injection), { id: 2, owner: injection, balance: 5 });
assert.deepEqual(
  db.query("SELECT count(*) AS n FROM sqlite_master WHERE name = 'accounts'").get(),
  { n: 1 },
);

// .get() hands back the first row as a plain object — and null, not
// undefined, when there is no row. better-sqlite3 returns undefined here, so
// `if (row === undefined)` ported from it is dead code.
const miss = findByOwner(db, "nobody");
assert.equal(miss, null);
assert.equal(miss === undefined, false);

// .all() is always an array, empty when nothing matched.
assert.deepEqual(listOwners(db), [{ owner: "ada" }, { owner: injection }]);
const none = db.query("SELECT owner FROM accounts WHERE owner = ?").all("nobody");
assert.ok(Array.isArray(none));
assert.deepEqual(none, []);

// lastInsertRowid belongs to the connection, not to the statement that just
// ran: a DELETE that changed nothing still reports the last INSERT's rowid.
// Reading it as "the row I just wrote" is wrong for every non-INSERT.
const deleted = db.prepare("DELETE FROM accounts WHERE owner = ?").run("nobody");
assert.deepEqual(deleted, { changes: 0, lastInsertRowid: 2 });

// Positional binding is counted and enforced.
assert.throws(
  () => db.prepare("SELECT ? AS a, ? AS b").get("only one"),
  /expected 2 values, received 1/,
);

// Named binding, default mode: the sigil is part of the key. Getting that
// wrong is the quiet failure in this API — { owner } instead of { $owner }
// does not throw and does not warn, it leaves the placeholder NULL and the
// caller reads "no such account".
assert.deepEqual(findByOwnerNamed(db, { $owner: "ada" }), {
  id: 1,
  owner: "ada",
  balance: 100,
});
assert.equal(findByOwnerNamed(db, { owner: "ada" }), null);

// Strict mode inverts it, and turns the silence into an exception: bare keys
// bind, the $ key is now the unbound one, and an unbound parameter raises
// instead of reading as NULL.
const strict = openLedger({ strict: true });
addAccount(strict, "ada", 100);
assert.deepEqual(findByOwnerNamed(strict, { owner: "ada" }), {
  id: 1,
  owner: "ada",
  balance: 100,
});
assert.throws(() => findByOwnerNamed(strict, { $owner: "ada" }), /Missing parameter "owner"/);
strict.close();

// db.query() memoises the Statement on the exact SQL string and returns the
// same object every time; db.prepare() builds a new one per call.
const sql = "SELECT balance FROM accounts WHERE owner = ?";
assert.equal(db.query(sql), db.query(sql));
assert.notEqual(db.prepare(sql), db.prepare(sql));
assert.notEqual(db.query(sql), db.query("SELECT  balance FROM accounts WHERE owner = ?"));

// Which matters because per-statement settings live on that shared object.
// safeIntegers() set on a cached query is still set the next time anybody
// asks for the same SQL, in another module, and their numbers arrive as
// BigInts. That is why src/ledger.mjs uses prepare() for it.
db.query(sql).safeIntegers(true);
assert.equal(typeof db.query(sql).get("ada").balance, "bigint");
db.query(sql).safeIntegers(false);
assert.equal(typeof db.query(sql).get("ada").balance, "number");

// Integers wider than 2^53. The write is lossless — SQLite stores int64 —
// and the default read is not: the value comes back as a rounded double,
// with no error anywhere. safeIntegers is what returns the exact value, as a
// BigInt, and reading the same stored row both ways proves the loss is on
// the read side.
const huge = 9007199254740993n; // Number.MAX_SAFE_INTEGER + 2
addAccount(db, "treasury", huge);
const rounded = readBalance(db, "treasury");
assert.equal(typeof rounded, "number");
assert.equal(rounded, 9007199254740992);
assert.notEqual(BigInt(rounded), huge);
const exact = readBalance(db, "treasury", { exact: true });
assert.equal(typeof exact, "bigint");
assert.equal(exact, huge);

// As a constructor option it applies to every read on the connection,
// including lastInsertRowid — but not to changes, which stays a number.
const exactDb = openLedger({ safeIntegers: true });
const bigWrite = addAccount(exactDb, "treasury", huge);
assert.equal(typeof bigWrite.changes, "number");
assert.equal(typeof bigWrite.lastInsertRowid, "bigint");
assert.equal(exactDb.query("SELECT balance FROM accounts").get().balance, huge);
exactDb.close();

// There is no connection-level toggle to reach for afterwards: the
// constructor option and Statement.safeIntegers() are the two switches.
assert.equal(typeof db.safeIntegers, "undefined");
assert.equal(typeof db.prepare(sql).safeIntegers, "function");

// Errors are SQLiteError, exported from the same builtin, and carry SQLite's
// extended result code — which is how a duplicate is told apart from every
// other constraint without matching on message text.
assert.throws(
  () => addAccount(db, "ada", 1),
  (err) => {
    assert.ok(err instanceof SQLiteError);
    assert.equal(err.code, "SQLITE_CONSTRAINT_UNIQUE");
    return true;
  },
);

db.close();
console.log("contract ok");
