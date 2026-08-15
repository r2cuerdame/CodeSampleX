import 'dart:collection';

import 'package:csx_freezed_style_equality/value.dart';
import 'package:equatable/equatable.dart';
import 'package:test/test.dart';

void main() {
  test('value equality replaces identity, and hashCode is derived from props',
      () {
    // The baseline: same two fields, no Equatable, and `==` answers the only
    // question Object knows how to answer.
    expect(PlainMoney(500, 'USD') == PlainMoney(500, 'USD'), isFalse);

    final a = Money(500, 'USD');
    final b = Money(500, 'USD');
    expect(identical(a, b), isFalse);
    expect(a == b, isTrue);
    expect(a.hashCode, equals(b.hashCode));
    expect(a == a, isTrue);

    // Both fields are in props, so a difference in either is a difference.
    expect(a == Money(501, 'USD'), isFalse);
    expect(a == Money(500, 'EUR'), isFalse);
    expect(a.hashCode == Money(501, 'USD').hashCode, isFalse);
    expect(a.hashCode == Money(500, 'EUR').hashCode, isFalse);

    // hashCode comes from the same props list as `==`, which is the point:
    // hand-written pairs drift when a field is added to one and not the other.
    expect(a.props, equals(<Object?>[500, 'USD']));

    // The one case where the unhelped class looks right, and the reason people
    // think identity is sometimes value equality: const canonicalizes, so both
    // literals are the same object. Nothing about PlainMoney changed.
    expect(identical(const PlainMoney(500, 'USD'), const PlainMoney(500, 'USD')),
        isTrue);
    expect(const PlainMoney(500, 'USD') == const PlainMoney(500, 'USD'), isTrue);
  });

  test('copyWith is yours to write; `??` cannot express "set this to null"', () {
    final original = Money(500, 'USD');

    // equatable generates no copyWith. Written by hand, an empty call must
    // still produce a new object that is equal to the old one.
    final copy = original.copyWith();
    expect(identical(original, copy), isFalse);
    expect(copy == original, isTrue);
    expect(copy.hashCode, equals(original.hashCode));

    final changed = original.copyWith(amountMinor: 750);
    expect(changed == original, isFalse);
    expect(changed.amountMinor, equals(750));
    expect(changed.currency, equals('USD'));
    expect(original.amountMinor, equals(500));

    // Now the nullable field, which is where the hand-written version breaks.
    // `nickname ?? this.nickname` reads an omitted argument and an explicit
    // null identically, so the clear is silently a no-op.
    final named = Customer(id: 'c1', nickname: 'Ada');
    final notCleared = named.copyWith(nickname: null);
    expect(notCleared.nickname, equals('Ada'));
    expect(notCleared == named, isTrue);
    expect(identical(notCleared, named), isFalse);

    // A sentinel default plus `identical` distinguishes the two cases. This is
    // the shape freezed generates for you.
    final cleared = named.copyWithClearing(nickname: null);
    expect(cleared.nickname, isNull);
    expect(cleared == named, isFalse);
    expect(named.copyWithClearing() == named, isTrue);
    expect(named.copyWithClearing(nickname: 'Grace').nickname, equals('Grace'));
    expect(named.copyWithClearing(id: 'c2').nickname, equals('Ada'));
  });

  test('a field left out of props is silently dropped from equality', () {
    // Both fields declared, both fields in props: the orders differ.
    final full = Order('o1', ['sku-a', 'sku-b']);
    final fullOther = Order('o1', ['sku-a', 'sku-c']);
    expect(full == fullOther, isFalse);

    // The same class with one line removed from props. Nothing else differs,
    // nothing warns, and two different orders are now the same order.
    final loose = LooseOrder('o1', ['sku-a', 'sku-b']);
    final looseOther = LooseOrder('o1', ['sku-a', 'sku-c']);
    expect(loose == looseOther, isTrue);
    expect(loose.hashCode, equals(looseOther.hashCode));

    // The fields really are different; only equality has stopped looking.
    expect(loose.lineItems, isNot(equals(looseOther.lineItems)));

    // Which is how the data disappears. A Set keeps the first and drops the
    // second, and a Map keeps the first key with the second value.
    final deduped = {loose, looseOther};
    expect(deduped.length, equals(1));
    expect(identical(deduped.single, loose), isTrue);
    expect(deduped.single.lineItems, equals(['sku-a', 'sku-b']));

    final byOrder = {loose: 'first', looseOther: 'second'};
    expect(byOrder.length, equals(1));
    expect(byOrder[loose], equals('second'));
    expect(byOrder[looseOther], equals('second'));
    expect(identical(byOrder.keys.single, loose), isTrue);
  });

  test('collections in props are compared deeply, by their own kind', () {
    // Dart itself does not do this: two Lists with the same contents are not
    // ==. Everything below is equatable's dispatch, not the language's.
    expect(['sku-a'] == ['sku-a'], isFalse);

    // A List prop is compared element-wise and in order.
    expect(Order('o1', ['a', 'b']) == Order('o1', ['a', 'b']), isTrue);
    expect(Order('o1', ['a', 'b']).hashCode,
        equals(Order('o1', ['a', 'b']).hashCode));
    expect(Order('o1', ['a', 'b']) == Order('o1', ['b', 'a']), isFalse);
    expect(Order('o1', ['a', 'b']).hashCode == Order('o1', ['b', 'a']).hashCode,
        isFalse);
    expect(Order('o1', ['a']) == Order('o1', ['a', 'b']), isFalse);
    expect(Order('o1', []) == Order('o1', []), isTrue);

    // A Set prop and a Map prop are not order-sensitive, because equatable
    // dispatches on the runtime type of each prop rather than treating
    // everything as an ordered Iterable.
    final t1 = Tagged('x', {'red', 'blue'}, {'a': 1, 'b': 2});
    final t2 = Tagged('x', {'blue', 'red'}, {'b': 2, 'a': 1});
    expect(t1 == t2, isTrue);
    expect(t1.hashCode, equals(t2.hashCode));
    expect(t1 == Tagged('x', {'red'}, {'a': 1, 'b': 2}), isFalse);
    expect(t1 == Tagged('x', {'red', 'blue'}, {'a': 1, 'b': 3}), isFalse);
    expect(t1 == Tagged('x', {'red', 'green'}, {'a': 1, 'b': 2}), isFalse);

    // A nested Equatable is checked before the collection branches, so value
    // objects compose to any depth.
    expect(LineItem('s', Money(500, 'USD')) == LineItem('s', Money(500, 'USD')),
        isTrue);
    expect(LineItem('s', Money(500, 'USD')) == LineItem('s', Money(501, 'USD')),
        isFalse);
    expect(LineItem('s', Money(500, 'USD')).hashCode,
        equals(LineItem('s', Money(500, 'USD')).hashCode));

    // Two nums are compared as nums, so int and double meet the way Dart's own
    // `==` makes them meet rather than being split by the runtimeType fallback.
    expect(1 == 1.0, isTrue);
    expect(Quantity(1) == Quantity(1.0), isTrue);
    expect(Quantity(1).hashCode, equals(Quantity(1.0).hashCode));
    expect(Quantity(1) == Quantity(2), isFalse);
  });

  test('Sets and Map keys work by value, until a props field mutates in place',
      () {
    // This is what the equality was for: a rebuilt key finds the stored entry.
    final prices = {Money(500, 'USD'), Money(500, 'USD'), Money(750, 'USD')};
    expect(prices.length, equals(2));
    expect(prices.contains(Money(500, 'USD')), isTrue);

    final cache = <Money, String>{Money(500, 'USD'): 'five'};
    expect(cache[Money(500, 'USD')], equals('five'));
    expect(cache.containsKey(Money(500, 'USD')), isTrue);
    expect(cache[Money(750, 'USD')], isNull);

    // `final List<String> lineItems` freezes the reference, not the contents.
    // equatable hashes the contents, so mutating the list moves the object's
    // hash after the Set has already filed it under the old one.
    final order = Order('o1', ['sku-a']);
    final orders = {order};
    expect(orders.contains(order), isTrue);

    final hashBefore = order.hashCode;
    order.lineItems.add('sku-b');
    expect(order.hashCode == hashBefore, isFalse);

    // The object is in the Set and the Set cannot find it, and cannot remove
    // it. Not a collision: the lookup hashes to a bucket it is not filed in.
    expect(orders.contains(order), isFalse);
    expect(orders.length, equals(1));
    expect(identical(orders.first, order), isTrue);
    expect(orders.remove(order), isFalse);
    expect(orders.length, equals(1));

    // Worse than a missed lookup: nothing can dedupe against an entry that
    // cannot be found, so the Set accepts an equal object and ends up holding
    // two elements that are == to each other with the same hashCode.
    final duplicate = Order('o1', ['sku-a', 'sku-b']);
    expect(duplicate == order, isTrue);
    expect(duplicate.hashCode, equals(order.hashCode));
    orders.add(duplicate);
    expect(orders.length, equals(2));
    expect(orders.first == orders.last, isTrue);
    expect(orders.first.hashCode, equals(orders.last.hashCode));

    // A Map key goes the same way: the entry is still there and unreachable.
    final keyed = Order('o2', ['a']);
    final byOrder = <Order, String>{keyed: 'stored'};
    expect(byOrder[keyed], equals('stored'));
    keyed.lineItems.add('b');
    expect(byOrder[keyed], isNull);
    expect(byOrder.containsKey(keyed), isFalse);
    expect(byOrder.length, equals(1));
    expect(identical(byOrder.keys.single, keyed), isTrue);
  });

  test('copying that Set does not rehash it, so the copy is broken too', () {
    final order = Order('o1', ['sku-a']);
    final orders = {order};
    order.lineItems.add('sku-b');
    expect(orders.contains(order), isFalse);

    // MEASURED on Dart 3.13.0, and the reverse of the obvious repair: a spread
    // literal, Set.of() and addAll() given another Set all carry the stale
    // entry across unchanged, so the copy is exactly as broken as the original.
    expect(<Order>{...orders}.contains(order), isFalse);
    expect(Set<Order>.of(orders).contains(order), isFalse);
    expect((LinkedHashSet<Order>()..addAll(orders)).contains(order), isFalse);

    // Putting an ordinary Iterable in between does rehash, because then the
    // elements go in one at a time. Same source, same elements, opposite
    // answer, decided by whether the argument is still a Set.
    final healed = Set<Order>.of(orders.toList());
    expect(healed.contains(order), isTrue);
    expect(Set<Order>.of(orders.where((_) => true)).contains(order), isTrue);
    final looped = <Order>{};
    for (final o in orders) {
      looped.add(o);
    }
    expect(looped.contains(order), isTrue);

    // The healed copy is the control: it refuses the duplicate the stale one
    // accepts, on the same value.
    orders.add(Order('o1', ['sku-a', 'sku-b']));
    expect(orders.length, equals(2));
    healed.add(Order('o1', ['sku-a', 'sku-b']));
    expect(healed.length, equals(1));

    // Maps copy the same damage, and a fresh equal key becomes a second entry.
    final keyed = Order('o2', ['a']);
    final byOrder = <Order, String>{keyed: 'stored'};
    keyed.lineItems.add('b');
    expect(Map<Order, String>.of(byOrder)[keyed], isNull);
    expect(<Order, String>{...byOrder}[keyed], isNull);
    final mapCopy = Map<Order, String>.of(byOrder)
      ..[Order('o2', ['a', 'b'])] = 'again';
    expect(mapCopy.length, equals(2));
  });

  test('runtimeType is compared before props, so a subclass never equals its '
      'parent', () {
    final base = Money(500, 'USD');
    final taxed = TaxedMoney(500, 'USD');

    // Same props, inherited verbatim.
    expect(taxed.props, equals(base.props));
    expect(taxed is Money, isTrue);

    // And still not equal, in either direction.
    expect(base == taxed, isFalse);
    expect(taxed == base, isFalse);
    expect(base.hashCode == taxed.hashCode, isFalse);
    expect({base, taxed}.length, equals(2));

    // Two of the subclass are equal to each other, so the class is not broken.
    expect(TaxedMoney(500, 'USD') == taxed, isTrue);

    // The hand-written copyWith compounds it. Inherited verbatim, its body
    // still constructs a Money, so copying a TaxedMoney silently downcasts and
    // the copy is not equal to what it copied. freezed generates a copyWith
    // per class; a hand-written one has to be overridden per class too.
    final copied = taxed.copyWith();
    expect(copied.runtimeType, equals(Money));
    expect(copied == taxed, isFalse);
  });

  test('toString prints props under asserts and hides them without', () {
    // Reading the getter latches the value, so save it before touching it.
    final ambient = EquatableConfig.stringify;
    addTearDown(() => EquatableConfig.stringify = ambient);

    // Measured under `dart test`, which runs with asserts enabled: the default
    // is true. A release build flips it, so the same log line that showed the
    // values in development prints the bare type name in production.
    expect(ambient, isTrue);
    expect(Money(500, 'USD').toString(), equals('Money(500, USD)'));

    EquatableConfig.stringify = false;
    expect(Money(500, 'USD').toString(), equals('Money'));
    expect(Order('o1', ['a']).toString(), equals('Order'));

    // A per-instance `stringify` overrides the global either way, which is how
    // you keep one type readable in release.
    expect(ApiFailure('E_TIMEOUT', 504).toString(),
        equals('ApiFailure(E_TIMEOUT, 504)'));

    EquatableConfig.stringify = true;
    expect(Order('o1', ['a', 'b']).toString(), equals('Order(o1, [a, b])'));
  });

  test('Equatable is a mixin class in 2.1.0 and EquatableMixin is deprecated',
      () {
    // `with Equatable` is the 2.1.0 spelling, and it is what lets a class that
    // already extends something get value equality without code generation.
    final a = ApiFailure('E_TIMEOUT', 504);
    final b = ApiFailure('E_TIMEOUT', 504);
    expect(identical(a, b), isFalse);
    expect(a == b, isTrue);
    expect(a.hashCode, equals(b.hashCode));
    expect(a == ApiFailure('E_TIMEOUT', 503), isFalse);
    expect(a is Failure, isTrue);
    expect(a is Equatable, isTrue);

    // The pre-2.1.0 spelling still gives value equality.
    final legacy = LegacyFailure('E_TIMEOUT', 504);
    expect(legacy == LegacyFailure('E_TIMEOUT', 504), isTrue);
    expect(legacy.hashCode, equals(LegacyFailure('E_TIMEOUT', 504).hashCode));

    // But it is not a subtype of Equatable, so `is Equatable` is not a valid
    // "has this class been migrated yet" check during a partial migration.
    expect(legacy is Equatable, isFalse);
    // ignore: deprecated_member_use
    expect(legacy is EquatableMixin, isTrue);

    // Two classes are never equal to each other whatever they mix in.
    expect(a == legacy, isFalse);
    expect(legacy == a, isFalse);
  });
}
