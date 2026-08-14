import 'dart:convert';

import 'package:http/http.dart' as http;
import 'package:http/testing.dart';

/// Testing HTTP code with package:http and no network, plus the defaults that
/// make people misread what their code is actually sending and receiving.
///
/// MockClient takes a plain function from Request to Response, so nothing is
/// stubbed or monkeypatched. What the handler receives is not your object:
/// MockClient finalizes whatever BaseRequest the client produced and rebuilds
/// it as a plain Request carrying the same method, url, headers and bytes.
/// Assert on that copy rather than on your own call arguments and the client is
/// under test too. The flattening is total — a MultipartRequest arrives as a
/// plain Request, so casting the handler argument back to MultipartRequest to
/// read .fields fails. The one thing you must do to get here is inject the
/// Client instead of constructing one inside the function.
///
/// Four traps, all of them silent:
///
/// 1. A 4xx or 5xx is not an error. get, post and send return the Response and
///    only response.statusCode tells you, which is the opposite of package:dio,
///    so code ported from dio — or from any language whose client throws —
///    parses an error page as data and reports success. read and readBytes are
///    the exception: they are the only members that check, and they throw
///    ClientException for any status at or above 400. Everything else hands
///    back the response whatever it says.
///
/// 2. response.body is bodyBytes decoded with the charset from the response
///    content-type, and the fallback when no charset is given is latin1, NOT
///    utf8 — with one exception that is easy to generalise from and get wrong.
///    The special case is the exact type application/json, decoded as utf8
///    because RFC 8259 defines JSON as UTF-8. text/plain, text/html,
///    application/xml, a vendor subtype like application/vnd.api+json, and a
///    response with no content-type at all all fall back to latin1, so UTF-8
///    text arrives as mojibake through .body. bodyBytes never guesses, which
///    makes utf8.decode(response.bodyBytes) the read that always means what you
///    wrote.
///
/// 3. post(body: someMap) is form-encoded. A Map is assigned to
///    Request.bodyFields, which forces application/x-www-form-urlencoded and
///    throws a StateError if you declared any other content-type. JSON is
///    jsonEncode plus the header, by hand. A String body with no header at all
///    is announced as text/plain; charset=utf-8, which is how a correct JSON
///    payload earns a 415.
///
/// 4. ClientException is the transport failure a real client raises, and it is
///    the one thing worth catching around an http call. A MockClient handler is
///    not wrapped at all: whatever it throws reaches the caller with its own
///    type, which is how you test the socket-died path, and also why a broken
///    fixture surfaces as itself rather than as a network error.

/// The code under test. The Client is a parameter, which is the whole trick.
Future<http.Response> fetchUser(http.Client client, String id) => client.get(
      Uri.parse('https://api.example.com/users/$id?verbose=true'),
      headers: {'Authorization': 'Bearer t0ken'},
    );

/// The same call through read, the one member that does check the status.
Future<String> readUserBody(http.Client client, String id) => client.read(
      Uri.parse('https://api.example.com/users/$id?verbose=true'),
      headers: {'Authorization': 'Bearer t0ken'},
    );

/// What people write when they mean JSON. It is a form post.
Future<http.Response> createUserWithMapBody(
  http.Client client,
  Map<String, String> fields,
) =>
    client.post(Uri.parse('https://api.example.com/users'), body: fields);

/// JSON, spelled out: encode the payload and declare the type.
Future<http.Response> createUserAsJson(
  http.Client client,
  Map<String, Object?> payload,
) =>
    client.post(
      Uri.parse('https://api.example.com/users'),
      headers: {'content-type': 'application/json'},
      body: jsonEncode(payload),
    );

/// The same JSON with the header forgotten. http fills in text/plain.
Future<http.Response> createUserUndeclared(
  http.Client client,
  Map<String, Object?> payload,
) =>
    client.post(
      Uri.parse('https://api.example.com/users'),
      body: jsonEncode(payload),
    );

/// Keep the Requests the client actually built so the test can inspect them.
MockClient recordingClient(
  List<http.Request> seen,
  Future<http.Response> Function(http.Request request) respond,
) =>
    MockClient((request) {
      seen.add(request);
      return respond(request);
    });

/// A server that always answers with the same bytes and headers.
MockClient fixedBytesClient(List<int> bytes, Map<String, String> headers) =>
    MockClient((_) async => http.Response.bytes(bytes, 200, headers: headers));
