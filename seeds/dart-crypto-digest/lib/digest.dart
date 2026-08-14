import 'dart:convert';

import 'package:crypto/crypto.dart';

/// package:crypto returns a Digest, not a String. It prints as hex, so a
/// comparison against a hex literal fails unless toString() is called —
/// the most common way a correct hash looks wrong.
///
/// The input must be BYTES, and the codec is a real decision: utf8.encode
/// and latin1.encode agree on ASCII and disagree on everything above
/// U+007F, so the wrong one silently changes the digest for non-ASCII text.
String sha256Hex(String text) => sha256.convert(utf8.encode(text)).toString();

String hmacSha256Hex(List<int> key, String message) =>
    Hmac(sha256, key).convert(utf8.encode(message)).toString();

int digestByteLength(String text) => sha256.convert(utf8.encode(text)).bytes.length;
