import 'package:equatable/equatable.dart';

/// Dart classes compare by identity. `Money(500, 'USD') == Money(500, 'USD')`
/// is false, because `Object.==` asks "is this the same object" and nothing in
/// the language upgrades that answer for you. seeds/dart-collection-equality
/// raises the same problem from the collection side; this is the object side,
/// and it is worse, because a List at least has a matcher in package:test that
/// papers over it. A value object has nothing: the wrong answer flows straight
/// into `contains`, `indexOf`, Set membership, Map keys, and every `if (a == b)`
/// guard in the codebase.
///
/// The usual fix is code generation — freezed, built_value, dart_mappable — and
/// all three need build_runner, a `part` directive, and a generated file that
/// must be regenerated and committed. equatable is the version that needs none
/// of it: you extend (or, since 2.1.0, mix in) `Equatable` and declare a `props`
/// list. `==` becomes an element-wise comparison of `props` plus a runtimeType
/// check, and `hashCode` is derived from the same list, so the two can never
/// drift apart the way a hand-written pair does.
///
/// What equatable buys you is exactly `==`, `hashCode` and `toString`. It does
/// not generate `copyWith`. That is the honest difference from freezed and the
/// reason this seed writes `copyWith` by hand: the equality half is free, the
/// construction half is not, and the hand-written half has its own trap
/// (see [Customer]).
///
/// The failure mode that makes this worth a sample is that `props` is an
/// ordinary list you maintain by hand. Add a field to the class, forget to add
/// it to `props`, and nothing breaks loudly: the class still compiles, `==`
/// still returns a bool, the tests that compare whole objects still pass — and
/// two genuinely different objects are now equal, collapse into one entry in a
/// Set, and overwrite each other as Map keys. There is no analyzer warning for
/// it. [LooseOrder] is that bug, held still.
///
/// The second trap is that `props` holding a collection is compared *deeply*.
/// equatable's `objectsEquals` dispatches on the runtime type: Sets go through
/// `setEquals` (order-insensitive), Maps through `mapEquals`, other Iterables
/// through `iterableEquals` (order-sensitive), nested Equatables through their
/// own `==`, and two `num`s through `==` even across int/double. That is more
/// than `==` on a List gives you and less than you get if you never put the
/// list in `props` at all — so the entire behaviour hinges on one line you have
/// to remember to edit.
///
/// The third trap is that `final` on a List field freezes the reference, not
/// the contents. equatable's hash is computed from the list's contents, so
/// mutating the list in place after the object has been filed in a Set moves
/// its hash out from under the table and the object becomes unfindable while
/// still sitting inside it — and the Set will then happily accept a freshly
/// built object that is `==` to the one already in there, so a Set ends up
/// holding two equal elements.
///
/// Measured on Dart 3.13.0, and the reverse of what this seed first asserted:
/// copying that Set does not repair it. `{...aSet}`, `Set.of(aSet)` and
/// `LinkedHashSet()..addAll(aSet)` all carry the stale entry across, and
/// `Map.of(aMap)` and `{...aMap}` do the same for keys. Routing the elements
/// through a plain Iterable first — `Set.of(aSet.toList())`, `Set.of(aSet
/// .where(...))`, or a `for` loop calling `add` — does rehash and does repair
/// it. Same source, same elements, opposite result, decided by whether the
/// argument is still a Set. So "just copy the set" is not the fix; not mutating
/// a field that is in `props` is.

/// The baseline, with no help: two of these with identical fields are two
/// unrelated objects as far as `==` is concerned.
class PlainMoney {
  const PlainMoney(this.amountMinor, this.currency);

  final int amountMinor;
  final String currency;
}

/// The same class as a value object. `props` lists both fields, so both
/// participate in `==` and in `hashCode`.
class Money extends Equatable {
  const Money(this.amountMinor, this.currency);

  final int amountMinor;
  final String currency;

  /// equatable does not generate this. Written by hand, it is unremarkable
  /// here because neither field is nullable — `??` is enough. [Customer] is
  /// where that stops being true.
  Money copyWith({int? amountMinor, String? currency}) => Money(
        amountMinor ?? this.amountMinor,
        currency ?? this.currency,
      );

  @override
  List<Object?> get props => [amountMinor, currency];
}

/// A subclass that adds no state and reuses the inherited `props`. It is still
/// never equal to its parent, because `Equatable.==` compares `runtimeType`
/// before it looks at `props`. Worth knowing before you reach for subclassing
/// to model a variant, and worth knowing before you write a sealed hierarchy
/// where one case wraps another.
class TaxedMoney extends Money {
  const TaxedMoney(super.amountMinor, super.currency);
}

/// The correct version: the nested List is in `props`, so equatable compares it
/// element by element and hashes it by content.
class Order extends Equatable {
  const Order(this.id, this.lineItems);

  final String id;
  final List<String> lineItems;

  Order copyWith({String? id, List<String>? lineItems}) =>
      Order(id ?? this.id, lineItems ?? this.lineItems);

  @override
  List<Object?> get props => [id, lineItems];
}

/// The bug. Identical fields to [Order], one line different: `lineItems` never
/// made it into `props`. Every order with the same id is now the same order, in
/// `==`, in `hashCode`, in a Set, and as a Map key. Nothing warns.
class LooseOrder extends Equatable {
  const LooseOrder(this.id, this.lineItems);

  final String id;
  final List<String> lineItems;

  @override
  List<Object?> get props => [id];
}

/// The sentinel type for [Customer.copyWithClearing]. It is a private class so
/// that no caller outside this library can construct one and collide with it.
/// `const Object()` looks like it would do, and does not: const instances are
/// canonicalized, so every `const Object()` in the program is the *same*
/// object, and a caller passing one would be read as "argument omitted".
class _Unset {
  const _Unset();
}

const _Unset _unset = _Unset();

/// A value object with a nullable field, which is where a hand-written
/// `copyWith` goes wrong. This is the piece freezed generates and equatable
/// does not, so it is the piece you own.
class Customer extends Equatable {
  const Customer({required this.id, this.nickname});

  final String id;
  final String? nickname;

  /// The `??` version everyone writes first. It cannot distinguish "the caller
  /// omitted nickname" from "the caller passed null on purpose", because both
  /// arrive as `null`. So this method can set a nickname and change a nickname
  /// and can never clear one: `copyWith(nickname: null)` returns an object
  /// equal to the original, and the call reads as if it worked.
  Customer copyWith({String? id, String? nickname}) => Customer(
        id: id ?? this.id,
        nickname: nickname ?? this.nickname,
      );

  /// The fix, without code generation: default the parameters to a private
  /// sentinel and test with `identical`, so an explicit `null` is a value like
  /// any other. freezed generates this shape; here you write it.
  Customer copyWithClearing({
    Object? id = _unset,
    Object? nickname = _unset,
  }) =>
      Customer(
        id: identical(id, _unset) ? this.id : id! as String,
        nickname:
            identical(nickname, _unset) ? this.nickname : nickname as String?,
      );

  @override
  List<Object?> get props => [id, nickname];
}

/// Collections other than List in `props`. equatable picks the comparison from
/// the runtime type, so the Set field is order-insensitive and the Map field is
/// key-order-insensitive without any extra declaration.
class Tagged extends Equatable {
  const Tagged(this.name, this.tags, this.meta);

  final String name;
  final Set<String> tags;
  final Map<String, int> meta;

  @override
  List<Object?> get props => [name, tags, meta];
}

/// Composition: a value object holding another value object. equatable checks
/// for a nested Equatable before it checks for a collection, so the inner
/// object's own `==` is used and the nesting composes to any depth.
class LineItem extends Equatable {
  const LineItem(this.sku, this.price);

  final String sku;
  final Money price;

  @override
  List<Object?> get props => [sku, price];
}

/// A `num` field. equatable special-cases num-to-num comparison (added in
/// 2.0.7) so that `1` and `1.0` in `props` compare the way Dart's own `==`
/// compares them, rather than being split apart by the runtimeType fallback
/// that catches every other pair of unrelated types.
class Quantity extends Equatable {
  const Quantity(this.value);

  final num value;

  @override
  List<Object?> get props => [value];
}

/// A hierarchy that already uses `extends`, which is the case `EquatableMixin`
/// existed for.
abstract class Failure {
  const Failure();

  String get code;
}

/// The 2.1.0 spelling. `Equatable` is declared `abstract mixin class`, so it
/// can be mixed into a class that already extends something. This is the
/// replacement for `EquatableMixin`, which 2.1.0 deprecates.
class ApiFailure extends Failure with Equatable {
  const ApiFailure(this.code, this.status);

  @override
  final String code;

  final int status;

  @override
  List<Object?> get props => [code, status];

  /// Opt this one class into printing its props, so the contract can assert
  /// `toString` without depending on whether asserts are enabled.
  @override
  bool? get stringify => true;
}

/// The pre-2.1.0 spelling, kept here because it is what every tutorial written
/// before mid-2026 shows. It still compiles and still gives value equality, but
/// it is deprecated, and it is not a subtype of `Equatable` — so a codebase
/// migrating class by class cannot use `is Equatable` as the test for "does
/// this have value equality yet".
// ignore: deprecated_member_use
class LegacyFailure extends Failure with EquatableMixin {
  LegacyFailure(this.code, this.status);

  @override
  final String code;

  final int status;

  @override
  List<Object?> get props => [code, status];
}
