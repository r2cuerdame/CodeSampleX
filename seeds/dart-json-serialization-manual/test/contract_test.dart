import 'dart:convert';

import 'package:csx_json_manual/product.dart';
import 'package:json_annotation/json_annotation.dart';
import 'package:test/test.dart';

void main() {
  test('jsonDecode never validates, so the failure lands at the use site', () {
    // Every single value in this payload has the wrong type for Product.
    const wrong = '{"sku":42,"quantity":"three","price":"free",'
        '"created_at":1747334852,"tags":"ink","note":7}';

    // Decoding succeeds anyway. jsonDecode's job ends at "is this well-formed
    // JSON"; its return type is dynamic and it checks nothing about the shape.
    final json = decodeObject(wrong);
    expect(json, isA<Map<String, dynamic>>());
    expect(json['sku'], equals(42));

    // Pulling the field out is still dynamic, so this does not fail either.
    final quantity = pluck(json, 'quantity');
    expect(quantity, equals('three'));

    // The first typed line is the first line that can fail — here an implicit
    // downcast on a function argument, in code that never mentions JSON. That
    // is why the stack trace points at business logic and not at the parser.
    expect(
        () => reorderLevel(pluck(json, 'quantity')), throwsA(isA<TypeError>()));

    // A constructor full of casts is the same thing, just closer to the decode.
    expect(() => Product.fromJsonNaive(json), throwsA(isA<TypeError>()));

    // There is exactly one hook that runs during the decode, and it is blunt.
    // Measured: reviver sees every pair bottom-up — the inner value before the
    // container holding it, list elements keyed by their int index, and the
    // whole document last under a null key — and each call gets that one key
    // and nothing else. No path, so it can normalise a type everywhere but
    // cannot say "this field, inside this object, must be an int".
    final visited = <Object?>[];
    jsonDecode('{"a":{"b":1},"c":[10,20]}', reviver: (k, v) {
      visited.add(k);
      return v;
    });
    expect(visited, equals(['b', 'a', 0, 1, 'c', null]));

    // Which makes it a type normaliser and not a schema. Useful for the number
    // problem below, useless for validating one field.
    expect(
      jsonDecode('{"q":3.0}', reviver: (k, v) => v is double ? v.toInt() : v),
      equals({'q': 3}),
    );
  });

  test('a decoded List is List<dynamic>, and .cast defers the check again', () {
    // Every element here is a String, and the list still is not a List<String>:
    // jsonDecode builds List<dynamic>, and a List<dynamic> is not a List<String>
    // no matter what is in it. `as List<String>` fails on perfectly good data.
    final good = decodeObject('{"tags":["ink","a4"]}')['tags'];
    expect(good, isA<List<dynamic>>());
    expect(good is List<String>, isFalse);
    expect(() => good as List<String>, throwsA(isA<TypeError>()));

    // .cast<String>() is the usual fix, and it is lazy: it returns a view that
    // checks each element as you read it. With a bad element inside, building
    // the view succeeds — and so does reading .length. The trap from the first
    // test, one level deeper, and the reason a bad list surfaces in a widget.
    final lazy =
        (decodeObject('{"tags":["ink",7]}')['tags'] as List).cast<String>();
    expect(lazy, isA<List<String>>());
    expect(lazy.length, equals(2));
    expect(lazy[0], equals('ink'));
    expect(() => lazy[1], throwsA(isA<TypeError>()));

    // List<String>.from copies now, so it fails now, at the decode.
    expect(
      () =>
          List<String>.from(decodeObject('{"tags":["ink",7]}')['tags'] as List),
      throwsA(isA<TypeError>()),
    );
  });

  test('a JSON number is int or double according to how it was spelled', () {
    // Nothing about the Dart type comes from your model. It comes from the
    // characters on the wire.
    expect(jsonDecode('{"q":3}')['q'], isA<int>());
    expect(jsonDecode('{"q":3.0}')['q'], isA<double>());
    expect(jsonDecode('{"q":3e0}')['q'], isA<double>());

    // So `as int` breaks on a whole number written with a decimal point, and
    // `as double` breaks on one written without. Both directions are live: an
    // encoder on the other side that trims a trailing zero flips the Dart type
    // of a field mid-stream, and nothing warns you.
    expect(() => jsonDecode('{"q":3.0}')['q'] as int, throwsA(isA<TypeError>()));
    expect(() => jsonDecode('{"q":3}')['q'] as double, throwsA(isA<TypeError>()));

    // num is the common supertype and the only cast that survives both.
    expect((jsonDecode('{"q":3.0}')['q'] as num).toInt(), equals(3));
    expect((jsonDecode('{"q":3}')['q'] as num).toDouble(), equals(3.0));

    // The price of that fix, stated plainly: toInt truncates and does not
    // throw, so a genuinely fractional value is silently floored.
    expect((jsonDecode('{"q":3.7}')['q'] as num).toInt(), equals(3));

    // The same rule has a second edge. An integer literal too large for Dart's
    // 64-bit int also decodes as a double, so a 19- or 20-digit id arrives with
    // its low digits already gone and no error anywhere.
    expect(jsonDecode('{"id":9223372036854775807}')['id'], isA<int>());
    expect(jsonDecode('{"id":9223372036854775808}')['id'], isA<double>());
    expect((jsonDecode('{"id":12345678901234567890}')['id'] as num).toString(),
        isNot(equals('12345678901234567890')));

    final asDouble = decodeObject(
        productJson.replaceFirst('"quantity":3', '"quantity":3.0'));
    expect(() => Product.fromJsonNaive(asDouble), throwsA(isA<TypeError>()));
    expect(Product.fromJsonNumSafe(asDouble).quantity, equals(3));
  });

  test('a missing key is null, and is indistinguishable from an explicit null',
      () {
    final json = decodeObject('{"sku":"CSX-1","note":null}');

    // Neither read throws. Map's [] returns null for anything it does not have.
    expect(json['note'], isNull);
    expect(json['nope'], isNull);
    // containsKey is the only thing that tells the two apart, and almost no
    // hand-written fromJson calls it.
    expect(json.containsKey('note'), isTrue);
    expect(json.containsKey('nope'), isFalse);

    // Into a non-nullable field, that null is a TypeError naming types and not
    // the key that was missing.
    final noQuantity =
        decodeObject(productJson.replaceFirst('"quantity":3,', ''));
    expect(() => Product.fromJsonNaive(noQuantity), throwsA(isA<TypeError>()));

    // Into a nullable field it is nothing at all, which is the quieter bug: a
    // misspelled optional key reads as "absent" forever and no layer notices.
    final typo = decodeObject(productJson.replaceFirst('"note"', '"notes"'));
    expect(Product.fromJsonNaive(typo).note, isNull);

    // $checkKeys is json_annotation's answer and it runs before any value is
    // read, so a missing key stays a missing key, with a name on it.
    expect(
      () => Product.fromJsonStrict(noQuantity),
      throwsA(isA<MissingRequiredKeysException>()
          .having((e) => e.missingKeys, 'missingKeys', equals(['quantity']))),
    );
    // It catches the same typo from the other side — the key nobody expected.
    expect(
      () => Product.fromJsonStrict(typo),
      throwsA(isA<UnrecognizedKeysException>().having(
          (e) => e.unrecognizedKeys, 'unrecognizedKeys', equals(['notes']))),
    );
    // And an explicit null where null is not allowed is its own third case,
    // which is the distinction the raw map cannot make at all.
    expect(
      () => Product.fromJsonStrict(decodeObject(
          productJson.replaceFirst('"sku":"CSX-1"', '"sku":null'))),
      throwsA(isA<DisallowedNullValueException>().having(
          (e) => e.keysWithNullValues, 'keysWithNullValues', equals(['sku']))),
    );

    // The good payload passes all three checks.
    expect(
        Product.fromJsonStrict(decodeObject(productJson)).sku, equals('CSX-1'));
  });

  test('DateTime is not a JSON type, and the two directions are not symmetric',
      () {
    // Decoding hands back the String. There is no format jsonDecode recognises
    // and no hook that would turn one into a DateTime.
    expect(decodeObject(productJson)['created_at'], isA<String>());
    expect(decodeObject(productJson)['created_at'], isNot(isA<DateTime>()));

    // Encoding one is an error, not a default format.
    expect(() => jsonEncode({'at': DateTime.utc(2026, 5, 15)}),
        throwsA(isA<JsonUnsupportedObjectError>()));

    // Both conversions are yours to write. JsonConverter is the shape
    // json_annotation gives them and it works standalone, no generator.
    const iso = IsoUtcDateTimeConverter();
    final decoded = iso.fromJson('2026-05-15T18:27:32.045Z');
    expect(decoded.isUtc, isTrue);
    expect(iso.toJson(decoded), equals('2026-05-15T18:27:32.045Z'));

    // Measured, against the expectation this seed started with: DateTime.parse
    // returns a UTC value for a numeric offset too, not just for a trailing Z.
    // Only a string with no zone designator at all comes back local. Reading is
    // not where the offset is lost.
    expect(DateTime.parse('2026-05-15T20:27:32.045+02:00').isUtc, isTrue);
    expect(iso.fromJson('2026-05-15T20:27:32.045+02:00'),
        equals(DateTime.utc(2026, 5, 15, 18, 27, 32, 45)));
    // Writing is. toIso8601String has exactly two forms — `Z` for a UTC value
    // and no designator for anything else — and no form that emits an offset,
    // so +02:00 is normalised away and can never round-trip as itself.
    expect(DateTime.parse('2026-05-15T20:27:32.045+02:00').toIso8601String(),
        equals('2026-05-15T18:27:32.045Z'));

    // Which makes this the actual trap: a DateTime that is not UTC — anything
    // from DateTime.now() or the DateTime(...) constructor — serializes to a
    // bare wall clock carrying no zone information whatsoever.
    const naive = NaiveDateTimeConverter();
    final local = DateTime(2026, 5, 15, 18, 27, 32, 45);
    expect(local.isUtc, isFalse);
    final ambiguous = naive.toJson(local);
    expect(ambiguous, equals('2026-05-15T18:27:32.045'));
    expect(ambiguous, allOf(isNot(contains('Z')), isNot(contains('+'))));
    expect(DateTime.parse(ambiguous).isUtc, isFalse);

    // The same characters denote a different instant per reader, which is the
    // bug that only shows up once a second machine is involved.
    expect(
      DateTime.parse('${ambiguous}Z')
          .difference(DateTime.parse('$ambiguous+02:00')),
      equals(const Duration(hours: 2)),
    );

    // toUtc() before writing is the fix, and it never emits the ambiguous form.
    expect(iso.toJson(local), endsWith('Z'));
    final back = DateTime.parse(iso.toJson(local));
    expect(back.isAtSameMomentAs(local), isTrue);
    // One more surprise about that round trip: DateTime's == compares the isUtc
    // flag as well as the instant, so the value that came back is not == the
    // local original even though it is the same moment. Comparing a decoded
    // timestamp against a locally built one with == fails for this reason
    // alone, which is why Product keeps every createdAt in UTC.
    expect(back.isUtc, isTrue);
    expect(back == local, isFalse);
  });

  test('jsonEncode finds toJson by name, not through an interface', () {
    final product = Product.fromJsonNumSafe(decodeObject(productJson));

    // Product implements nothing and extends nothing. A no-argument method
    // called toJson is the entire contract, resolved by dynamic dispatch at
    // encode time, which is why misspelling the method name still compiles.
    expect(
      jsonDecode(jsonEncode(product)),
      equals({
        'sku': 'CSX-1',
        'quantity': 3,
        'price': 19.5,
        'created_at': '2026-05-15T18:27:32.045Z',
        'tags': ['ink', 'a4'],
        'note': null,
      }),
    );
    // The recursion applies it per element, so collections need no help.
    expect((jsonDecode(jsonEncode([product, product])) as List).length,
        equals(2));

    // No toJson: a runtime error only. jsonEncode's parameter is Object?, so
    // the call site type-checks and nothing warns at build time. The cause is
    // a NoSuchMethodError, which is the duck typing showing through.
    expect(
      () => jsonEncode(Warehouse('W1')),
      throwsA(isA<JsonUnsupportedObjectError>()
          .having((e) => e.unsupportedObject, 'unsupportedObject',
              isA<Warehouse>())
          .having((e) => e.cause, 'cause', isA<NoSuchMethodError>())),
    );

    // Without a custom toJson anywhere in the path, an unencodable value is
    // reported as itself: here the DateTime, with the NoSuchMethodError from
    // the failed toJson lookup as its cause.
    expect(
      () => jsonEncode({'created_at': DateTime.utc(2026, 5, 15)}),
      throwsA(isA<JsonUnsupportedObjectError>()
          .having(
              (e) => e.unsupportedObject, 'unsupportedObject', isA<DateTime>())
          .having((e) => e.cause, 'cause', isA<NoSuchMethodError>())),
    );

    // Measured, against the expectation that the innermost value is what gets
    // reported: put a toJson in the path and it is no longer. LeakyProduct
    // returns a map holding that same DateTime, and the error re-attributes to
    // LeakyProduct — the nearest object whose toJson dart:convert called. The
    // DateTime is only reachable by unwrapping `cause`, which is itself another
    // JsonUnsupportedObjectError, and toString() never mentions it at all. Log
    // the message alone and you go hunting through the wrong class.
    expect(
      () => jsonEncode(LeakyProduct(DateTime.utc(2026, 5, 15))),
      throwsA(isA<JsonUnsupportedObjectError>()
          .having((e) => e.unsupportedObject, 'unsupportedObject',
              isA<LeakyProduct>())
          .having((e) => e.toString(), 'toString',
              allOf(contains('LeakyProduct'), isNot(contains('DateTime'))))
          .having((e) => e.cause, 'cause',
              isA<JsonUnsupportedObjectError>().having((c) => c.unsupportedObject,
                  'cause.unsupportedObject', isA<DateTime>()))
          // partialResult shows how far the encoder had streamed before it gave
          // up, and is carried unchanged up the chain.
          .having((e) => e.partialResult, 'partialResult',
              equals('{"created_at":'))),
    );

    // toEncodable is the escape hatch for types you do not own.
    expect(
        jsonEncode(Warehouse('W1'), toEncodable: (o) => (o as Warehouse).code),
        equals('"W1"'));
  });

  test('the checked helpers name the class and the key that failed', () {
    final badPrice = decodeObject(
        productJson.replaceFirst('"price":19.5', '"price":"free"'));

    // Hand-written casts give a TypeError that knows the types and not the
    // field, which in a fifteen-field model is a bisect.
    expect(() => Product.fromJsonNaive(badPrice), throwsA(isA<TypeError>()));

    // The same conversions wrapped in $checkedCreate/$checkedConvert — the
    // helpers json_serializable emits for `checked: true`, called by hand —
    // rethrow it as a CheckedFromJsonException carrying the JSON key, the class
    // name and the original error. Identical safety, usable message.
    expect(
      () => Product.fromJsonChecked(badPrice),
      throwsA(isA<CheckedFromJsonException>()
          .having((e) => e.key, 'key', equals('price'))
          .having((e) => e.className, 'className', equals('Product'))
          .having((e) => e.innerError, 'innerError', isA<TypeError>())
          .having((e) => e.toString(), 'toString',
              contains('There is a problem with "price"'))),
    );

    // It reports the JSON key, not the Dart field name, including where the two
    // differ — which is what fieldKeyMap is for.
    expect(
      () => Product.fromJsonChecked(decodeObject(productJson.replaceFirst(
          '"created_at":"2026-05-15T18:27:32.045Z"',
          '"created_at":1747334852'))),
      throwsA(isA<CheckedFromJsonException>()
          .having((e) => e.key, 'key', equals('created_at'))),
    );

    // A missing key arrives the same way, so one catch covers both.
    expect(
      () => Product.fromJsonChecked(
          decodeObject(productJson.replaceFirst('"quantity":3,', ''))),
      throwsA(isA<CheckedFromJsonException>()
          .having((e) => e.key, 'key', equals('quantity'))),
    );

    // What it does not buy: catching anything the naive version would not.
    // These helpers only wrap failures, so the good payload is untouched.
    final ok = Product.fromJsonChecked(decodeObject(productJson));
    expect(ok.sku, equals('CSX-1'));
    expect(ok.quantity, equals(3));
    expect(ok.createdAt, equals(DateTime.utc(2026, 5, 15, 18, 27, 32, 45)));
    expect(ok.tags, equals(['ink', 'a4']));
    expect(ok.note, isNull);
  });

  test('the annotations themselves do nothing at runtime', () {
    // json_annotation's annotations are const objects that only build_runner
    // reads. Adding the package and annotating a field changes no behaviour:
    // AnnotationsAreInert declares the wire key is `renamed` with a default of
    // `fallback`, and the hand-written code that actually runs uses `value`
    // and has no default.
    expect(AnnotationsAreInert('x').toJson(), equals({'value': 'x'}));
    expect(AnnotationsAreInert.fromJson({'value': 'x'}).value, equals('x'));
    expect(() => AnnotationsAreInert.fromJson({'renamed': 'x'}),
        throwsA(isA<TypeError>()));
    expect(() => AnnotationsAreInert.fromJson({}), throwsA(isA<TypeError>()));

    // The annotation is readable as an ordinary value, which is all it is.
    expect(const JsonKey(name: 'renamed').name, equals('renamed'));
    expect(const JsonKey(name: 'renamed', defaultValue: 'fallback').defaultValue,
        equals('fallback'));
  });
}
