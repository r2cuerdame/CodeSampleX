import 'dart:convert';

import 'package:json_annotation/json_annotation.dart';

/// Hand-written JSON serialization in Dart: dart:convert for the bytes,
/// json_annotation's runtime half for the parts dart:convert refuses to do.
/// No build_runner, no `part 'product.g.dart'`, no generated file.
///
/// The one idea everything below follows from: jsonDecode is typed `dynamic`.
/// It parses, it does not validate. Any well-formed JSON object decodes to a
/// Map<String, dynamic> whatever the values are, so every type error is
/// deferred to the moment some typed code touches the value — which may be a
/// constructor, or may be three call frames and one async gap later, in a stack
/// trace that does not mention the payload at all.
///
/// The five things that catch people, each of them silent at decode time:
///
/// 1. dynamic defers, it does not check. Reading a field out of the decoded map
///    succeeds for any value. `as int` is the first line that can fail, and if
///    you never write a cast — you assign to a dynamic local, or pass it along
///    — the failure happens at the eventual typed use. The list case is the
///    sharpest form: a decoded array is List<dynamic> and is not a List<String>
///    even when every element is a String, and the usual fix,
///    `(x as List).cast<String>()`, builds a lazy checking view, so it defers
///    one more time and blows up at the first bad element you read.
///
/// 2. A JSON number is int or double depending on how it was SPELLED. `3`
///    decodes to int, `3.0` and `3e0` decode to double, and `as int` on a
///    double throws — as does `as double` on an int, so both directions bite.
///    The server that sent you `"quantity": 3` all week sends `3.0` once and
///    your parser breaks. Read numbers as `num` and convert.
///
/// 3. A missing key returns null. It does not throw and it is indistinguishable
///    from an explicit `null` unless you ask containsKey. Into a non-nullable
///    field that null is a TypeError naming a type and not the key; into a
///    nullable one it is nothing at all, which is how a misspelled optional
///    field stays absent forever.
///
/// 4. DateTime is not a JSON type in either direction, and the two directions
///    are not symmetric. Decoding gives you the String; encoding a DateTime
///    throws. DateTime.parse accepts three zone forms — a trailing `Z`, a
///    numeric offset like `+02:00`, or no designator at all — but
///    toIso8601String can only ever write two of them: `Z` when the value is
///    UTC, and nothing whatsoever when it is not. It has no way to emit an
///    offset. So the zone is lost on the way OUT, not on the way in, and a
///    DateTime that is not UTC serializes to a bare wall clock that means a
///    different instant in every zone that reads it back.
///
/// 5. jsonEncode does not use an interface. It calls `toJson()` by dynamic
///    dispatch on anything it does not recognise, so a class with the method
///    encodes and a class without it throws JsonUnsupportedObjectError. Nothing
///    is checked at compile time either way, because jsonEncode takes Object?.
///
/// What json_annotation adds without the generator: JsonConverter, a plain
/// two-way conversion contract for item 4, and the `$`-prefixed helpers the
/// generator emits in `checked: true` mode — $checkedCreate, $checkedConvert
/// and $checkKeys — which turn items 1-3 from a bare TypeError into a
/// CheckedFromJsonException naming the class and the offending key. Their
/// doc comments say "should not be used directly", meaning json_serializable
/// users are not expected to call them; they are ordinary public top-level
/// functions and calling them by hand is exactly what the generated code does.
/// What it does NOT add: the annotations. @JsonKey and @JsonSerializable are
/// inert const objects at runtime — see AnnotationsAreInert.

/// The wire shape used throughout:
///   {"sku":"CSX-1","quantity":3,"price":19.5,
///    "created_at":"2026-05-15T18:27:32.045Z","tags":["ink","a4"],"note":null}
const productJson = '{"sku":"CSX-1","quantity":3,"price":19.5,'
    '"created_at":"2026-05-15T18:27:32.045Z","tags":["ink","a4"],"note":null}';

/// Decoding is the step that does not fail. Any syntactically valid JSON object
/// gets through here whatever the field types are, which is precisely why the
/// eventual error appears somewhere else.
Map<String, dynamic> decodeObject(String source) =>
    jsonDecode(source) as Map<String, dynamic>;

/// Pulling a field out is also typed dynamic, so this too accepts anything.
/// Between the decode and the first typed line there is no checkpoint.
dynamic pluck(Map<String, dynamic> json, String key) => json[key];

/// An ordinary typed function, with nothing to do with JSON. This is the "use"
/// in "fails at use": the implicit downcast on the argument is where a dynamic
/// value from the payload finally dies, arbitrarily far from the jsonDecode
/// that produced it.
int reorderLevel(int quantity) => quantity * 2;

/// DateTime in both directions, as json_annotation's JsonConverter — the one
/// piece of the package designed to be used by hand.
///
/// toUtc() on the write side is the whole point. toIso8601String() emits a
/// trailing `Z` only for a UTC instant; for anything else it emits no zone
/// designator at all, and it has no form that writes an offset. Normalising to
/// UTC first is what makes the string self-describing. toUtc() on the read side
/// is only tidiness — DateTime.parse already returns UTC for both `Z` and a
/// numeric offset — but it keeps every Product.createdAt in one zone, which
/// matters because DateTime's == compares the isUtc flag as well as the instant.
class IsoUtcDateTimeConverter extends JsonConverter<DateTime, String> {
  const IsoUtcDateTimeConverter();

  @override
  DateTime fromJson(String json) => DateTime.parse(json).toUtc();

  @override
  String toJson(DateTime object) => object.toUtc().toIso8601String();
}

/// The version people write. Its decode is fine: DateTime.parse normalises `Z`
/// and `+02:00` to UTC on its own. Its encode is where the zone disappears,
/// because a DateTime that is not UTC — anything from DateTime.now() or the
/// DateTime(...) constructor — has no way to write its offset.
class NaiveDateTimeConverter extends JsonConverter<DateTime, String> {
  const NaiveDateTimeConverter();

  @override
  DateTime fromJson(String json) => DateTime.parse(json);

  @override
  String toJson(DateTime object) => object.toIso8601String();
}

class Product {
  Product({
    required this.sku,
    required this.quantity,
    required this.price,
    required this.createdAt,
    required this.tags,
    this.note,
  });

  final String sku;
  final int quantity;
  final double price;
  final DateTime createdAt;
  final List<String> tags;
  final String? note;

  /// The version everyone writes first. Every cast here is a place the payload
  /// can kill you at runtime with no compile-time warning, and the error names
  /// a type rather than a field.
  factory Product.fromJsonNaive(Map<String, dynamic> json) => Product(
        sku: json['sku'] as String,
        // Breaks the day the server sends 3.0 instead of 3.
        quantity: json['quantity'] as int,
        // Breaks the day it sends 20 instead of 20.0 — the same bug pointing
        // the other way, because a whole number written without a decimal
        // point is an int and `as double` rejects it.
        price: json['price'] as double,
        createdAt: DateTime.parse(json['created_at'] as String),
        // Lazy: this succeeds even when an element is not a String.
        tags: (json['tags'] as List).cast<String>(),
        note: json['note'] as String?,
      );

  /// The same decode with the number and list problems fixed and nothing else
  /// changed. num covers both spellings; List<String>.from copies eagerly, so a
  /// bad element fails here, where you still know which payload it came from.
  factory Product.fromJsonNumSafe(Map<String, dynamic> json) => Product(
        sku: json['sku'] as String,
        quantity: (json['quantity'] as num).toInt(),
        price: (json['price'] as num).toDouble(),
        createdAt: const IsoUtcDateTimeConverter()
            .fromJson(json['created_at'] as String),
        tags: List<String>.from(json['tags'] as List),
        note: json['note'] as String?,
      );

  /// The same decode again, wrapped in the helpers json_serializable emits when
  /// `checked: true` — written by hand, in the shape the generator writes them.
  ///
  /// $checkedConvert catches whatever the per-field closure throws and rethrows
  /// it as a CheckedFromJsonException carrying the JSON key; $checkedCreate
  /// attaches the class name and maps an ArgumentError raised inside the
  /// constructor back to a key via fieldKeyMap. The conversion logic is
  /// unchanged — this buys the error message, not the safety.
  factory Product.fromJsonChecked(Map<String, dynamic> json) =>
      $checkedCreate<Product>(
        'Product',
        json,
        (convert) => Product(
          sku: convert('sku', (v) => v as String),
          quantity: convert('quantity', (v) => (v as num).toInt()),
          price: convert('price', (v) => (v as num).toDouble()),
          createdAt: convert('created_at',
              (v) => const IsoUtcDateTimeConverter().fromJson(v as String)),
          tags: convert('tags', (v) => List<String>.from(v as List)),
          note: convert('note', (v) => v as String?),
        ),
        // Dart field name to JSON key, for errors that arrive naming the field.
        fieldKeyMap: const {'createdAt': 'created_at'},
      );

  /// Key-level validation, which is the only way to see a missing key AS a
  /// missing key. $checkKeys inspects the map before any value is read, so
  /// "you never sent quantity", "you sent a key I do not know" and "you sent
  /// an explicit null" stay three different errors instead of one TypeError.
  factory Product.fromJsonStrict(Map<String, dynamic> json) {
    $checkKeys(
      json,
      allowedKeys: const [
        'sku',
        'quantity',
        'price',
        'created_at',
        'tags',
        'note',
      ],
      requiredKeys: const ['sku', 'quantity', 'price', 'created_at', 'tags'],
      disallowNullValues: const ['sku'],
    );
    return Product.fromJsonChecked(json);
  }

  /// jsonEncode finds this by dynamic dispatch on the name. Product implements
  /// no interface and extends nothing; a no-argument method called toJson is
  /// the entire contract, which is why misspelling it compiles.
  ///
  /// Every value in the returned map must itself be encodable, which is why
  /// createdAt goes through the converter here. Returning the DateTime would
  /// just move the failure inside jsonEncode — see LeakyProduct.
  Map<String, dynamic> toJson() => {
        'sku': sku,
        'quantity': quantity,
        'price': price,
        'created_at': const IsoUtcDateTimeConverter().toJson(createdAt),
        'tags': tags,
        'note': note,
      };
}

/// The same idea with no toJson. Passing one of these to jsonEncode is a
/// runtime failure only: the call site type-checks, because jsonEncode's
/// parameter is Object?.
class Warehouse {
  Warehouse(this.code);

  final String code;
}

/// A toJson that returns a map containing something jsonEncode still cannot
/// handle. Encoding recurses into whatever you return, so the DateTime fails
/// one level down — and the error that comes out names LeakyProduct, because
/// dart:convert re-attributes the failure to the nearest object whose toJson it
/// called. The DateTime is buried in `cause` and absent from the message.
class LeakyProduct {
  LeakyProduct(this.createdAt);

  final DateTime createdAt;

  Map<String, dynamic> toJson() => {'created_at': createdAt};
}

/// The annotations are const objects and nothing reads them at runtime. Adding
/// json_annotation to pubspec.yaml and annotating fields changes no behaviour
/// at all without json_serializable and build_runner: this class declares that
/// the wire key is `renamed` with a default of `fallback`, the hand-written
/// code below uses `value` and has no default, and the hand-written code is the
/// only thing that runs.
@JsonSerializable()
class AnnotationsAreInert {
  AnnotationsAreInert(this.value);

  @JsonKey(name: 'renamed', defaultValue: 'fallback')
  final String value;

  factory AnnotationsAreInert.fromJson(Map<String, dynamic> json) =>
      AnnotationsAreInert(json['value'] as String);

  Map<String, dynamic> toJson() => {'value': value};
}
