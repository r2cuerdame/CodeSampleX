import { expect, test } from "bun:test";
import { id, readSelf, sha256 } from "../src/index.ts";

test("an npm package works unchanged on bun", () => {
  const value = id();
  expect(value).toHaveLength(21);
  expect(value).toMatch(/^[A-Za-z0-9_-]{21}$/);
});

test("node: builtins are implemented", () => {
  // Same digest node produces — the algorithm is not reimplemented.
  expect(sha256("codesamplex")).toHaveLength(64);
  expect(sha256("")).toBe(
    "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
  );
});

test("node:fs/promises works", async () => {
  expect(await readSelf("package.json")).toBe(true);
});

test("Bun global exists here and would not on node", () => {
  expect(typeof Bun).toBe("object");
  expect(typeof Bun.version).toBe("string");
});
