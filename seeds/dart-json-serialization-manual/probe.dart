import 'dart:convert';

import 'package:csx_json_manual/product.dart';
import 'package:json_annotation/json_annotation.dart';

void main() {
  // 1. reviver: what does it actually see, and in what order?
  final seen = <String>[];
  final out = jsonDecode(
    '{"a":{"b":1},"c":[10,20],"d":"x"}',
    reviver: (k, v) {
      seen.add('key=${k.runtimeType}:$k value=${v.runtimeType}');
      return v;
    },
  );
  print('REVIVER SEEN:');
  for (final s in seen) {
    print('  $s');
  }
  print('REVIVER OUT: $out');

  // 2. fieldKeyMap: does the created_at assertion actually depend on it?
  try {
    $checkedCreate<Product>(
      'Product',
      decodeObject(productJson.replaceFirst(
          '"created_at":"2026-05-15T18:27:32.045Z"', '"created_at":1747334852')),
      (convert) => Product(
        sku: convert('sku', (v) => v as String),
        quantity: convert('quantity', (v) => (v as num).toInt()),
        price: convert('price', (v) => (v as num).toDouble()),
        createdAt: convert('created_at',
            (v) => const IsoUtcDateTimeConverter().fromJson(v as String)),
        tags: convert('tags', (v) => List<String>.from(v as List)),
        note: convert('note', (v) => v as String?),
      ),
      // NO fieldKeyMap on purpose.
    );
  } on CheckedFromJsonException catch (e) {
    print('NO-FIELDKEYMAP convert failure key=${e.key} class=${e.className}');
  }

  // ArgumentError escaping the constructor body, without fieldKeyMap.
  try {
    $checkedCreate<Product>(
      'Product',
      decodeObject(productJson),
      (convert) => throw ArgumentError.value(null, 'createdAt', 'too old'),
    );
  } on CheckedFromJsonException catch (e) {
    print('ARGERROR no-map key=${e.key} msg=${e.message} class=${e.className}');
  }

  // Same, with fieldKeyMap.
  try {
    $checkedCreate<Product>(
      'Product',
      decodeObject(productJson),
      (convert) => throw ArgumentError.value(null, 'createdAt', 'too old'),
      fieldKeyMap: const {'createdAt': 'created_at'},
    );
  } on CheckedFromJsonException catch (e) {
    print('ARGERROR with-map key=${e.key} msg=${e.message} '
        'class=${e.className} toString=${e.toString().replaceAll('\n', ' | ')}');
  }

  // 3. what does the bare TypeError from a hand-written cast actually say?
  try {
    Product.fromJsonNaive(
        decodeObject(productJson.replaceFirst('"price":19.5', '"price":"free"')));
  } catch (e) {
    print('BARE TYPEERROR: ${e.runtimeType}: $e');
  }

  // 4. the two JsonUnsupportedObjectError shapes, verbatim.
  try {
    jsonEncode({'created_at': DateTime.utc(2026, 5, 15)});
  } on JsonUnsupportedObjectError catch (e) {
    print('PLAIN DATETIME: obj=${e.unsupportedObject.runtimeType} '
        'cause=${e.cause.runtimeType} partial=${e.partialResult}');
    print('  toString=${e.toString().replaceAll('\n', ' | ')}');
  }
  try {
    jsonEncode(LeakyProduct(DateTime.utc(2026, 5, 15)));
  } on JsonUnsupportedObjectError catch (e) {
    print('LEAKY: obj=${e.unsupportedObject.runtimeType} '
        'cause=${e.cause.runtimeType} partial=${e.partialResult}');
    print('  toString=${e.toString().replaceAll('\n', ' | ')}');
    final cause = e.cause;
    if (cause is JsonUnsupportedObjectError) {
      print('  cause.obj=${cause.unsupportedObject.runtimeType} '
          'cause.cause=${cause.cause.runtimeType} '
          'cause.partial=${cause.partialResult}');
    }
  }
  try {
    jsonEncode([LeakyProduct(DateTime.utc(2026, 5, 15))]);
  } on JsonUnsupportedObjectError catch (e) {
    print('LEAKY IN LIST: obj=${e.unsupportedObject.runtimeType} '
        'cause=${e.cause.runtimeType} partial=${e.partialResult}');
  }

  // 5. timezone of the container, and the DateTime.parse zone forms.
  print('LOCAL TZ OFFSET: ${DateTime.now().timeZoneOffset} '
      'name=${DateTime.now().timeZoneName}');
  print('parse Z isUtc=${DateTime.parse('2026-05-15T18:27:32.045Z').isUtc}');
  print('parse +02:00 isUtc='
      '${DateTime.parse('2026-05-15T20:27:32.045+02:00').isUtc} '
      'iso=${DateTime.parse('2026-05-15T20:27:32.045+02:00').toIso8601String()}');
  print('parse bare isUtc=${DateTime.parse('2026-05-15T18:27:32.045').isUtc}');
  final local = DateTime(2026, 5, 15, 18, 27, 32, 45);
  final back = DateTime.parse(const IsoUtcDateTimeConverter().toJson(local));
  print('back==local: ${back == local} sameMoment=${back.isAtSameMomentAs(local)}');

  // 6. is a cast view really lazy about length, and what of .toList()?
  final lazy = (decodeObject('{"tags":["ink",7]}')['tags'] as List).cast<String>();
  print('lazy runtimeType=${lazy.runtimeType} length=${lazy.length}');
  try {
    lazy.toList();
  } catch (e) {
    print('lazy.toList threw ${e.runtimeType}');
  }

  // 7. big-int spelling
  print('9223372036854775807 -> ${jsonDecode('{"id":9223372036854775807}')['id'].runtimeType}');
  print('9223372036854775808 -> ${jsonDecode('{"id":9223372036854775808}')['id'].runtimeType}');
  print('12345678901234567890 -> ${jsonDecode('{"id":12345678901234567890}')['id']}');

  // 8. $checkKeys ordering: which exception wins when several apply at once?
  try {
    $checkKeys(
      decodeObject('{"note":null,"nope":1}'),
      allowedKeys: const ['sku', 'note'],
      requiredKeys: const ['sku'],
      disallowNullValues: const ['note'],
    );
  } catch (e) {
    print('CHECKKEYS combined -> ${e.runtimeType}');
  }
}
