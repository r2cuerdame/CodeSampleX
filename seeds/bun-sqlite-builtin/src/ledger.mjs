import { Database } from "bun:sqlite";

/**
 * SQLite on Bun, without installing anything.
 *
 * bun:sqlite is compiled into the Bun binary. No npm package sits behind the
 * "bun:sqlite" specifier, which is why this project has no dependencies and
 * no native build step — and equally why none of this file runs on Node: the
 * specifier resolves in Bun and nowhere else. Bun 1.3.14 does not ship
 * node:sqlite either, so on this runtime the built-in module is the option,
 * not the shortcut.
 *
 * The API reads like better-sqlite3 and differs from it in the places that
 * cost an afternoon each:
 *
 *   - .get() returns null for no rows, where better-sqlite3 returns undefined.
 *     `if (row === undefined)` ported from it never fires again.
 *   - named parameters are keyed WITH their sigil ({ $owner }) unless the
 *     database was opened strict, and a mismatched key is not an error: the
 *     placeholder stays NULL and the query quietly returns nothing.
 *   - db.query() memoises the Statement per SQL string and hands the same
 *     object to every caller, while db.prepare() builds a new one, so a
 *     per-statement setting on a query() result outlives the code that set it.
 *   - integers wider than 2^53 are stored exactly and read back as rounded
 *     doubles unless safeIntegers is on. Nothing throws; the number is just
 *     quietly wrong.
 */

/**
 * Opens a private in-memory database. Every ":memory:" is its own database:
 * two connections do not see each other's tables.
 *
 * Both option keys are always passed, deliberately. The options argument is
 * not "defaults plus overrides" — Bun sets the open flags to zero as soon as
 * options is an object, and only readonly, create or readwrite set to TRUE,
 * or a strict or safeIntegers key at any value, puts an access mode back. So
 * `new Database(file, {})`, and any object built from options that happened
 * to come out empty, opens with no access mode and throws SQLITE_MISUSE,
 * while `new Database(file)` works. The key half of that is measured, not
 * assumed: `{ create: false }` reads like "open an existing file, do not
 * create it" and lands on the same zero flags as `{}`, so it throws too.
 * Naming both keys here makes all of it unreachable.
 */
export function openLedger({ strict = false, safeIntegers = false } = {}) {
  const db = new Database(":memory:", { strict, safeIntegers });
  db.run(`CREATE TABLE accounts (
    id INTEGER PRIMARY KEY,
    owner TEXT NOT NULL UNIQUE,
    balance INTEGER NOT NULL
  )`);
  return db;
}

/**
 * Positional binding. The value goes to SQLite as a value and is never
 * spliced into the SQL text, so an owner name full of quotes and semicolons
 * is just a long name.
 */
export function addAccount(db, owner, balance) {
  return db
    .prepare("INSERT INTO accounts (owner, balance) VALUES (?, ?)")
    .run(owner, balance);
}

export function findByOwner(db, owner) {
  return db.query("SELECT id, owner, balance FROM accounts WHERE owner = ?").get(owner);
}

/**
 * Named binding, where the two modes are mutually exclusive rather than
 * merely different: a database opened normally wants { $owner: "ada" } and
 * treats { owner: "ada" } as nothing to bind, while one opened with
 * strict: true wants { owner: "ada" } and raises Missing parameter for the
 * $-prefixed key. Strict is the mode to want — the failure is an exception
 * instead of an empty result set.
 */
export function findByOwnerNamed(db, params) {
  return db.query("SELECT id, owner, balance FROM accounts WHERE owner = $owner").get(params);
}

export function listOwners(db) {
  return db.query("SELECT owner FROM accounts ORDER BY id").all();
}

/**
 * Reads a balance, optionally as an exact BigInt.
 *
 * prepare() is deliberate: safeIntegers is a property of the statement, and a
 * statement from query() is shared with everyone else who asks for that SQL
 * string. Turning it on there changes the type their reads come back as.
 */
export function readBalance(db, owner, { exact = false } = {}) {
  const stmt = db.prepare("SELECT balance FROM accounts WHERE owner = ?");
  if (exact) stmt.safeIntegers(true);
  const row = stmt.get(owner);
  return row === null ? null : row.balance;
}
