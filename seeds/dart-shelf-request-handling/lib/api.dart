import 'package:shelf/shelf.dart';
import 'package:shelf_router/shelf_router.dart';

/// Handling shelf requests without binding a port. A `Handler` is nothing but
/// `FutureOr<Response> Function(Request)`, so a test builds a Request and calls
/// it. No socket, no mock, no fixture server: the router, the middleware and
/// the handler under test are all the real ones, and the whole round trip is a
/// function call.
///
/// Six things behave differently than the shape of the API suggests.
///
/// 1. shelf_router decides how to deliver URL parameters from the handler's
///    signature, not from the route. RouterEntry tests `_handler is Handler`
///    first: a handler that takes only a Request is called with only the
///    Request, and the captured values arrive through `request.params`, the
///    extension getter shelf_router adds to Request. A handler that takes extra
///    arguments has them applied positionally, in the order the parameters
///    appear in the route pattern — the closure's own parameter names are never
///    read, so swapping two of them silently swaps two values. Getting the
///    count wrong is not rejected when the route is registered; it is a
///    NoSuchMethodError on the first matching request.
///
/// 2. The default 404 is `Router.routeNotFound`, one shared object rather than
///    a fresh Response per request. shelf_router had to override `read()` on it
///    so the same instance can be served repeatedly, which is the loudest hint
///    in either package that an ordinary Response cannot. That object also
///    means "no match, keep looking" when a handler returns it: the router
///    resumes matching later routes. `Response.notFound('Route not found')` —
///    same status, same bytes — does not, because the router compares by
///    identity.
///
/// 3. `Pipeline().addMiddleware(a).addMiddleware(b).addHandler(h)` composes to
///    `a(b(h))`. Declaration order is the order requests are seen and the
///    reverse of the order responses are seen, so the outermost middleware
///    writes its response header last and wins a collision.
///
/// 4. Every shelf body is a single-subscription stream, whatever you passed in.
///    A String body is not stored as a String; `Body` encodes it to bytes and
///    wraps it in a Stream, and `read()` hands that stream over and nulls its
///    own reference. The second `readAsString()` is a StateError, thrown
///    synchronously out of the call rather than delivered to the Future. Worse
///    for middleware: `change()` carries the same Body object across, so a
///    logging middleware that reads the body and returns `response.change(...)`
///    forwards a response whose body is already gone. Read once, then put what
///    you read back with `change(body: ...)`.
///
/// 5. `Router.get` quietly registers two routes: the GET, and a HEAD in front
///    of it wrapped in a body-stripping middleware. A `router.head` you add
///    afterwards for the same pattern is dead code, because the generated one
///    already matched.
///
/// 6. Header lookup is case-insensitive, but the keys are not lowercased —
///    `CaseInsensitiveMap` canonicalises for lookup and keeps the spelling you
///    stored. dart:io lowercases header names as it parses them off the wire,
///    so a served request really does have lowercase keys, and a Request you
///    constructed has whatever case you typed. Only code that iterates `.keys`
///    or serialises the header map can tell, and that code passes in a unit
///    test and fails in production.

/// The application under test. Routes are registered against the real Router,
/// so what the test exercises is the routing, not a description of it.
Router buildApp() {
  final app = Router();

  // One argument, so shelf_router treats this as a plain Handler and the
  // capture arrives in request.params.
  app.get('/users/<id>', (Request request) {
    return Response.ok('user ${request.params['id']}');
  });

  // Extra arguments, so the captures are applied positionally instead. Both
  // styles are supported at once; the router chooses per route.
  app.get('/orgs/<org>/users/<id>', (Request request, String org, String id) {
    return Response.ok('$org/$id');
  });

  app.post('/users', (Request request) async {
    return Response(201, body: 'created ${await request.readAsString()}');
  });

  // Two handlers on one pattern. The first declines by returning the sentinel,
  // which sends the router back to matching rather than ending the request.
  app.get('/search', (Request request) {
    final q = request.url.queryParameters['q'];
    if (q == null) return Router.routeNotFound;
    return Response.ok('results for $q');
  });
  app.get('/search', (Request request) => Response.ok('search form'));

  return app;
}

/// Builds the Request an adapter would have built, without the socket. The
/// host is only there because Request requires an absolute requestedUri; the
/// router matches on `request.url`, which is the path relative to handlerPath.
Request buildRequest(
  String method,
  String path, {
  Map<String, Object>? headers,
  Object? body,
}) =>
    Request(
      method,
      Uri.parse('http://example.com$path'),
      headers: headers,
      body: body,
    );

/// Records the order it is entered and left, and appends its name to a shared
/// response header on the way out so the ordering can be read off the response
/// as well as off the log.
Middleware stamp(String name, List<String> log) =>
    (Handler innerHandler) => (Request request) async {
          log.add('$name >');
          final response = await innerHandler(request);
          log.add('< $name');
          final existing = response.headers['x-stamp'];
          return response.change(headers: {
            'x-stamp': existing == null ? name : '$existing,$name',
          });
        };

/// Sets a header outright instead of appending to it, so a pipeline of these
/// shows which writer lands last rather than only in which order they ran.
Middleware setOwner(String name) =>
    (Handler innerHandler) => (Request request) async =>
        (await innerHandler(request)).change(headers: {'x-owner': name});

/// Answers without calling the inner handler, which is what makes middleware
/// order load-bearing rather than cosmetic.
Middleware requireToken() => (Handler innerHandler) => (Request request) async {
      if (request.headers['authorization'] != 'Bearer t0ken') {
        return Response.forbidden('no token');
      }
      return innerHandler(request);
    };

/// The logging middleware everybody writes first. It reads the body to log it
/// and hands the same Response on, and `change` brings the drained Body with
/// it, so the adapter finds nothing left to write.
Middleware drainingLogger(List<String> log) =>
    (Handler innerHandler) => (Request request) async {
          final response = await innerHandler(request);
          log.add(await response.readAsString());
          return response.change(headers: {'x-logged': 'yes'});
        };

/// The same middleware with the one fix: put back what you took.
Middleware bodyLogger(List<String> log) =>
    (Handler innerHandler) => (Request request) async {
          final response = await innerHandler(request);
          final body = await response.readAsString();
          log.add(body);
          return response.change(body: body, headers: {'x-logged': 'yes'});
        };

/// The request-side version of the same mistake. The handler downstream gets a
/// Request whose body has already been consumed.
Middleware drainingRequestLogger(List<String> log) =>
    (Handler innerHandler) => (Request request) async {
          log.add(await request.readAsString());
          return innerHandler(request);
        };

/// And its fix, for the request side.
Middleware requestLogger(List<String> log) =>
    (Handler innerHandler) => (Request request) async {
          final body = await request.readAsString();
          log.add(body);
          return innerHandler(request.change(body: body));
        };
