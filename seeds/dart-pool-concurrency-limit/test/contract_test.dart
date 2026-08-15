import 'dart:async';

import 'package:csx_pool_concurrency_limit/throttle.dart';
import 'package:fake_async/fake_async.dart';
import 'package:pool/pool.dart';
import 'package:test/test.dart';

/// Every action here is driven by a Completer rather than by a delay, so the
/// interleavings below are facts about pool and not about how loaded the
/// machine is.
void main() {
  test('the limit is real, and withResource gives the slot back on a throw',
      () async {
    final pool = Pool(2);
    final tracker = ConcurrencyTracker();

    Future<int> boom() async => throw StateError('body failed');
    await expectLater(pool.withResource(boom), throwsStateError);

    // If the failing body had leaked its slot, the pool would be down to one
    // and the high-water mark below would read 1. withResource releases in a
    // finally, so the error costs nothing.
    await mapPooledInOrder(pool, [1, 2, 3, 4, 5, 6],
        (i) => tracker.track(() async {
              await Future<void>.delayed(Duration.zero);
              return i * 2;
            }));

    expect(tracker.started, 6);
    expect(tracker.maxInFlight, 2);
  });

  test('forEach emits in completion order; Future.wait is what keeps position',
      () async {
    final keys = ['a', 'b', 'c', 'd'];
    final forEachGates = {for (final k in keys) k: Completer<String>()};
    final waitGates = {for (final k in keys) k: Completer<String>()};

    // Four workers, four elements, so all four are in flight at once and the
    // only thing that decides the output order is who finishes first.
    final viaForEach = mapPooledWithForEach(
        Pool(4), keys, (String k) => forEachGates[k]!.future);
    final viaWait =
        mapPooledInOrder(Pool(4), keys, (String k) => waitGates[k]!.future);
    await pumpEventQueue();

    for (final k in ['d', 'c', 'b', 'a']) {
      forEachGates[k]!.complete(k.toUpperCase());
      waitGates[k]!.complete(k.toUpperCase());
      await pumpEventQueue();
    }

    // This is the bug: the obvious call reads like a parallel array to `keys`
    // and is a permutation of it. Indexing viaForEach[0] to get the result for
    // keys[0] is wrong here, and is right in any test where the work happens to
    // finish in the order it started.
    expect(await viaForEach, ['D', 'C', 'B', 'A']);
    expect((await viaForEach).toSet(), {'A', 'B', 'C', 'D'});
    expect(await viaWait, ['A', 'B', 'C', 'D']);
  });

  test('forEach holds every resource in the pool while its stream is live',
      () async {
    final pool = Pool(2);
    final gates = [Completer<int>(), Completer<int>()];
    final drained = drain(pool.forEach([0, 1], (int i) => gates[i].future));
    await pumpEventQueue();

    // An unrelated caller on the same pool is not throttled alongside forEach,
    // it is blocked until forEach is finished with the whole pool.
    var granted = false;
    unawaited(pool.request().then((r) {
      granted = true;
      r.release();
    }));
    await pumpEventQueue();
    expect(granted, isFalse);

    gates[0].complete(10);
    gates[1].complete(11);
    await drained;
    await pumpEventQueue();
    expect(granted, isTrue);
  });

  test('an error in forEach does not stop the run, and onError(false) hides it',
      () async {
    final seen = <int>[];
    Future<int> action(int i) async {
      seen.add(i);
      if (i == 3) throw StateError('item 3 failed');
      return i * 10;
    }

    // Default onError returns true: the error is added to the stream and the
    // remaining elements are still processed. Nothing about this resembles
    // Future.wait, which abandons the rest at the first error.
    final outcome = await drain(Pool(2).forEach([1, 2, 3, 4, 5, 6], action));
    expect(seen..sort(), [1, 2, 3, 4, 5, 6]);
    expect(outcome.values..sort(), [10, 20, 40, 50, 60]);
    expect(outcome.errors.single, isA<StateError>());

    // onError returning false does not stop anything either. It only drops the
    // error, so the stream completes clean and five of six results is a
    // success as far as the caller can tell.
    seen.clear();
    final quiet = await drain(Pool(2)
        .forEach([1, 2, 3, 4, 5, 6], action, onError: (i, e, s) => false));
    expect(quiet.errors, isEmpty);
    expect(quiet.values..sort(), [10, 20, 40, 50, 60]);
    expect(seen, hasLength(6));
  });

  test('toList throws away the results that already arrived and cancels the rest',
      () async {
    final seen = <int>[];
    Future<int> action(int i) async {
      seen.add(i);
      if (i == 3) throw StateError('item 3 failed');
      return i * 10;
    }

    // A pool of one processes the elements strictly in order, so 10 and 20 are
    // on the stream before item 3 throws. toList listens with cancelOnError,
    // so the caller gets the error and none of the values, and the cancel stops
    // the iteration dead: items 4, 5 and 6 are never attempted. Same stream,
    // same pool, and the opposite outcome from the drain above.
    await expectLater(
        Pool(1).forEach([1, 2, 3, 4, 5, 6], action).toList(), throwsStateError);
    await pumpEventQueue();
    expect(seen, [1, 2, 3]);
  });

  test('errors thrown by the iterable itself never reach onError', () async {
    Iterable<int> elements() sync* {
      yield 1;
      throw StateError('iteration failed');
    }

    // onError says drop everything, and this error lands on the stream anyway,
    // because onError is only consulted for errors thrown by the action.
    final outcome = await drain(Pool(2).forEach(elements(), (int i) async => i,
        onError: (i, e, s) => false));
    expect(outcome.errors.single, isA<StateError>());
  });

  test('timeout is a pool-wide inactivity alarm that fires exactly once', () {
    fakeAsync((async) {
      final pool = Pool(1, timeout: const Duration(seconds: 1));
      PoolResource? held;
      pool.request().then((r) => held = r);
      async.flushMicrotasks();
      expect(held, isNotNull);

      final outcomes = <String, Object?>{};
      void watch(String name) {
        pool.request().then((r) {
          outcomes[name] = r;
        }).catchError((Object error) {
          outcomes[name] = error;
        });
      }

      // 'a' is starved from here on and never gets a resource. Each new request
      // from someone else calls _resetTimer, which restarts the countdown, so
      // 'a' waits nearly three times the timeout without tripping it. A caller
      // that reads `timeout:` as "no one waits longer than a second" is wrong
      // by an unbounded amount: any activity at all keeps the alarm asleep.
      watch('a');
      async.elapse(const Duration(milliseconds: 900));
      expect(outcomes, isEmpty);
      watch('b');
      async.elapse(const Duration(milliseconds: 900));
      watch('c');
      async.elapse(const Duration(milliseconds: 900));
      async.flushMicrotasks();
      expect(outcomes, isEmpty);

      // Quiet at last. It fires against every pending request at once, not just
      // the one that has waited longest.
      async.elapse(const Duration(seconds: 1));
      async.flushMicrotasks();
      expect(outcomes.keys, unorderedEquals(['a', 'b', 'c']));
      final failure = outcomes['a'] as TimeoutException;
      expect(failure.duration, const Duration(seconds: 1));
      expect(failure.message, contains('deadlock'));
      expect(outcomes['b'], isA<TimeoutException>());
      expect(outcomes['c'], isA<TimeoutException>());

      // And now the guard is gone for the life of the pool: _onTimeout drops
      // the timer instead of rearming it, so this second deadlock is silent no
      // matter how long it lasts.
      outcomes.clear();
      watch('d');
      async.elapse(const Duration(hours: 1));
      async.flushMicrotasks();
      expect(outcomes, isEmpty);

      // The pool is otherwise healthy; the slot is handed over as usual.
      held!.release();
      async.flushMicrotasks();
      expect(outcomes['d'], isA<PoolResource>());
    });
  });

  test('a closed pool reports through two different doors', () async {
    final pool = Pool(2);
    await pool.close();
    expect(pool.isClosed, isTrue);

    // request() is an ordinary method, so this throws out of the call itself.
    expect(() => pool.request(), throwsStateError);

    // withResource() is `async`, so the identical StateError goes into the
    // returned future instead. The try/catch that protects the request() call
    // catches nothing here, and an unawaited call becomes an unhandled async
    // error somewhere else entirely.
    Object? caughtSynchronously;
    Future<int>? pending;
    try {
      pending = pool.withResource(() => 1);
    } catch (error) {
      caughtSynchronously = error;
    }
    expect(caughtSynchronously, isNull);
    await expectLater(pending, throwsStateError);

    // close() is memoized, so it is safe to call from every teardown path.
    expect(identical(pool.close(), pool.close()), isTrue);
  });

  test('done waits for the resources that were already handed out', () async {
    final pool = Pool(2);
    final held = await pool.request();
    var finished = false;
    unawaited(pool.done.then((_) => finished = true));

    final closing = pool.close();
    await pumpEventQueue();
    expect(finished, isFalse);

    held.release();
    await closing;
    await pumpEventQueue();
    expect(finished, isTrue);
  });

  test('allowRelease spends the token, and a failing callback loses the slot',
      () async {
    final pool = Pool(1);
    final resource = await pool.request();
    var ran = false;
    resource.allowRelease(() {
      ran = true;
    });

    // Nothing has happened yet: the callback runs only when the slot is wanted.
    expect(ran, isFalse);
    // The token is spent all the same, so the tidy-looking release() that
    // follows an allowRelease is a StateError, not a no-op.
    expect(() => resource.release(), throwsStateError);

    final next = await pool.request();
    expect(ran, isTrue);
    next.release();

    // Now the part that costs a production incident. The onRelease callback is
    // a teardown, so it is exactly the kind of code that fails, and pool 1.5.2
    // delivers its error to whoever is next in the request queue: a caller with
    // no connection to the resource whose teardown broke, holding an exception
    // from a stack it never entered. (dart-lang/tools on GitHub has since
    // rewritten this to swallow the error instead. The published 1.5.2 does
    // not, so read the version you actually depend on.)
    final second = await pool.request();
    second.allowRelease(() {
      throw StateError('teardown failed');
    });
    await expectLater(pool.request(), throwsStateError);

    // And the slot is gone for the life of the pool. allowRelease already spent
    // the token, the failed callback was taken off the queue, and nothing ever
    // decremented the allocation count, so this one-resource pool now has zero
    // usable resources and every later request waits forever. Without a
    // `timeout:` on the pool nothing will ever report it.
    var latecomer = false;
    unawaited(pool.request().then((_) {
      latecomer = true;
    }));
    await pumpEventQueue();
    expect(latecomer, isFalse);
  });

  test('requests are answered in request order, whoever frees the slot',
      () async {
    final pool = Pool(2);
    final first = await pool.request();
    final second = await pool.request();
    final firstGate = Completer<void>();
    final secondGate = Completer<void>();
    first.allowRelease(() => firstGate.future);
    second.allowRelease(() => secondGate.future);

    final order = <String>[];
    for (final name in ['x', 'y']) {
      unawaited(pool.request().then((r) {
        order.add(name);
        r.release();
      }));
    }
    await pumpEventQueue();
    expect(order, isEmpty);

    // The second onRelease callback finishes first, and the resource it freed
    // still goes to 'x', because pool completes those futures in the order the
    // requests were made. Waiting on the callback you triggered is the wrong
    // model of this.
    secondGate.complete();
    await pumpEventQueue();
    expect(order, ['x']);

    firstGate.complete();
    await pumpEventQueue();
    expect(order, ['x', 'y']);
  });

  test('the pool refuses a nonsense limit and serves waiters first in, first out',
      () async {
    expect(() => Pool(0), throwsArgumentError);
    expect(() => Pool(-1), throwsArgumentError);

    final pool = Pool(1);
    final held = await pool.request();
    final order = <int>[];
    for (var i = 0; i < 3; i++) {
      unawaited(pool.request().then((r) {
        order.add(i);
        r.release();
      }));
    }
    await pumpEventQueue();
    expect(order, isEmpty);

    held.release();
    await pumpEventQueue();
    expect(order, [0, 1, 2]);
  });
}
