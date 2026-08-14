import 'dart:collection';

import 'package:collection/collection.dart';
import 'package:csx_collection_equality/equality.dart';
import 'package:test/test.dart';

void main() {
  test('same contents, different objects: == on SDK collections is identity',
      () {
    final left = [1, 2];
    final right = [1, 2];

    expect(identical(left, right), isFalse);
    expect(left == right, isFalse);
    expect({'a': 1} == {'a': 1}, isFalse);
    expect({1, 2} == {1, 2}, isFalse);
    // Copying is not a rescue: every constructor produces a new object.
    expect(left == [...left], isFalse);
    expect(left == List<int>.of(left), isFalse);

    // And this is why the bug survives review: package:test's equals matcher
    // does its own deep comparison, so the assertion passes on exactly the
    // values the production `==` calls unequal.
    expect(left, equals(right));
    expect({'a': 1}, equals({'a': 1}));
    // The matcher recurses, so even the nested shape that defeats the one-level
    // Equality objects below is green here.
    expect([
      [1],
      [2]
    ], equals([
      [1],
      [2]
    ]));
    expect(left, isNot(same(right)));
  });

  test('const collections are canonicalized, which is what makes it confusing',
      () {
    // Same literal as above, opposite answer. A const collection is one shared
    // instance for the whole program, so identity happens to be right.
    expect(identical(const [1, 2], const [1, 2]), isTrue);
    expect(const [1, 2] == const [1, 2], isTrue);
    expect(const {'a': 1} == const {'a': 1}, isTrue);
    expect(const {1, 2} == const {1, 2}, isTrue);

    // Canonicalization is per type argument: these two hold the same numbers
    // and are still two objects.
    expect(identical(const <int>[1, 2], const <num>[1, 2]), isFalse);
    expect(const <int>[1, 2] == const <num>[1, 2], isFalse);

    // It also does not survive being copied, so the moment a const list is
    // spread into a builder the equality quietly reverts.
    expect(const [1, 2] == [...const [1, 2]], isFalse);
  });

  test('the Equality objects compare contents, exactly one level deep', () {
    const listEq = ListEquality<int>();
    expect(listEq.equals([1, 2], [1, 2]), isTrue);
    expect(listEq.hash([1, 2]), equals(listEq.hash([1, 2])));
    // A List is ordered, so this is an inequality and not a set comparison.
    expect(listEq.equals([1, 2], [2, 1]), isFalse);
    expect(listEq.equals([1, 2], [1, 2, 3]), isFalse);

    // A Map is not ordered for equality purposes: same pairs, written in the
    // other order, is equal.
    const mapEq = MapEquality<String, int>();
    expect(mapEq.equals({'a': 1, 'b': 2}, {'b': 2, 'a': 1}), isTrue);
    expect(mapEq.equals({'a': 1}, {'a': 1, 'b': 2}), isFalse);

    // Set equality has the same identity default as List (`{1, 2} == {1, 2}`
    // is false above) and SetEquality is the fix, order-insensitive by nature.
    const setEq = SetEquality<int>();
    expect(setEq.equals({1, 2}, {2, 1}), isTrue);
    expect(setEq.hash({1, 2}), equals(setEq.hash({2, 1})));
    expect(setEq.equals({1, 2}, {1, 2, 3}), isFalse);

    // IterableEquality compares in order without caring what the container is,
    // which is the one to reach for when you hold an Iterable you did not build.
    expect(const IterableEquality<int>().equals([1, 2], {1, 2}), isTrue);
  });

  test('one level is not enough: nested elements are compared with ==', () {
    const listOfLists = ListEquality<List<int>>();
    // The outer List is compared by content, the inner ones by `==`, which is
    // identity again. This is the failure that sends people to the docs.
    expect(listOfLists.equals([
      [1],
      [2]
    ], [
      [1],
      [2]
    ]), isFalse);

    // Make the inner lists const and the identical call answers true, because
    // the canonical instances really are identical. Same equality object, same
    // shape, different answer, decided by a keyword on the elements.
    expect(listOfLists.equals([const [1], const [2]], [const [1], const [2]]),
        isTrue);

    // Map values go the same way.
    expect(
        const MapEquality<String, List<int>>().equals({
          'a': [1]
        }, {
          'a': [1]
        }),
        isFalse);
  });

  test('DeepCollectionEquality recurses, and unordered() extends it to Lists',
      () {
    const deep = DeepCollectionEquality();
    final left = {
      'ids': [1, 2],
      'meta': {
        'tags': {'a', 'b'},
        'nested': [
          [3]
        ],
      },
    };
    final right = {
      'meta': {
        'nested': [
          [3]
        ],
        'tags': {'b', 'a'},
      },
      'ids': [1, 2],
    };

    expect(left == right, isFalse);
    expect(deep.equals(left, right), isTrue);

    // The plain DeepCollectionEquality is already order-insensitive for Sets
    // and Map keys, because it dispatches per collection type on the way down.
    // Only Lists stay ordered, which is the distinction the name hides.
    expect(deep.equals({1, 2}, {2, 1}), isTrue);
    expect(deep.equals([1, 2], [2, 1]), isFalse);
    expect(const DeepCollectionEquality.unordered().equals([1, 2], [2, 1]),
        isTrue);
    // Unordered still counts, so it is a multiset and not a set.
    expect(const DeepCollectionEquality.unordered().equals([1, 2], [1, 2, 2]),
        isFalse);

    // Non-collections fall through to the base equality, so it is safe to hand
    // it a value of unknown shape.
    expect(deep.equals('ab', 'ab'), isTrue);
    expect(deep.equals(1, 2), isFalse);
  });

  test('a Map used as a key needs both equals and hash, or it never matches',
      () {
    final query = {'status': 'open', 'limit': 10};
    final rebuilt = {'status': 'open', 'limit': 10};

    final plain = IdentityQueryCache()..store(query, ['a-1', 'a-2']);
    // The instance you stored under works, which is how this ships: the first
    // test written is usually the one that reuses the variable.
    expect(plain.lookup(query), equals(['a-1', 'a-2']));
    // An equal query built anywhere else misses.
    expect(plain.lookup(rebuilt), isNull);
    // So the cache is not a cache. It is a leak with a lookup method: every
    // call adds an entry and none of them are ever found again.
    plain.store(rebuilt, ['a-1', 'a-2']);
    expect(plain.size, equals(2));

    final deep = DeepQueryCache()..store(query, ['a-1', 'a-2']);
    expect(deep.lookup(rebuilt), equals(['a-1', 'a-2']));
    // Writing under an equal key replaces rather than adds.
    deep.store(rebuilt, ['a-1']);
    expect(deep.size, equals(1));
    expect(deep.lookup(query), equals(['a-1']));
    // Key order in the literal is irrelevant, because Map equality is not
    // ordered — the cache does not care how the caller spelled the query.
    expect(deep.lookup({'limit': 10, 'status': 'open'}), equals(['a-1']));
    // A key holding a nested collection still matches, which is the part that
    // needs the deep equality rather than MapEquality.
    final nested = DeepQueryCache()
      ..store({
        'ids': [1, 2]
      }, [
        'a-1'
      ]);
    expect(
        nested.lookup({
          'ids': [1, 2]
        }),
        equals(['a-1']));
  });

  test('the hash is the half people forget', () {
    const deep = DeepCollectionEquality();
    final left = {
      'ids': [1, 2]
    };
    final right = {
      'ids': [1, 2]
    };

    expect(identical(left, right), isFalse);
    expect(deep.equals(left, right), isTrue);
    // Equal structures hash the same, which is the contract a hash table needs
    // and the thing Object.hashCode cannot give you: it is an identity hash, so
    // these two are filed under different hashes and `equals` is never consulted.
    expect(deep.hash(left), equals(deep.hash(right)));

    // Which makes the half-fix worse than no fix: a HashMap given `equals` and
    // not `hashCode` still misses, so the equality you supplied is dead code.
    final half = EqualsOnlyQueryCache()
      ..store({'status': 'open'}, ['a-1']);
    expect(half.lookup({'status': 'open'}), isNull);
    final both = DeepQueryCache()..store({'status': 'open'}, ['a-1']);
    expect(both.lookup({'status': 'open'}), equals(['a-1']));

    // And it is not collision luck. Over 2000 freshly built pairs nothing hits,
    // and the supplied `equals` is not called once: the table compares the hash
    // each key was filed under before it consults equality, and an identity hash
    // never repeats. So the tempting story — that an equal-but-distinct key gets
    // found whenever the two keys happen to share a bucket — is wrong; the
    // equality never gets a vote.
    var equalsCalls = 0;
    var hits = 0;
    for (var i = 0; i < 2000; i++) {
      final table = HashMap<Query, String>(equals: (a, b) {
        equalsCalls++;
        return deep.equals(a, b);
      });
      table[{'status': 'open', 'limit': i}] = 'row';
      if (table[{'status': 'open', 'limit': i}] != null) hits++;
    }
    expect(hits, isZero);
    expect(equalsCalls, isZero);

    // The hash tracks its equality. Order changes the ordered hash and not the
    // unordered one, so the pairs stay consistent whichever you pick.
    expect(deep.hash([1, 2]), isNot(equals(deep.hash([2, 1]))));
    expect(const DeepCollectionEquality.unordered().hash([1, 2]),
        equals(const DeepCollectionEquality.unordered().hash([2, 1])));
  });

  test('groupBy, firstWhereOrNull and whereNotNull', () {
    const words = ['ada', 'grace', 'alan', 'edsger', 'ken'];

    expect(
        wordsByLength(words),
        equals({
          3: ['ada', 'ken'],
          5: ['grace'],
          4: ['alan'],
          6: ['edsger'],
        }));
    // The keys come out in first-occurrence order, not sorted, and the values
    // keep the input order — worth pinning, because it is what makes groupBy
    // usable for rendering without a second sort.
    expect(wordsByLength(words).keys.toList(), equals([3, 5, 4, 6]));

    expect(firstStartingWith(words, 'e'), equals('edsger'));
    expect(firstStartingWith(words, 'z'), isNull);
    // The SDK method it replaces throws instead of returning null.
    expect(() => words.firstWhere((word) => word.startsWith('z')),
        throwsStateError);

    const scores = <int?>[10, null, 7, null, 3];
    expect(presentScores(scores), equals([10, 7, 3]));
    // The point of it is the type: Iterable<int?> in, List<int> out, no `!`.
    expect(presentScores(scores), isA<List<int>>());
    // Measured: whereNotNull is deprecated in collection 1.19.1 and the SDK's
    // nonNulls is the replacement. Same elements, same order, no dependency.
    expect(presentScores(scores), equals(presentScoresFromSdk(scores)));
  });

  test('records have value equality; a record holding a List does not', () {
    // The built-in fix when you control the shape. No package, no Equality
    // object, and it works as a Map key because the hashCode is structural too.
    expect((1, 2) == (1, 2), isTrue);
    expect((id: 'a-1', qty: 2) == (id: 'a-1', qty: 2), isTrue);
    expect({(1, 2): 'hit'}[(1, 2)], equals('hit'));

    // Only as deep as its fields: the record compares each field with `==`, so
    // a List field is identity again and the whole record is unequal.
    expect(([1, 2],) == ([1, 2],), isFalse);
    final shared = [1, 2];
    expect((shared,) == (shared,), isTrue);
  });
}
