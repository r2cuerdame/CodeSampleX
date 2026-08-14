import 'package:csx_dart_crypto/digest.dart';
import 'package:test/test.dart';

void main() {
  test('reproduces the NIST SHA-256 vector for "abc"', () {
    expect(
      sha256Hex('abc'),
      equals('ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad'),
    );
  });

  test('reproduces RFC 4231 HMAC-SHA256 test case 1', () {
    final key = List<int>.filled(20, 0x0b);
    expect(
      hmacSha256Hex(key, 'Hi There'),
      equals('b0344c61d8db38535ca8afceaf0bf12b881dc200c9833da726e9376c2e32cff7'),
    );
  });

  test('a sha256 digest is 32 bytes wide', () {
    expect(digestByteLength('codesamplex'), equals(32));
  });

  test('the input encoding decides the digest', () {
    expect(sha256Hex('cafe'), isNot(equals(sha256Hex('cafe\u0301'))));
  });
}
