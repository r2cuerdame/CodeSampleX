import 'dart:async';
import 'dart:io';

import 'package:csx_shelf_requests/api.dart';
import 'package:shelf/shelf.dart';
import 'package:shelf/shelf_io.dart' as shelf_io;
import 'package:shelf_router/shelf_router.dart';
import 'package:test/test.dart';

void main() {
  test('a Router dispatches on method and path, with no port bound', () async {
    final app = buildApp();

    // The whole round trip: build a Request, call the Router, read the
    // Response. Router implements call(), so it is itself a Handler.
    final user = await app(buildRequest('GET', '/users/u42'));
    expect(user.statusCode, equals(200));
    expect(await user.readAsString(), equals('user u42'));

    final created = await app(buildRequest('POST', '/users', body: 'ada'));
    expect(created.statusCode, equals(201));
    expect(await created.readAsString(), equals('created ada'));

    // Same path, unregistered method. shelf_router does not do 405: a method
    // it has no route for is indistinguishable from a path it has no route
    // for, so this is the plain 404.
    expect((await app(buildRequest('DELETE', '/users/u42'))).statusCode,
        equals(404));

    // A GET route answers HEAD too, because Router.add registers a second
    // entry for it wrapped in a body-stripping middleware. The status matches
    // the GET and the body is gone.
    final head = await app(buildRequest('HEAD', '/users/u42'));
    expect(head.statusCode, equals(200));
    expect(await head.readAsString(), isEmpty);
    // Measured, against the expectation that stripping the body zeroes the
    // length: it is still the length of the GET body. The stripping middleware
    // sets content-length to '0' first and Response.change recomputes the
    // header from the body it carries over, undoing that; the later
    // change(body: []) then leaves the header alone. RFC 9110 wants exactly
    // this number on a HEAD, but nothing in the code aimed for it, so assert
    // the number rather than the intent.
    expect(head.headers['content-length'], equals('8'));
    expect((await app(buildRequest('GET', '/users/u42'))).contentLength,
        equals(8));

    // <id> compiles to [^/]+, so it stops at a slash and does not swallow the
    // rest of the path.
    expect((await app(buildRequest('GET', '/users/u42/roles'))).statusCode,
        equals(404));

    // The generated HEAD entry is registered ahead of the GET, so a head route
    // added afterwards for the same pattern never matches. Registering it
    // first is the documented way to own HEAD, and it works.
    final headLast = Router()
      ..get('/ping', (Request request) => Response.ok('pong'))
      ..head('/ping',
          (Request request) => Response.ok('', headers: {'x-from': 'head'}));
    expect((await headLast(buildRequest('HEAD', '/ping'))).headers['x-from'],
        isNull);

    final headFirst = Router()
      ..head('/ping',
          (Request request) => Response.ok('', headers: {'x-from': 'head'}))
      ..get('/ping', (Request request) => Response.ok('pong'));
    expect((await headFirst(buildRequest('HEAD', '/ping'))).headers['x-from'],
        equals('head'));
  });

  test('captures reach a one-argument handler through request.params',
      () async {
    final app = buildApp();

    expect(await (await app(buildRequest('GET', '/users/u42'))).readAsString(),
        equals('user u42'));

    // Outside a Router the extension getter is not an error and not null: it
    // is an empty unmodifiable map, so a route registered without <id> reads
    // params['id'] as null rather than failing.
    final bare = buildRequest('GET', '/users/u42');
    expect(bare.params, isEmpty);
    expect(bare.params['id'], isNull);
    expect(() => bare.params['id'] = 'u42', throwsUnsupportedError);

    // params is carried in the request context under a namespaced key, which
    // is how it survives request.change() through middleware.
    late Request seen;
    final router = Router()
      ..get('/users/<id>', (Request request) {
        seen = request;
        return Response.ok('');
      });
    await router(buildRequest('GET', '/users/u42'));
    expect(seen.context['shelf_router/params'], equals({'id': 'u42'}));
    // change() copies the context, so an inner middleware that rewrites the
    // request keeps the captures the router attached.
    expect(
        seen.change(headers: {'x-inner': '1'}).params, equals({'id': 'u42'}));
  });

  test('a multi-argument handler is filled by position, never by name',
      () async {
    final app = buildApp();
    expect(
      await (await app(buildRequest('GET', '/orgs/acme/users/u42')))
          .readAsString(),
      equals('acme/u42'),
    );

    // The names in the closure are decoration. This handler calls the first
    // argument `id` and the second `org`, and still receives them in the order
    // the pattern captures them, so the values come out swapped.
    final swapped = Router()
      ..get('/orgs/<org>/users/<id>',
          (Request request, String id, String org) => Response.ok('$id/$org'));
    expect(
      await (await swapped(buildRequest('GET', '/orgs/acme/users/u42')))
          .readAsString(),
      equals('acme/u42'),
    );

    // Arity is not checked when the route is registered. Registering succeeds
    // and the failure arrives on the first matching request, as a
    // NoSuchMethodError from Function.apply.
    final wrongArity = Router()
      ..get('/users/<id>',
          (Request request, String id, String extra) => Response.ok(''));
    await expectLater(
      wrongArity(buildRequest('GET', '/users/u42')),
      throwsA(isA<NoSuchMethodError>()),
    );
  });

  test(
      'the unmatched route returns the router 404, a shared re-readable object',
      () async {
    final app = buildApp();

    final missing = await app(buildRequest('GET', '/nope'));
    expect(missing.statusCode, equals(404));
    expect(await missing.readAsString(), equals('Route not found'));

    // It is not a response built for this request: the default notFoundHandler
    // returns the one static instance every Router shares, and shelf_router had
    // to override read() on it so serving it twice works. That override is why
    // reading it twice below succeeds where a Response you built would throw.
    expect(identical(missing, Router.routeNotFound), isTrue);
    expect(await missing.readAsString(), equals('Route not found'));

    // Returning that object from a handler means "not matched, keep going",
    // so /search without ?q falls through to the second handler registered on
    // the same pattern.
    expect(await (await app(buildRequest('GET', '/search'))).readAsString(),
        equals('search form'));
    expect(
        await (await app(buildRequest('GET', '/search?q=shelf')))
            .readAsString(),
        equals('results for shelf'));

    // A look-alike does not fall through. Same status, same body text, but the
    // router compares by identity, so this ends the request and the second
    // handler is never reached.
    final lookalike = Router()
      ..get(
          '/search', (Request request) => Response.notFound('Route not found'))
      ..get('/search', (Request request) => Response.ok('search form'));
    final ended = await lookalike(buildRequest('GET', '/search'));
    expect(ended.statusCode, equals(404));
    expect(identical(ended, Router.routeNotFound), isFalse);

    // The 404 is a handler like any other and can be replaced.
    final custom = Router(
        notFoundHandler: (Request request) =>
            Response.notFound('no route for ${request.url.path}'));
    expect(await (await custom(buildRequest('GET', '/nope'))).readAsString(),
        equals('no route for nope'));
  });

  test('Pipeline runs middleware in declaration order and unwinds in reverse',
      () async {
    final log = <String>[];
    final handler = const Pipeline()
        .addMiddleware(stamp('outer', log))
        .addMiddleware(stamp('inner', log))
        .addHandler((Request request) {
      log.add('handler');
      return Response.ok('body');
    });

    final response = await handler(buildRequest('GET', '/'));

    // addMiddleware(a).addMiddleware(b) composes to a(b(handler)), so requests
    // travel outward-in and responses inward-out.
    expect(
        log, equals(['outer >', 'inner >', 'handler', '< inner', '< outer']));

    // Read off the response instead of off the log: inner appended first, so
    // the outermost middleware is the one whose value ends up last.
    expect(response.headers['x-stamp'], equals('inner,outer'));

    // Appending hides which write lands last, so run it again with middleware
    // that overwrite. The outermost one writes on the way out, after the
    // handler and after everything nested inside it, so its value is the one
    // that reaches the client.
    final collided = const Pipeline()
        .addMiddleware(setOwner('outer'))
        .addMiddleware(setOwner('inner'))
        .addHandler((Request request) =>
            Response.ok('body', headers: {'x-owner': 'handler'}));
    expect((await collided(buildRequest('GET', '/'))).headers['x-owner'],
        equals('outer'));

    // Middleware that answers without calling the inner handler cuts
    // everything below it out of the request, including the middleware
    // declared after it.
    log.clear();
    final guarded = const Pipeline()
        .addMiddleware(stamp('outer', log))
        .addMiddleware(requireToken())
        .addMiddleware(stamp('inner', log))
        .addHandler((Request request) {
      log.add('handler');
      return Response.ok('body');
    });

    final rejected = await guarded(buildRequest('GET', '/'));
    expect(rejected.statusCode, equals(403));
    expect(log, equals(['outer >', '< outer']));
    expect(rejected.headers['x-stamp'], equals('outer'));

    final allowed = await guarded(
        buildRequest('GET', '/', headers: {'authorization': 'Bearer t0ken'}));
    expect(allowed.statusCode, equals(200));
    expect(log.last, equals('< outer'));
    expect(allowed.headers['x-stamp'], equals('inner,outer'));
  });

  test('a body is a single-subscription stream whatever type you passed',
      () async {
    // Measured, against the expectation that a String body is held as a String
    // and can be read as often as you like. Body encodes it once and keeps a
    // Stream; there is no stored String to re-read, so the String case and the
    // Stream case fail identically.
    for (final response in [
      Response.ok('hello'),
      Response.ok(Stream<List<int>>.value('hello'.codeUnits)),
    ]) {
      expect(await response.readAsString(), equals('hello'));
      // The StateError is thrown out of the call, not delivered to the Future:
      // read() runs before readAsString builds one. `await` still catches it,
      // but .catchError on the returned Future never fires.
      expect(
        () => response.readAsString(),
        throwsA(isA<StateError>().having(
          (e) => e.message,
          'message',
          "The 'read' method can only be called once on a "
              'shelf.Request/shelf.Response object.',
        )),
      );
      expect(() => response.read(), throwsStateError);
    }

    // Requests are the same object model, so the same rule applies.
    final request = buildRequest('POST', '/users', body: 'ada');
    expect(await request.readAsString(), equals('ada'));
    expect(() => request.readAsString(), throwsStateError);

    // change() copies headers and context but shares the Body instance, which
    // is what turns "I only logged it" into an empty response.
    final drained = Response.ok('hello');
    await drained.readAsString();
    expect(() => drained.change(headers: {'x-logged': 'yes'}).readAsString(),
        throwsStateError);

    // Putting the string back is the whole fix, and it is a new Body.
    final refilled = Response.ok('hello');
    final captured = await refilled.readAsString();
    final forwarded = refilled.change(body: captured);
    expect(await forwarded.readAsString(), equals('hello'));
    expect(forwarded.headers['content-length'], equals('5'));
  });

  test('reading the body in middleware breaks the handler below it', () async {
    final log = <String>[];

    // Response side: the middleware logs the body and the caller gets nothing.
    final broken = const Pipeline()
        .addMiddleware(drainingLogger(log))
        .addHandler((Request request) => Response.ok('hello'));
    final emptied = await broken(buildRequest('GET', '/'));
    expect(log, equals(['hello']));
    expect(emptied.headers['x-logged'], equals('yes'));
    expect(() => emptied.readAsString(), throwsStateError);

    log.clear();
    final fixed = const Pipeline()
        .addMiddleware(bodyLogger(log))
        .addHandler((Request request) => Response.ok('hello'));
    final intact = await fixed(buildRequest('GET', '/'));
    expect(log, equals(['hello']));
    expect(intact.headers['x-logged'], equals('yes'));
    expect(await intact.readAsString(), equals('hello'));

    // Request side, through the real Router: shelf_router calls
    // request.change() to attach params, which carries the drained Body along,
    // so the route handler's readAsString fails.
    log.clear();
    final brokenIn = const Pipeline()
        .addMiddleware(drainingRequestLogger(log))
        .addHandler(buildApp().call);
    await expectLater(
      brokenIn(buildRequest('POST', '/users', body: 'ada')),
      throwsA(isA<StateError>()),
    );
    expect(log, equals(['ada']));

    log.clear();
    final fixedIn = const Pipeline()
        .addMiddleware(requestLogger(log))
        .addHandler(buildApp().call);
    final ok = await fixedIn(buildRequest('POST', '/users', body: 'ada'));
    expect(log, equals(['ada']));
    expect(await ok.readAsString(), equals('created ada'));
  });

  test('header lookup ignores case; the stored key keeps the case you wrote',
      () async {
    final request = buildRequest(
      'POST',
      '/users',
      headers: {'Content-Type': 'application/json', 'X-Request-Id': 'abc'},
      body: '{"n":1}',
    );

    // Lookup is canonicalised, so every spelling finds the value and
    // containsKey agrees.
    for (final spelling in ['Content-Type', 'content-type', 'CONTENT-TYPE']) {
      expect(request.headers[spelling], equals('application/json'));
      expect(request.headers.containsKey(spelling), isTrue);
    }
    expect(request.mimeType, equals('application/json'));
    expect(request.headers['x-request-id'], equals('abc'));

    // Measured, against the expectation that shelf lowercases header names on
    // the way in. CaseInsensitiveMap canonicalises for lookup only and hands
    // back the key as stored, so a constructed Request keeps your capitals.
    // Only iteration can tell, which is exactly what a header-forwarding proxy
    // or a snapshot assertion does.
    expect(request.headers.keys, contains('Content-Type'));
    expect(request.headers.keys, isNot(contains('content-type')));
    // shelf added this one itself, in the spelling shelf uses.
    expect(request.headers['content-length'], equals('7'));
    expect(request.headers.keys, contains('content-length'));

    // Responses behave the same way, and headersAll is the multi-value view of
    // the identical map.
    final response = Response.ok('hi', headers: {
      'X-Trace': ['a', 'b']
    });
    expect(response.headers['x-trace'], equals('a,b'));
    expect(response.headersAll['x-trace'], equals(['a', 'b']));
    expect(response.headers.keys, contains('X-Trace'));

    // Where the lowercase belief comes from: dart:io lowercases header names
    // while parsing them, so a request that arrived over a socket really does
    // have lowercase keys. Served on loopback, which works with the network
    // disabled, with the mixed-case name written straight onto the wire so
    // nothing in a client library can be blamed for the change.
    final served = Completer<List<String>>();
    final server = await shelf_io.serve((Request request) {
      if (!served.isCompleted) served.complete(request.headers.keys.toList());
      return Response.ok('');
    }, InternetAddress.loopbackIPv4, 0);
    final socket =
        await Socket.connect(InternetAddress.loopbackIPv4, server.port);
    socket.write('GET /users/u42 HTTP/1.1\r\n'
        'Host: example.com\r\n'
        'X-Request-Id: abc\r\n'
        'Connection: close\r\n'
        '\r\n');
    await socket.flush();
    final servedKeys = await served.future;
    socket.destroy();
    await server.close(force: true);

    expect(servedKeys, contains('x-request-id'));
    expect(servedKeys, isNot(contains('X-Request-Id')));
  });
}
