import 'dart:convert';

import 'package:csx_http_mock/users.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:test/test.dart';

void main() {
  test('the handler is handed a finalized copy of the built Request', () async {
    final seen = <http.Request>[];
    final client = recordingClient(seen, (_) async => http.Response('{}', 200));

    await fetchUser(client, 'u42');

    final sent = seen.single;
    expect(sent, isA<http.Request>());
    expect(sent.method, equals('GET'));
    // The url is the one the client assembled, query string included, so a
    // wrong path or a dropped parameter fails here and not in production.
    expect(
      sent.url,
      equals(Uri.parse('https://api.example.com/users/u42?verbose=true')),
    );
    expect(sent.url.queryParameters['verbose'], equals('true'));
    // BaseRequest.headers looks up case-insensitively but stores the case you
    // wrote, so assert with either spelling and never with .keys.first.
    expect(sent.headers['authorization'], equals('Bearer t0ken'));
    expect(sent.headers['AUTHORIZATION'], equals('Bearer t0ken'));
    expect(sent.headers.keys, contains('Authorization'));
    expect(sent.body, isEmpty);
    expect(sent.finalized, isTrue);
  });

  test('the copy is a plain Request, so a MultipartRequest arrives flat',
      () async {
    final seen = <http.Request>[];
    final client = recordingClient(seen, (_) async => http.Response('', 200));

    final mine = http.MultipartRequest(
      'POST',
      Uri.parse('https://api.example.com/uploads'),
    )..fields['name'] = 'Ada Lovelace';
    await client.send(mine);

    // Not your object: MockClient rebuilds the finalized request. Everything
    // that made it a MultipartRequest is gone by the time the handler runs, so
    // read the encoded body, not .fields.
    expect(identical(seen.single, mine), isFalse);
    expect(seen.single, isNot(isA<http.MultipartRequest>()));
    expect(seen.single.headers['content-type'],
        startsWith('multipart/form-data; boundary='));
    expect(seen.single.body, contains('name="name"'));
    expect(seen.single.body, contains('Ada Lovelace'));
    // Your request was finalized on the way through and cannot be sent twice.
    expect(mine.finalized, isTrue);
  });

  test('a 404 does not throw; only statusCode says so', () async {
    // The single most common mistake in Dart HTTP code: waiting for an
    // exception that never arrives. The Future completes normally.
    final client = MockClient((_) async => http.Response(
          '{"error":"no such user"}',
          404,
          headers: {'content-type': 'application/json'},
          reasonPhrase: 'Not Found',
        ));

    final response = await fetchUser(client, 'ghost');

    expect(response.statusCode, equals(404));
    expect(response.reasonPhrase, equals('Not Found'));
    // The error page decodes exactly like a success payload would, which is
    // why a missing status check reads as valid data.
    expect(jsonDecode(response.body), equals({'error': 'no such user'}));
    // A 503 is equally uneventful, so checking the status is not optional.
    final boom = MockClient((_) async => http.Response('down', 503));
    expect((await fetchUser(boom, 'ghost')).statusCode, equals(503));
  });

  test('read and readBytes are the only members that check the status',
      () async {
    final client = MockClient((_) async => http.Response(
          '{"error":"no such user"}',
          404,
          headers: {'content-type': 'application/json'},
          reasonPhrase: 'Not Found',
        ));

    // The same 404 that get returned quietly is a ClientException here, which
    // is as close to raise_for_status as package:http gets. It costs you the
    // response: ClientException carries a message and a uri, so the error body
    // the server sent is gone.
    await expectLater(
      readUserBody(client, 'ghost'),
      throwsA(isA<http.ClientException>().having(
        (e) => e.message,
        'message',
        allOf(contains('failed with status 404: Not Found'),
            isNot(contains('no such user'))),
      )),
    );
    await expectLater(
      client.readBytes(Uri.parse('https://api.example.com/users/ghost')),
      throwsA(isA<http.ClientException>()),
    );

    // The cut is at 400, not at 2xx: a 399 is read back as a body.
    final odd = MockClient((_) async => http.Response('still fine', 399));
    expect(await readUserBody(odd, 'u42'), equals('still fine'));
  });

  test('response.body guesses an encoding, bodyBytes does not', () async {
    final utf8Bytes = utf8.encode('café ☕');
    Future<http.Response> read(Map<String, String> headers) =>
        fetchUser(fixedBytesClient(utf8Bytes, headers), 'u42');

    // Measured, against the expectation: application/json with no charset is
    // special-cased to utf8 because RFC 8259 defines JSON as UTF-8. So the
    // most common case is safe and teaches the wrong lesson.
    expect((await read({'content-type': 'application/json'})).body,
        equals('café ☕'));

    // Every other type without a charset falls back to latin1, and so does a
    // response with no content-type at all. Same bytes, mangled string. The
    // special case is the exact type application/json and nothing else, so a
    // JSON:API or any other +json vendor subtype is mojibake — which is the
    // generalisation the line above invites and the reason to check it.
    for (final headers in [
      {'content-type': 'text/plain'},
      {'content-type': 'text/html'},
      {'content-type': 'application/xml'},
      {'content-type': 'application/vnd.api+json'},
      <String, String>{},
    ]) {
      final mangled = await read(headers);
      expect(mangled.body, equals(latin1.decode(utf8Bytes)));
      expect(mangled.body, isNot(equals('café ☕')));
      // bodyBytes is untouched, so the fix never depends on the server.
      expect(mangled.bodyBytes, equals(utf8Bytes));
      expect(utf8.decode(mangled.bodyBytes), equals('café ☕'));
    }

    // Declaring the charset is the other fix.
    expect((await read({'content-type': 'text/plain; charset=utf-8'})).body,
        equals('café ☕'));
  });

  test('a Map body is form-encoded, and JSON has to be spelled out', () async {
    final seen = <http.Request>[];
    final client = recordingClient(seen, (_) async => http.Response('', 201));

    await createUserWithMapBody(client, {'name': 'Ada Lovelace', 'w': 'café'});
    // Exactly this, with no charset parameter appended.
    expect(seen.single.headers['content-type'],
        equals('application/x-www-form-urlencoded'));
    // Percent-encoded UTF-8, with + for the space. Nothing about it is JSON.
    expect(seen.single.body, equals('name=Ada+Lovelace&w=caf%C3%A9'));

    seen.clear();
    await createUserAsJson(client, {'name': 'Ada Lovelace', 'w': 'café'});
    // Also exactly this: charset is undefined for JSON, so http leaves the
    // header alone.
    expect(seen.single.headers['content-type'], equals('application/json'));
    // The bytes on the wire are utf8, because a Request's default encoding is
    // utf8 — the mirror image of the Response's latin1 fallback. Assert the
    // bytes: jsonDecode(request.body) round-trips through the same encoding it
    // was written with, so it would pass under latin1 too and prove nothing.
    expect(
      seen.single.bodyBytes,
      equals(utf8.encode(jsonEncode({'name': 'Ada Lovelace', 'w': 'café'}))),
    );

    seen.clear();
    await createUserUndeclared(client, {'name': 'Ada Lovelace'});
    // Correct JSON bytes announced as text/plain. This is the 415.
    expect(seen.single.headers['content-type'],
        equals('text/plain; charset=utf-8'));

    seen.clear();
    await client.post(
      Uri.parse('https://api.example.com/users'),
      headers: {'content-type': 'application/xml'},
      body: '<user name="Ada Lovelace"/>',
    );
    // The rule behind all four headers: a charset is appended only to text/*
    // and the XML types, which is why XML gets one and JSON does not.
    expect(seen.single.headers['content-type'],
        equals('application/xml; charset=utf-8'));

    // Declaring JSON and passing a Map is a StateError, not a silent
    // mis-encode: bodyFields refuses any other content-type.
    await expectLater(
      client.post(
        Uri.parse('https://api.example.com/users'),
        headers: {'content-type': 'application/json'},
        body: {'name': 'Ada Lovelace'},
      ),
      throwsA(isA<StateError>().having(
        (e) => e.message,
        'message',
        contains('content-type "application/json"'),
      )),
    );
  });

  test('a ClientException from the handler reaches the caller', () async {
    final client = MockClient((request) async =>
        throw http.ClientException('Connection reset by peer', request.url));

    // Unwrapped, with the uri intact: this is the shape of a real transport
    // failure and the one exception worth catching around an http call.
    await expectLater(
      fetchUser(client, 'u42'),
      throwsA(isA<http.ClientException>()
          .having((e) => e.message, 'message', 'Connection reset by peer')
          .having((e) => e.uri, 'uri',
              Uri.parse('https://api.example.com/users/u42?verbose=true'))),
    );

    // Nothing is wrapped on the way out, so a handler that throws something
    // else delivers that type verbatim. Useful, and the reason a mistake in a
    // fixture never disguises itself as a network failure.
    await expectLater(
      fetchUser(
          MockClient((_) async => throw const FormatException('bad fixture')),
          'u42'),
      throwsA(isA<FormatException>()),
    );
  });
}
