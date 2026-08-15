import 'dart:async';

import 'package:pool/pool.dart';

/// package:pool is the answer to "run at most N of these at once" in Dart, and
/// it is almost never what people expect, because the name promises a generic
/// worker pool and the four traps below are all in the semantics rather than in
/// the signatures. The signatures are guessable: `Pool(int, {Duration? timeout})`,
/// `Future<T> withResource<T>(FutureOr<T> Function())`,
/// `Future<PoolResource> request()`, `void release()`. Everything that bites is
/// underneath them.
///
/// Trap 1 — `timeout` is not a deadline. It reads like "no caller waits more
/// than this", and it is a whole-pool deadlock alarm instead. The timer is reset
/// by *any* activity anywhere in the pool: every request that queues, every
/// release, every element `forEach` picks up. So one starved caller can sit
/// behind a busy pool for hours and never time out, as long as its neighbours
/// keep churning. When it finally does fire it fires against *every* pending
/// request at once, not just the oldest, with a TimeoutException whose message
/// says "Pool deadlock". And then it is over: the implementation drops the timer
/// (`_timer = null`) after firing, so a pool that has timed out once will never
/// time out again for the rest of its life. A timeout that silently stops
/// protecting you after its first trip is worse than no timeout, so treat it as
/// what it is documented to be — a deadlock detector — and put real per-call
/// deadlines on the work itself with `Future.timeout`.
///
/// Trap 2 — `forEach` does not preserve order. It returns `Stream<T>`, and
/// N workers pull from one shared iterator and emit each result the moment it is
/// ready, so the stream is in completion order. `pool.forEach(items, f).toList()`
/// therefore lines up with `items` only when every call happens to finish in the
/// order it started, which in tests with a warm cache it usually does. Index into
/// that list by the position of the input and it is wrong in production only.
/// [mapPooledInOrder] is the fix, and it is short.
///
/// Trap 3 — `forEach` does not stop on an error, and `toList()` throws away the
/// results that already arrived. The default `onError` returns true, which means
/// "add this error to the stream"; iteration carries straight on and the
/// remaining elements are still processed. Since a Stream ends at its first
/// error, `.toList()` completes with that error and discards every value emitted
/// before it — while the pool keeps running the rest of the work in the
/// background. Returning false from `onError` does not stop anything either; it
/// only drops the error silently. Nothing here behaves like `Future.wait`.
///
/// Trap 4 — closing reports two different ways. `request()` is a plain method
/// and throws StateError *synchronously* on a closed pool, so the exception
/// escapes the call itself. `withResource()` is `async` and throws the same
/// StateError *into its returned future*. The same mistake is a synchronous
/// crash through one door and an unhandled async error through the other, which
/// is how "we handle a closed pool" passes review while one of the two paths
/// never runs its catch.
///
/// Trap 5 — `PoolResource.allowRelease` is not a softer `release`. It spends the
/// token there and then, so the `release()` that looks like tidy-up afterwards
/// throws StateError; the callback it takes runs later, when someone else needs
/// the slot. And if that callback throws — it is a teardown, so it is the code
/// most likely to throw — pool 1.5.2 completes the *next queued request* with
/// that error, handing an exception to a caller that has nothing to do with the
/// broken resource, and the slot is never given back. A pool of one becomes a
/// pool of zero and every later request waits forever with nothing logged. Read
/// this behaviour from the version you depend on rather than from the
/// repository: dart-lang/tools has since rewritten `_runOnRelease` to swallow
/// the error instead, which is the opposite outcome from the same call.
///
/// One more thing that costs an afternoon: `forEach` takes every resource in the
/// pool while its stream is being consumed. Sharing a pool between `forEach` and
/// anything else means the other caller is blocked for the whole run, not
/// throttled alongside it.

/// Runs [action] over [items] with at most `pool` calls in flight, and returns
/// the results in the order of [items].
///
/// This is what most people mean when they reach for `pool.forEach`. Future.wait
/// keeps position, and `withResource` is what does the throttling: every call is
/// created up front, but each one waits inside the pool for a free slot, so the
/// concurrency limit holds even though the futures all exist at once.
Future<List<T>> mapPooledInOrder<S, T>(
  Pool pool,
  Iterable<S> items,
  FutureOr<T> Function(S item) action,
) =>
    Future.wait(items.map((item) => pool.withResource(() => action(item))));

/// The version that looks equivalent and is not: `forEach` emits in completion
/// order, so the returned list is a permutation of the results, not a parallel
/// array to [items].
Future<List<T>> mapPooledWithForEach<S, T>(
  Pool pool,
  Iterable<S> items,
  FutureOr<T> Function(S item) action,
) =>
    pool.forEach(items, action).toList();

/// Records how many calls were genuinely in flight at once, so the contract can
/// show the limit is real rather than assuming it.
class ConcurrencyTracker {
  int _inFlight = 0;

  /// The high-water mark of concurrent calls to [track].
  int maxInFlight = 0;

  /// How many times [track] was entered.
  int started = 0;

  Future<T> track<T>(FutureOr<T> Function() action) async {
    started++;
    _inFlight++;
    if (_inFlight > maxInFlight) maxInFlight = _inFlight;
    try {
      return await action();
    } finally {
      _inFlight--;
    }
  }
}

/// The outcome of draining a `pool.forEach` stream without letting the first
/// error end it, which is the only way to see what actually happened.
class ForEachOutcome<T> {
  final List<T> values;
  final List<Object> errors;

  ForEachOutcome(this.values, this.errors);
}

/// Drains [stream] to completion, keeping values and errors side by side.
///
/// `toList()` cannot do this: a Stream terminates at its first error, so the
/// values that arrived before it are lost even though the pool went on to
/// produce the rest.
Future<ForEachOutcome<T>> drain<T>(Stream<T> stream) async {
  final values = <T>[];
  final errors = <Object>[];
  final done = Completer<void>();
  stream.listen(
    values.add,
    onError: (Object error) => errors.add(error),
    onDone: done.complete,
    cancelOnError: false,
  );
  await done.future;
  return ForEachOutcome(values, errors);
}
