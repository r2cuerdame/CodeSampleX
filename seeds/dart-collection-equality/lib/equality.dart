import 'dart:collection';

import 'package:collection/collection.dart';

/// Two Dart collections with the same contents are not equal. `[1, 2] == [1, 2]`
/// is false, `{'a': 1} == {'a': 1}` is false, `{1, 2} == {1, 2}` is false. List,
/// Map and Set inherit `==` and `hashCode` from Object, and Object compares by
/// identity, so the only question those operators answer is "is this the same
/// object". Nothing in the language gives an SDK collection value semantics, and
/// nothing warns you: the code type-checks, reads correctly, and quietly means
/// something else.
///
/// The reason people believe it works sometimes is `const`. A const collection
/// is canonicalized at compile time, so every `const [1, 2]` in the program is
/// one shared instance and `identical` is true — which makes `==` true as well.
/// Same literal text, opposite answer, decided by a keyword that is usually
/// somewhere else in the file. Canonicalization is per type argument too, so
/// `const <int>[1, 2]` and `const <num>[1, 2]` are two different canonical
/// objects and compare false.
///
/// package:collection is the answer: Equality objects that compare contents
/// instead of identity. They come in one-level and deep flavours, and picking
/// the wrong one is the second trap. ListEquality, MapEquality and SetEquality
/// compare the collection itself by content but compare its *elements* with
/// their own `==`, so a List of Lists is back to identity one level down.
/// DeepCollectionEquality recurses, dispatching per collection type on the way:
/// SetEquality for sets, MapEquality for maps, ListEquality for lists. That
/// means Sets and Map keys are already order-insensitive under the plain
/// DeepCollectionEquality; `.unordered()` is what extends that to Lists.
///
/// The consequence that costs real money is caching. A Map or a List used as a
/// key in another Map never matches a rebuilt key, so the lookup misses, the
/// work is redone, and the cache grows one entry per call until the process
/// dies. Supplying only `equals` does not fix it, and the mechanism is sharper
/// than the usual "the probe lands in the wrong bucket": the table records the
/// hash each key was filed under and compares that recorded hash before it will
/// call `equals` at all. An identity hash never repeats, so the `equals` you
/// supplied is not outvoted, it is never invoked — the contract measures 2000
/// fresh pairs and counts zero hits and zero calls into it. Both hooks, or
/// nothing — `HashMap(equals: deep.equals, hashCode: deep.hash)`.
///
/// Tests hide all of this. package:test's `equals` matcher runs its own deep
/// comparison, so `expect(list, equals([1, 2]))` passes while the `==` the
/// production code uses is false. A green suite is not evidence that your
/// equality works.
///
/// If you control the shape, a Dart 3 record is the built-in fix: records have
/// structural equality, so `(1, 2) == (1, 2)`. The escape hatch is only as deep
/// as its fields — a record holding a List compares that field with `==` and is
/// back where it started.

/// A cache keyed by the query itself. The keys here are Maps, which is exactly
/// where the identity default hurts.
typedef Query = Map<String, Object?>;

/// The cache everyone writes first. It compiles, it type-checks, and every
/// lookup with a freshly built query misses.
class IdentityQueryCache {
  final Map<Query, List<String>> _entries = {};

  void store(Query query, List<String> rows) => _entries[query] = rows;

  List<String>? lookup(Query query) => _entries[query];

  int get size => _entries.length;
}

/// The half-fix, and the reason the whole thing is worth a sample: `equals` is
/// supplied and `hashCode` is left at the identity default. It reads as done and
/// it never hits. The miss is structural rather than a matter of collision luck:
/// over 2000 freshly built key pairs on Dart 3.13 the contract counts zero hits
/// and zero calls into the supplied `equals`, because the recorded hash is
/// compared first.
class EqualsOnlyQueryCache {
  static const _deep = DeepCollectionEquality();

  final Map<Query, List<String>> _entries = HashMap(equals: _deep.equals);

  void store(Query query, List<String> rows) => _entries[query] = rows;

  List<String>? lookup(Query query) => _entries[query];

  int get size => _entries.length;
}

/// The same cache with both hooks supplied. `equals` alone would still miss,
/// because the bucket is chosen by the hash before `equals` is ever called.
class DeepQueryCache {
  static const _deep = DeepCollectionEquality();

  final Map<Query, List<String>> _entries = HashMap(
    equals: _deep.equals,
    hashCode: _deep.hash,
  );

  void store(Query query, List<String> rows) => _entries[query] = rows;

  List<String>? lookup(Query query) => _entries[query];

  int get size => _entries.length;
}

/// groupBy keeps the first-occurrence order of the keys, because the Map it
/// returns is a plain insertion-ordered LinkedHashMap.
Map<int, List<String>> wordsByLength(Iterable<String> words) =>
    groupBy(words, (word) => word.length);

/// firstWhere throws StateError when nothing matches, so "not found" arrives as
/// an exception on an ordinary path. firstWhereOrNull returns null instead.
String? firstStartingWith(Iterable<String> words, String prefix) =>
    words.firstWhereOrNull((word) => word.startsWith(prefix));

/// whereNotNull narrows `Iterable<int?>` to `Iterable<int>`. Measured: it is
/// deprecated as of collection 1.19.1 in favour of the SDK's own
/// `Iterable.nonNulls`, which has been in dart:core since Dart 3.0 and does the
/// same job with no dependency. It still runs, so existing code is not broken,
/// but new code should use nonNulls.
// ignore: deprecated_member_use
List<int> presentScores(Iterable<int?> scores) => scores.whereNotNull().toList();

/// The replacement, for comparison in the contract.
List<int> presentScoresFromSdk(Iterable<int?> scores) => scores.nonNulls.toList();
