import { expect, it } from "vitest";

import { loadConfig } from "../src/config.mjs";

/**
 * This file is supposed to fail. Both assertions are false and neither is
 * awaited, which is the mistake everyone makes once.
 *
 * The runner starts it as its own vitest run and asserts how it fails, because
 * that is the only way to record the behaviour without shipping a red suite:
 * the passing run's include (test/*.test.mjs) does not match this name, so it
 * is never collected there.
 *
 * The two test functions differ only in the `async` keyword, and vitest 4
 * reports them differently. See test/contract.mjs for what was measured.
 */

it("sync test function, forgotten await", () => {
  expect(loadConfig({})).rejects.toThrow("not the real message");
});

it("async test function, forgotten await", async () => {
  expect(loadConfig({})).rejects.toThrow("not the real message either");
});
