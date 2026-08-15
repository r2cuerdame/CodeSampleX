import { atom, computed, map, onMount, STORE_UNMOUNT_DELAY } from "nanostores";

export { STORE_UNMOUNT_DELAY };

// A store module shaped the way an app would really write one: a map keyed by
// sku, and a derived total over it.
export function createCart() {
  const $items = map({ apple: 2 });
  const $count = computed($items, items =>
    Object.values(items).reduce((sum, qty) => sum + qty, 0),
  );
  return { $count, $items };
}

// Records the arguments a listener receives without destructuring them. The
// whole question below is what is in which position, so the recorder must not
// bake in an answer: it keeps each call as a raw array, including its length.
export function recordArgs($store, method = "listen") {
  const calls = [];
  const unbind = $store[method]((...args) => calls.push(args));
  return { calls, unbind };
}

// A store whose mount initializer performs a real side effect. The counters
// stand in for the thing an initializer normally does: open a socket, start an
// interval, subscribe to a server. Counting them is the only way to see the
// mount lifecycle: the only mount state nanostores keeps is an internal
// `active` flag that the published types do not declare, unlike lc and value.
export function createLazyResource(initial = "idle") {
  const counts = { mounted: 0, unmounted: 0 };
  const $store = atom(initial);
  onMount($store, () => {
    counts.mounted += 1;
    return () => {
      counts.unmounted += 1;
    };
  });
  return { $store, counts };
}

// Counts how often the derive callback actually executes, which is not the
// same as how often it is read.
export function createCounted($dep, fn) {
  const counts = { calls: 0 };
  const $derived = computed($dep, value => {
    counts.calls += 1;
    return fn(value);
  });
  return { $derived, counts };
}

// nanostores reports deprecations through console.groupCollapsed + console.trace
// rather than by throwing, so the only way to assert on one is to capture the
// console. warn() also dedupes by exact message text for the life of the
// process, so each message can be observed exactly once.
export function captureConsole(fn) {
  const captured = [];
  const saved = {
    groupCollapsed: console.groupCollapsed,
    groupEnd: console.groupEnd,
    trace: console.trace,
  };
  console.groupCollapsed = text => captured.push(String(text));
  console.groupEnd = () => {};
  console.trace = () => {};
  try {
    return { captured, result: fn() };
  } finally {
    Object.assign(console, saved);
  }
}

export const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));
