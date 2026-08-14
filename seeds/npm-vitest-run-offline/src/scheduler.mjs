/**
 * A one-shot timer and a delayed retry. Nothing here knows about vitest —
 * that is the point: fake timers replace the global setTimeout, so the code
 * being tested keeps calling the real API and never takes a clock argument.
 */

export function scheduleOnce(ms, fn) {
  const id = setTimeout(fn, ms);
  return () => clearTimeout(id);
}

/** Resolves after ms, reporting the clock it saw when it fired. */
export function retryAfter(ms) {
  return new Promise((resolve) => {
    setTimeout(() => resolve(Date.now()), ms);
  });
}
