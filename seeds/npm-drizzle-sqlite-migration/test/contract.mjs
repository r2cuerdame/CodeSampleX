import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import { DatabaseSync } from "node:sqlite";
import { fileURLToPath } from "node:url";
import { eq, sql } from "drizzle-orm";
import { drizzle } from "drizzle-orm/sqlite-proxy";
import { migrate } from "drizzle-orm/sqlite-proxy/migrator";

import {
  applyMigrations,
  drizzleOverObjectRows,
  openDatabase,
  users,
} from "../src/db.mjs";

// drizzle-orm's exports map lists every driver and not "./package.json", so
// the usual way to read a dependency's own version off disk throws instead of
// returning it. Reading the file is the way in.
const require = createRequire(import.meta.url);
assert.throws(() => require("drizzle-orm/package.json"), {
  code: "ERR_PACKAGE_PATH_NOT_EXPORTED",
});
const version = JSON.parse(
  readFileSync(new URL("../node_modules/drizzle-orm/package.json", import.meta.url)),
).version;
assert.equal(version, "0.45.2");

// The driver choice, stated as an assertion rather than as a comment. The
// natural import for "drizzle plus the SQLite that ships inside Node" is not
// published on this version — it exists only on the 1.0.0 release candidates
// — so sqlite-proxy is what a working answer has to use today.
const nodeSqliteDriver = await import("drizzle-orm/node-sqlite").then(
  () => null,
  (err) => err,
);
assert.equal(nodeSqliteDriver?.code, "ERR_PACKAGE_PATH_NOT_EXPORTED");
assert.ok(await import("drizzle-orm/sqlite-proxy"));

const handle = openDatabase();
const { db, sqlite } = handle;

// The migration folder is hand-written and committed: two DDL statements
// split on drizzle-kit's --> statement-breakpoint marker, plus the row the
// migrator writes into __drizzle_migrations to remember it did so.
const firstRun = await applyMigrations(handle);
assert.equal(firstRun.length, 3);
assert.match(firstRun[0], /CREATE TABLE `users`/);
assert.match(firstRun[1], /CREATE UNIQUE INDEX `users_email_unique`/);
assert.match(firstRun[2], /INSERT INTO `__drizzle_migrations`/);

// Second run is a no-op. The migrator compares the journal's `when` against
// the newest created_at it finds, so re-running against a live database
// applies nothing rather than failing on "table users already exists".
assert.deepEqual(await applyMigrations(handle), []);
assert.equal(
  sqlite.prepare("select count(*) as n from __drizzle_migrations").get().n,
  1,
);

// The migrations table exists even though the callback was never asked to
// create it: the migrator runs its own bookkeeping through the driver. Which
// means a proxy that only handles the methods a query builder uses is not
// enough — the bookkeeping SELECT arrives as "values", and a driver that
// switches on "all" alone never answers it.
assert.ok(!firstRun.some((query) => /IF NOT EXISTS/i.test(query)));
const seenMethods = [];
const probe = new DatabaseSync(":memory:");
await migrate(
  drizzle(async (query, params, method) => {
    seenMethods.push(method);
    if (method === "run") {
      probe.prepare(query).run(...params);
      return { rows: [] };
    }
    const statement = probe.prepare(query);
    statement.setReturnArrays(true);
    return { rows: statement.all(...params) };
  }),
  async () => {},
  { migrationsFolder: fileURLToPath(new URL("../drizzle", import.meta.url)) },
);
assert.deepEqual(seenMethods, ["run", "values"]);

// .returning() hands back the row the database actually stored, not the
// object that went in: the id AUTOINCREMENT assigned is there, and so is the
// login_count the insert never mentioned.
const inserted = await db
  .insert(users)
  .values({ email: "ada@example.com", displayName: "Ada Lovelace", role: "admin" })
  .returning();
assert.deepEqual(inserted, [
  {
    id: 1,
    email: "ada@example.com",
    displayName: "Ada Lovelace",
    role: "admin",
    loginCount: 0,
  },
]);

await db.insert(users).values([
  { email: "grace@example.com", displayName: "Grace Hopper", role: "user" },
  {
    email: "katherine@example.com",
    displayName: "Katherine Johnson",
    role: "user",
    loginCount: 7,
  },
]);

// A select comes back keyed by the schema's JS names rather than the column
// names, which is the whole of what drizzle does to a row here. It does not
// touch the values: for a plain integer or text column mapFromDriverValue is
// the identity function, so `typeof id === "number"` is node:sqlite's doing
// and not a conversion drizzle performed. Worth knowing before trusting a
// declared type to coerce anything.
const all = await db.select().from(users);
assert.equal(all.length, 3);
assert.deepEqual(Object.keys(all[0]), [
  "id",
  "email",
  "displayName",
  "role",
  "loginCount",
]);
assert.equal(typeof all[0].id, "number");
assert.equal(typeof all[0].displayName, "string");
assert.equal(typeof all[2].loginCount, "number");
assert.equal(all[2].loginCount, 7);
assert.equal(users.loginCount.mapFromDriverValue("7"), "7");

// .where(eq(...)) filters in SQL, not in JavaScript: the bound value reaches
// the statement as a parameter and only one row comes back.
const filtered = await db
  .select()
  .from(users)
  .where(eq(users.email, "grace@example.com"));
assert.equal(filtered.length, 1);
assert.equal(filtered[0].displayName, "Grace Hopper");
assert.deepEqual(db.select().from(users).where(eq(users.id, 2)).toSQL().params, [2]);

// .get() on a miss is undefined, not an empty row. Worth an assertion because
// the proxy driver has to return undefined rather than [] for that to hold.
assert.equal(await db.select().from(users).where(eq(users.id, 99)).get(), undefined);

// Omitting a notNull column is a TypeScript error at build time. It is also a
// runtime error, which is the half a JavaScript contract can prove, and the
// runtime half is the one that matters when the insert values come from a
// parsed request body that the compiler never saw.
const missingDisplayName = await db
  .insert(users)
  .values({ email: "alan@example.com", role: "user" })
  .catch((err) => err);
assert.equal(missingDisplayName.cause.message, "NOT NULL constraint failed: users.display_name");

// And the sharper version of the same trap. `role` has DEFAULT 'user' in the
// migration, so the SQL default would have covered the omission — except
// drizzle does not omit the column. It writes a literal null into the values
// list, which beats the default and hits the NOT NULL constraint. A column's
// SQL default only helps if the drizzle schema repeats it as .default().
const missingRole = await db
  .insert(users)
  .values({ email: "alan@example.com", displayName: "Alan Turing" })
  .catch((err) => err);
assert.equal(missingRole.cause.message, "NOT NULL constraint failed: users.role");
assert.match(
  missingRole.query,
  /insert into "users" \("id", "email", "display_name", "role", "login_count"\) values \(null, \?, \?, null, \?\)/,
);

// A unique violation throws, and the SQLite message is NOT on the error you
// catch. drizzle 0.45 wraps every failed query in DrizzleQueryError, whose
// own message is the SQL and the parameters; the driver's error is the cause.
// Matching on err.message for "UNIQUE constraint failed" therefore never
// fires, which is the measurement that corrected the first draft of this file.
const duplicate = await db
  .insert(users)
  .values({ email: "ada@example.com", displayName: "Ada again", role: "user" })
  .catch((err) => err);
assert.equal(duplicate.constructor.name, "DrizzleQueryError");
assert.ok(!duplicate.message.includes("UNIQUE constraint failed"));
assert.match(duplicate.message, /^Failed query: insert into "users" .*\nparams: /s);
assert.equal(duplicate.cause.message, "UNIQUE constraint failed: users.email");
assert.equal(duplicate.cause.code, "ERR_SQLITE_ERROR");
// SQLITE_CONSTRAINT_UNIQUE vs SQLITE_CONSTRAINT_NOTNULL. The extended result
// codes tell the two constraint failures apart without parsing English.
assert.equal(duplicate.cause.errcode, 2067);
assert.equal(missingRole.cause.errcode, 1299);
// The wrapper is not useless: it carries the statement and the bound values.
assert.deepEqual(duplicate.params, ["ada@example.com", "Ada again", "user", 0]);

// The security-relevant one. An interpolated value inside sql`` is bound, not
// concatenated: the statement holds a ?, and the quote-carrying value travels
// beside it in params. So the classic tautology stays a string and matches the
// zero users whose email literally is that string.
const hostile = "ada@example.com' OR '1'='1";
const bound = db.select({ id: users.id }).from(users).where(sql`${users.email} = ${hostile}`);
const boundSql = bound.toSQL();
assert.equal(boundSql.sql, 'select "id" from "users" where "users"."email" = ?');
assert.deepEqual(boundSql.params, [hostile]);
assert.ok(!boundSql.sql.includes("'"));
assert.deepEqual(await bound, []);

// The contrast, so the assertion above is about escaping and not about the
// value being harmless. sql.raw is the escape hatch that concatenates: the
// same string lands in the statement text, the tautology fires, and the query
// returns every row in the table.
const concatenated = db
  .select({ id: users.id })
  .from(users)
  .where(sql.raw(`"users"."email" = '${hostile}'`));
const concatenatedSql = concatenated.toSQL();
assert.deepEqual(concatenatedSql.params, []);
assert.ok(concatenatedSql.sql.endsWith("OR '1'='1'"));
assert.deepEqual(await concatenated, [{ id: 1 }, { id: 2 }, { id: 3 }]);

// Same story for eq(): a value carrying a statement terminator is one
// parameter, and the table it names is still there afterwards.
const dropper = "ada@example.com'; DROP TABLE users; --";
assert.deepEqual(db.select().from(users).where(eq(users.email, dropper)).toSQL().params, [
  dropper,
]);
assert.deepEqual(await db.select().from(users).where(eq(users.email, dropper)), []);
assert.equal((await db.select().from(users)).length, 3);

// Finally, what the driver's setReturnArrays(true) is buying. Give drizzle
// node:sqlite's default object rows and nothing complains: same row count,
// same keys, every value undefined. A silent wrong answer is the reason that
// line is in the driver and this assertion is in the contract.
const broken = await drizzleOverObjectRows(sqlite).select().from(users);
assert.equal(broken.length, 3);
assert.deepEqual(Object.keys(broken[0]), [
  "id",
  "email",
  "displayName",
  "role",
  "loginCount",
]);
assert.equal(broken[0].id, undefined);
assert.equal(broken[0].email, undefined);

console.log("contract ok: drizzle-orm", version, "on", process.version);
