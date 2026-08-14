import { nanoid } from "nanoid";
import { createHash } from "node:crypto";
import { readFile } from "node:fs/promises";

// Bun implements most of the node: builtins, so an npm package that reaches
// for node:crypto or node:fs keeps working — the usual reason to think
// otherwise is that an import wrote "crypto" instead of "node:crypto",
// which resolves to the npm package of that name on every runtime.
//
// What is NOT interchangeable is the test runner and the install: `bun
// test` uses bun:test, not node:test, and bun install writes its own
// lockfile. Code is portable; tooling is not.
export function id() {
  return nanoid();
}

export function sha256(text: string) {
  return createHash("sha256").update(text).digest("hex");
}

export async function readSelf(path: string) {
  return (await readFile(path, "utf8")).length > 0;
}
