import { expect, it, vi } from "vitest";

import { ConfigError, loadConfig, normalize } from "../src/config.mjs";
import { retryAfter, scheduleOnce } from "../src/scheduler.mjs";

/**
 * Five traps, one per test. Where two matchers disagree, both are asserted, so
 * the disagreement itself is what is on record rather than one side of it.
 */

it("toEqual ignores an own property whose value is undefined; toStrictEqual does not", () => {
  const config = normalize({ name: "worker" });

  // The property is really there. Nothing about it is optional or missing.
  expect(Object.hasOwn(config, "timeout")).toBe(true);
  expect(Object.keys(config)).toEqual(["name", "retries", "timeout"]);

  // toEqual treats undefined as absent, so this passes and reads like the
  // object has two keys.
  expect(config).toEqual({ name: "worker", retries: 3 });

  // toStrictEqual compares the key sets, so the same pair disagrees: a review
  // swaps toEqual for toStrictEqual, nothing about the code changes, and the
  // test fails.
  expect(config).not.toStrictEqual({ name: "worker", retries: 3 });
  expect(config).toStrictEqual({ name: "worker", retries: 3, timeout: undefined });

  // The other half of toStrictEqual: the prototype has to match too. A class
  // instance and a plain object with identical fields are toEqual and are not
  // toStrictEqual.
  class Config {
    constructor() {
      this.name = "worker";
    }
  }
  expect(new Config()).toEqual({ name: "worker" });
  expect(new Config()).not.toStrictEqual({ name: "worker" });
});

it("advanceTimersByTime runs a setTimeout without waiting for it, and moves Date", () => {
  vi.useFakeTimers();
  try {
    vi.setSystemTime(new Date("2026-01-01T00:00:00.000Z"));
    const start = Date.now();
    const fired = [];
    scheduleOnce(1000, () => fired.push(Date.now()));

    // One millisecond short is not close enough — the callback has not run.
    vi.advanceTimersByTime(999);
    expect(fired).toEqual([]);

    vi.advanceTimersByTime(1);
    expect(fired).toHaveLength(1);

    // useFakeTimers replaces Date as well as the timer functions, and the
    // clock moved by exactly what was advanced. Code that stamps Date.now()
    // inside a timer sees the timer's time, not the wall clock.
    expect(fired[0] - start).toBe(1000);
    expect(Date.now() - start).toBe(1000);

    // The cancel path, so that "it fired" is not passing for the wrong reason.
    const cancel = scheduleOnce(500, () => fired.push("cancelled"));
    cancel();
    vi.advanceTimersByTime(5000);
    expect(fired).toHaveLength(1);
  } finally {
    // Without this the fake clock leaks into every later test file in the
    // same worker.
    vi.useRealTimers();
  }
});

it("advanceTimersByTime is synchronous, so a promise resolved by a timer is not settled yet", async () => {
  vi.useFakeTimers();
  try {
    let settled = null;
    const pending = retryAfter(50).then((clock) => {
      settled = clock;
    });

    vi.advanceTimersByTime(50);

    // The timer callback ran and called resolve, but resolve only queues a
    // microtask and advanceTimersByTime never yields, so the .then body has
    // not executed. This is why "the awaited value is undefined" happens
    // under fake timers.
    expect(settled).toBeNull();

    await pending;
    expect(settled).toBe(Date.now());

    // The async variant advances and drains, which is what you want whenever
    // the code under test awaits anything inside a timer.
    let second = null;
    const chained = retryAfter(50).then((clock) => {
      second = clock;
    });
    await vi.advanceTimersByTimeAsync(50);
    expect(second).not.toBeNull();
    await chained;
  } finally {
    vi.useRealTimers();
  }
});

it("a rejection is compared inside the promise .rejects returns, not by the call", async () => {
  await expect(loadConfig({})).rejects.toBeInstanceOf(ConfigError);
  await expect(loadConfig({})).rejects.toThrow("name is required");
  await expect(loadConfig({})).rejects.toMatchObject({ field: "name" });

  // The trap, demonstrated instead of described. This assertion is false —
  // the rejection message is "name is required" — and making it throws
  // nothing: .rejects hands back a thenable, an object carrying then/catch/
  // finally that is not a Promise, and the comparison happens inside the
  // promise it wraps.
  const unawaited = expect(loadConfig({})).rejects.toThrow("not the real message");
  expect(typeof unawaited.then).toBe("function");
  expect(unawaited).not.toBeInstanceOf(Promise);

  // Awaiting that exact expression is the whole difference. Forgetting the
  // await is not the silent pass it is usually described as — vitest 4
  // registers the promise on the running test and auto-awaits it — but what
  // happens instead depends on whether the test function is async, so it is
  // measured in test/not-awaited-rejects.mjs rather than described here.
  let failure = null;
  try {
    await unawaited;
  } catch (error) {
    failure = error;
  }
  expect(failure).toBeInstanceOf(Error);
  expect(failure.message).toContain("not the real message");

  // And the synchronous matcher is not a weaker check, it is the wrong one:
  // an async function returns a rejected promise rather than throwing, so
  // toThrow watches a normal return and reports no error.
  const escaped = [];
  expect(() => escaped.push(loadConfig({}))).not.toThrow();
  await Promise.allSettled(escaped);
});

it("toBe compares with Object.is, so NaN matches NaN and -0 does not match 0", () => {
  // The reason people expect toBe(NaN) to fail.
  expect(NaN === NaN).toBe(false);

  // It passes anyway: toBe is Object.is, not ===, and Object.is(NaN, NaN)
  // is true. toEqual agrees, so on NaN the two matchers do not differ.
  expect(NaN).toBe(NaN);
  expect(NaN).toEqual(NaN);
  expect({ ratio: NaN }).toEqual({ ratio: NaN });
  expect([NaN]).toStrictEqual([NaN]);

  // Where Object.is really does bite is signed zero, which === accepts.
  expect(0 === -0).toBe(true);
  expect(-0).not.toBe(0);
  expect(-0).toBe(-0);
  expect(-0).not.toEqual(0);
});
