import { fileURLToPath } from "node:url";
import { DatabaseSync } from "node:sqlite";

import { integer, sqliteTable, text } from "drizzle-orm/sqlite-core";
import { drizzle } from "drizzle-orm/sqlite-proxy";
import { migrate } from "drizzle-orm/sqlite-proxy/migrator";

/**
 * drizzle-orm on top of node:sqlite, with no native module, no server and no
 * network.
 *
 * The import people reach for first, `drizzle-orm/node-sqlite`, does not
 * exist in 0.45.2 — measured against the published exports map, which has
 * better-sqlite3, bun-sqlite, expo-sqlite, op-sqlite and sqlite-proxy and no
 * node-sqlite. That entry point arrives with the 1.0.0 release candidates:
 * 1.0.0-rc.1 already lists ./node-sqlite, ./node-sqlite/driver and
 * ./node-sqlite/migrator. So on the current `latest` the way to reach Node's
 * built-in SQLite is sqlite-proxy, drizzle's bring-your-own-executor driver,
 * with node:sqlite as the executor.
 *
 * One thing this is NOT is a workaround for better-sqlite3 being
 * uninstallable. Measured, because the objection is stale: better-sqlite3
 * 13.0.3 installs under the resolve stage's --ignore-scripts on node:22-alpine
 * and opens a database, since 13.x ships prebuilds/linuxmusl-x64.node inside
 * the tarball and declares no install script. The `install: prebuild-install
 * || node-gyp rebuild` that made --ignore-scripts fatal was last seen in 11.x.
 * The reason to prefer node:sqlite is that it is not a native addon at all,
 * not that the addon cannot be had.
 *
 * The trap in wiring the two together is the row shape. drizzle reads the
 * proxy's answer POSITIONALLY — mapResultRow indexes row[0], row[1] against
 * the selected fields and never looks at a key. node:sqlite's
 * StatementSync.all() returns objects. Handing those back is not an error at
 * any layer: you get the right number of rows, with the right keys, and every
 * value undefined. drizzleOverObjectRows below exists so that failure is
 * pinned by an assertion instead of described.
 */

export const users = sqliteTable("users", {
  id: integer("id").primaryKey({ autoIncrement: true }),
  email: text("email").notNull().unique(),
  displayName: text("display_name").notNull(),
  // The table declares DEFAULT 'user' for this column and the schema
  // deliberately does not repeat it. drizzle fills omitted columns with a
  // literal null rather than leaving them out of the statement, so the SQL
  // default never gets a chance to apply and a NOT NULL column with a
  // perfectly good default still fails at runtime. Pinned in the contract.
  role: text("role").notNull(),
  loginCount: integer("login_count").notNull().default(0),
});

/**
 * Opens an in-memory database and wraps it in the drizzle proxy driver.
 * `:memory:` is what keeps this offline: no server to reach, nothing on disk.
 * The raw handle comes back beside the drizzle instance because the migration
 * SQL the migrator hands back is a plain string with no parameters, so it goes
 * straight to sqlite.exec rather than back through the query builder.
 */
export function openDatabase() {
  const sqlite = new DatabaseSync(":memory:");

  const proxy = async (query, params, method) => {
    if (method === "run") {
      sqlite.prepare(query).run(...params);
      return { rows: [] };
    }
    const statement = sqlite.prepare(query);
    // Positional rows, not objects. This is the whole trick.
    statement.setReturnArrays(true);
    const rows = statement.all(...params);
    // `get` wants the row itself, not a list holding it, and undefined when
    // there is none: mapGetResult treats a falsy row as "no result", while an
    // empty array would read as a row whose every column is undefined.
    return { rows: method === "get" ? rows[0] : rows };
  };

  return { sqlite, db: drizzle(proxy) };
}

/**
 * The same wiring with setReturnArrays(true) left out, kept so the contract
 * can assert what going wrong looks like. Do not copy this one.
 */
export function drizzleOverObjectRows(sqlite) {
  return drizzle(async (query, params, method) => {
    const rows = sqlite.prepare(query).all(...params);
    return { rows: method === "get" ? rows[0] : rows };
  });
}

/**
 * Runs the committed migration folder. The proxy migrator splits the work in
 * a way the name hides: its own bookkeeping goes through the driver, not
 * through this callback. It runs CREATE TABLE IF NOT EXISTS
 * __drizzle_migrations and then SELECT id, hash, created_at ... LIMIT 1
 * itself, reads drizzle/meta/_journal.json, and hands the callback only the
 * migration SQL of every entry whose `when` is newer than the created_at it
 * just read, each file split on --> statement-breakpoint, plus the INSERT
 * that records it. So the callback never sees the migrations table being
 * created, and the driver has to answer more than "run": that SELECT arrives
 * as method "values". Both are pinned in the contract.
 *
 * Returning the list is what makes "did the second run do nothing?" a
 * checkable question rather than a claim.
 */
export async function applyMigrations({ sqlite, db }) {
  const executed = [];
  await migrate(
    db,
    async (queries) => {
      for (const query of queries) {
        executed.push(query);
        sqlite.exec(query);
      }
    },
    { migrationsFolder: fileURLToPath(new URL("../drizzle", import.meta.url)) },
  );
  return executed;
}
