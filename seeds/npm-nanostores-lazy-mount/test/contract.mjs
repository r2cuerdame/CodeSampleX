import assert from "node:assert/strict";

import {
  allTasks,
  atom,
  batch,
  cleanStores,
  computed,
  deepMap,
  keepMount,
  listenKeys,
  map,
  readonlyType,
  task,
} from "nanostores";

import {
  captureConsole,
  createCart,
  createCounted,
  createLazyResource,
  recordArgs,
  sleep,
  STORE_UNMOUNT_DELAY,
} from "../src/stores.mjs";

// ---------------------------------------------------------------------------
// Listener argument order
// ---------------------------------------------------------------------------

// The obvious wrong version is $items.listen((value, changedKey) => ...),
// written by anyone who reasons "a map listener tells you which key moved, so
// the key comes right after the value". It does not. Every store — atom and
// map alike — calls listeners as (value, oldValue, changedKey). Position 2 is
// the PREVIOUS WHOLE OBJECT, so that code compares an object against a key
// string and silently matches nothing.
const { $items } = createCart();
const mapCalls = recordArgs($items);
$items.setKey("apple", 5);

assert.equal(mapCalls.calls.length, 1);
const [mapValue, mapOldValue, mapChangedKey] = mapCalls.calls[0];
assert.deepEqual(mapValue, { apple: 5 });
assert.deepEqual(mapOldValue, { apple: 2 });
assert.equal(mapChangedKey, "apple");
// Stated as the trap rather than as the fix: argument 2 is not the key.
assert.notEqual(mapOldValue, "apple");
assert.equal(typeof mapOldValue, "object");
mapCalls.unbind();

// An atom fills the same three slots and simply leaves the third empty, which
// is why the order cannot be "key second" — there is no key on an atom to put
// there.
const $plain = atom(1);
const atomCalls = recordArgs($plain);
$plain.set(2);
assert.equal(atomCalls.calls.length, 1);
assert.equal(atomCalls.calls[0].length, 3);
assert.deepEqual(atomCalls.calls[0].slice(0, 2), [2, 1]);
assert.equal(atomCalls.calls[0][2], undefined);
atomCalls.unbind();

// subscribe() differs from listen() only by an immediate first call — and that
// first call is made with EXACTLY ONE argument, not with (value, oldValue).
// A listener written as (value, oldValue) therefore cannot tell its priming
// call from a real change whose previous value happened to be undefined,
// because arguments.length is the only thing that separates them.
const $primed = atom("first");
const subCalls = recordArgs($primed, "subscribe");
assert.equal(subCalls.calls.length, 1);
assert.deepEqual(subCalls.calls[0], ["first"]);
assert.equal(subCalls.calls[0].length, 1);
$primed.set("second");
assert.equal(subCalls.calls[1].length, 3);
subCalls.unbind();

// ---------------------------------------------------------------------------
// map.setKey and identity
// ---------------------------------------------------------------------------

// setKey(key, undefined) DELETES the key. It does not store undefined. The
// obvious wrong version is clearing a field with $m.setKey('coupon', undefined)
// and then reading 'coupon' in $m.get() as a "was it ever set" test, which now
// answers false. Object.keys shrinks and JSON.stringify drops the field.
const $profile = map({ coupon: "SAVE10", name: "ada" });
$profile.setKey("coupon", undefined);
assert.deepEqual($profile.get(), { name: "ada" });
assert.equal("coupon" in $profile.get(), false);
assert.deepEqual(Object.keys($profile.get()), ["name"]);
assert.equal(JSON.stringify($profile.get()), '{"name":"ada"}');

// Deleting an absent key is not a change, so it notifies nobody. The delete
// branch is guarded on `key in value`, and the fallback comparison
// undefined !== undefined is false.
const absent = recordArgs($profile);
$profile.setKey("coupon", undefined);
assert.equal(absent.calls.length, 0);
absent.unbind();

// set() compares with === and nothing deeper, so handing a map an
// equal-looking NEW object is a change and re-renders every subscriber, while
// handing back its own current object is not.
const $ident = map({ k: 1 });
const identCalls = recordArgs($ident);
$ident.set({ k: 1 });
assert.equal(identCalls.calls.length, 1);
$ident.set($ident.get());
assert.equal(identCalls.calls.length, 1);
identCalls.unbind();

// ---------------------------------------------------------------------------
// computed() is not read-only at runtime
// ---------------------------------------------------------------------------

// readonlyType is an IDENTITY FUNCTION. It narrows a type and returns the very
// same object, so nothing about read-only-ness survives to runtime.
const $sourceForReadonly = atom(1);
assert.equal(readonlyType($sourceForReadonly), $sourceForReadonly);

// A computed store IS an atom, so .set() on it neither throws nor is ignored:
// it takes the value. The obvious wrong belief is that a derived store is
// protected and that a stray .set() will fail loudly in tests.
const $price = atom(10);
const $withTax = computed($price, n => n * 2);
assert.equal($withTax.get(), 20);
$withTax.set(999);
assert.equal($withTax.get(), 999);

// Worse than being accepted, the written value STICKS. Re-reading does not
// repair it, because the recompute short-circuits when the dependency values
// are unchanged. Nothing heals it but a real move in one of its OWN
// dependencies — an unrelated store changing is not enough, which the epoch
// section below measures separately.
assert.equal($withTax.get(), 999);
$price.set(11);
assert.equal($withTax.get(), 22);

// ---------------------------------------------------------------------------
// .value and .get() disagree
// ---------------------------------------------------------------------------

// A freshly created computed has never run its callback. Reading the .value
// property — which is a real, present, non-throwing property — yields
// undefined, while .get() yields the computed number. Server-side code that
// reaches for .value to avoid "subscribing" during a render serialises
// undefined into the payload.
const { $count, $items: $cartItems } = createCart();
assert.equal($count.value, undefined);
assert.equal($count.get(), 2);
assert.equal($count.value, 2);
$cartItems.setKey("pear", 3);
assert.equal($count.get(), 5);

// ---------------------------------------------------------------------------
// Reading is not passive: .get() runs the mount initializer
// ---------------------------------------------------------------------------

// The single most surprising consequence of lazy mounting. atom.get() is
// implemented as "if nobody is listening, attach a no-op listener and
// immediately drop it", which MOUNTS the store. A bare read therefore fires
// onMount — opening the socket, starting the interval — even though the
// listener count is back to 0 the instant get() returns and nothing will ever
// deliver an update.
const resource = createLazyResource("idle");
assert.equal(resource.counts.mounted, 0);
assert.equal(resource.$store.lc, 0);

assert.equal(resource.$store.get(), "idle");
assert.equal(resource.counts.mounted, 1);
assert.equal(resource.$store.lc, 0);

// Repeat reads inside the delay window do not re-mount, because the store is
// still flagged active.
resource.$store.get();
resource.$store.get();
assert.equal(resource.counts.mounted, 1);
assert.equal(resource.counts.unmounted, 0);

// ---------------------------------------------------------------------------
// Unmount is deferred by STORE_UNMOUNT_DELAY
// ---------------------------------------------------------------------------

// The cleanup returned from onMount does NOT run when the last listener
// leaves. It is scheduled on a setTimeout of exactly 1000 ms, so a test that
// unsubscribes and asserts teardown on the next line always sees zero.
assert.equal(STORE_UNMOUNT_DELAY, 1000);

const deferred = createLazyResource("open");
const unbind = deferred.$store.listen(() => {});
assert.equal(deferred.counts.mounted, 1);
unbind();
assert.equal(deferred.$store.lc, 0);
assert.equal(deferred.counts.unmounted, 0);

await sleep(STORE_UNMOUNT_DELAY + 200);
assert.equal(deferred.counts.unmounted, 1);

// The same delay is what makes the get()-mounts-the-store behaviour leak: once
// the window expires the store really does unmount, so the NEXT bare read runs
// the initializer all over again. A store that is only ever read with .get()
// therefore re-runs its mount side effect about once a second for as long as
// anything keeps reading it.
assert.equal(resource.counts.unmounted, 1);
resource.$store.get();
assert.equal(resource.counts.mounted, 2);

// ---------------------------------------------------------------------------
// batch() collapses calls and drops the changed key
// ---------------------------------------------------------------------------

// Inside batch() a listener is invoked ONCE no matter how many writes landed,
// and the changed-key argument is dropped to undefined because a single call
// cannot name two keys. Code that switches on the third argument silently
// stops running the moment its writes are wrapped in a batch.
const $batched = map({ p: 1, q: 1 });
const batchCalls = recordArgs($batched);
batch(() => {
  $batched.setKey("p", 2);
  $batched.setKey("q", 2);
});
assert.equal(batchCalls.calls.length, 1);
const [batchValue, batchOld, batchKey] = batchCalls.calls[0];
assert.deepEqual(batchValue, { p: 2, q: 2 });
assert.deepEqual(batchOld, { p: 1, q: 1 });
assert.equal(batchKey, undefined);
batchCalls.unbind();

// Unbatched, the same two writes deliver two calls, each naming its key.
const unbatchedCalls = recordArgs($batched);
$batched.setKey("p", 3);
$batched.setKey("q", 3);
assert.equal(unbatchedCalls.calls.length, 2);
assert.deepEqual(
  unbatchedCalls.calls.map(call => call[2]),
  ["p", "q"],
);
unbatchedCalls.unbind();

// listenKeys survives the batch only because it diffs the old and new objects
// whenever the key argument is missing, rather than trusting it. Both modes
// are measured here, because the claim is that it stays correct in both: the
// batched half proves the diff fallback, the unbatched half proves the plain
// Set-membership path.
const $watched = map({ a: 1, b: 1 });
const watchHits = [];
const unwatch = listenKeys($watched, ["a"], value => watchHits.push(value.a));
batch(() => {
  $watched.setKey("b", 9);
});
assert.equal(watchHits.length, 0);
batch(() => {
  $watched.setKey("a", 7);
});
assert.deepEqual(watchHits, [7]);

// Unbatched, the changed key is present and membership decides instead.
$watched.setKey("b", 8);
assert.deepEqual(watchHits, [7]);
$watched.setKey("a", 6);
assert.deepEqual(watchHits, [7, 6]);
unwatch();

// ---------------------------------------------------------------------------
// The recompute cache is a single global epoch counter
// ---------------------------------------------------------------------------

// The callback does not run at construction, and repeated reads do not re-run
// it. The first guard is a single epoch counter parked on globalThis — put
// there deliberately so that separately bundled copies of nanostores share one
// counter — and every notify() of every store in the process bumps it.
const $dep = atom(1);
const $unrelated = atom("x");
const counted = createCounted($dep, n => n * 2);
assert.equal(counted.counts.calls, 0);

assert.equal(counted.$derived.get(), 2);
assert.equal(counted.counts.calls, 1);

counted.$derived.get();
counted.$derived.get();
assert.equal(counted.counts.calls, 1);

// An unrelated store, which this computed has never referenced, still moves
// the shared counter. Measured rather than asserted from the source: the
// epoch really does advance on a store the computed knows nothing about.
const epochBefore = globalThis.nanostoresGlobal.epoch;
assert.equal(typeof epochBefore, "number");
$unrelated.set("y");
assert.equal(globalThis.nanostoresGlobal.epoch, epochBefore + 1);

// So the epoch guard is stale and the recompute path is entered again — yet
// the callback still does not run, because the comparison that follows is
// per-store and this store's own dependency has not moved.
assert.equal(counted.$derived.get(), 2);
assert.equal(counted.counts.calls, 1);

$dep.set(4);
assert.equal(counted.$derived.get(), 8);
assert.equal(counted.counts.calls, 2);

// ---------------------------------------------------------------------------
// An async callback in computed() stores the Promise itself
// ---------------------------------------------------------------------------

// computed() detects an async callback by looking for a `.t` marker that only
// task() attaches — not by checking for a thenable. A plain async arrow
// therefore falls through to the ordinary path and the STORE VALUE BECOMES THE
// PROMISE OBJECT, with no warning and no throw. Downstream code reads a
// Promise where it expected a number.
const $asyncSource = atom(3);
const asyncProbe = captureConsole(() => {
  const $asyncDerived = computed($asyncSource, async n => n * 10);
  return $asyncDerived.get();
});
assert.ok(asyncProbe.result instanceof Promise);
assert.equal(typeof asyncProbe.result.then, "function");
assert.equal(await asyncProbe.result, 30);
assert.deepEqual(asyncProbe.captured, []);

// The positive control that pins the gate down. Identical shape, the only
// difference being that task() stamps `.t` on the promise it returns. Now the
// async path IS taken: a deprecation warning appears where the plain arrow
// produced silence, and the store holds undefined rather than the Promise.
const $taskSource = atom(4);
const taskProbe = captureConsole(() => {
  const $taskDerived = computed($taskSource, n => task(async () => n * 10));
  return { first: $taskDerived.get(), $store: $taskDerived };
});
assert.deepEqual(taskProbe.captured, [
  "Nano Stores: Use @nanostores/async for async computed. " +
    "We will remove Promise support in computed() in Nano Stores 2.0",
]);
assert.equal(taskProbe.result.first, undefined);

// And awaiting allTasks() is not enough to read the result. allTasks resolves
// as soon as the task's own promise settles, which is strictly earlier than
// the .then() computed chained onto that same promise to store the value, so
// the store is still undefined on the line after the await.
await allTasks();
assert.equal(taskProbe.result.$store.get(), undefined);
await sleep(5);
assert.equal(taskProbe.result.$store.get(), 40);

// ---------------------------------------------------------------------------
// Dev-only helpers
// ---------------------------------------------------------------------------

// keepMount pins a store by attaching a listener and THROWING AWAY the
// unsubscribe function, so it returns undefined and there is no matching
// dropMount. Once pinned, a store stays mounted for the life of the process.
const core = await import("nanostores");
const pinned = createLazyResource("pinned");
assert.equal(keepMount(pinned.$store), undefined);
assert.equal(pinned.counts.mounted, 1);
assert.equal(pinned.$store.lc, 1);
assert.equal(core.dropMount, undefined);

// cleanStores is the documented escape hatch, and it is compiled out of
// production: it throws rather than degrading to a no-op, so a teardown helper
// that survives into a production bundle takes the process down.
const savedEnv = process.env.NODE_ENV;
try {
  process.env.NODE_ENV = "production";
  assert.throws(
    () => cleanStores(pinned.$store),
    err => {
      assert.equal(
        err.message,
        "cleanStores() can be used only during development or tests",
      );
      return true;
    },
  );
} finally {
  if (savedEnv === undefined) delete process.env.NODE_ENV;
  else process.env.NODE_ENV = savedEnv;
}

// Outside production the same call unmounts the pinned store synchronously,
// with no 1000 ms delay — cleanStores is the one path that skips it.
cleanStores(pinned.$store);
assert.equal(pinned.$store.lc, 0);
assert.equal(pinned.counts.unmounted, 1);

// deepMap still exports from the core package, so importing it looks current,
// but constructing one announces that it has moved to @nanostores/deepmap and
// will be removed in 2.0. The notice is a collapsed console group, not a
// throw, so nothing fails and nothing blocks.
const deepProbe = captureConsole(() => deepMap({ nested: { n: 1 } }));
assert.equal(deepProbe.captured.length, 1);
assert.equal(
  deepProbe.captured[0],
  "Nano Stores: Move to deepmap() from @nanostores/deepmap. " +
    "deepmap() will be removed in 2.0.",
);
// It works regardless, and setKey takes a dotted PATH, not a plain key: no
// literal "nested.n" key is created, the dot descends into the object.
deepProbe.result.setKey("nested.n", 2);
assert.deepEqual(deepProbe.result.get(), { nested: { n: 2 } });
assert.equal("nested.n" in deepProbe.result.get(), false);

// Building a second deepMap says nothing at all. warn() keys a module-level
// map by the exact message text, so a deprecation fires at most once per
// process — miss the first construction and the notice is gone for good.
const deepAgain = captureConsole(() => deepMap({ nested: { n: 9 } }));
assert.deepEqual(deepAgain.captured, []);

// The core package has no create(), no createStore() and no useStore(): the
// names carried over from the zustand/jotai/redux family are simply absent,
// and useStore lives in the separate @nanostores/react package.
for (const missing of ["create", "createStore", "useStore", "useState"]) {
  assert.equal(core[missing], undefined);
}
assert.equal(typeof core.atom, "function");
assert.equal(typeof core.map, "function");
assert.equal(typeof core.computed, "function");

console.log("nanostores lazy-mount contract: all assertions passed");
