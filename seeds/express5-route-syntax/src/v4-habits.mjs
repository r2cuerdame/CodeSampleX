// The express 4 patterns that stop working the moment you install express 5.
// They live here as data + tiny helpers so the contract can OBSERVE the
// failure instead of trusting a comment about it.

// express 5 bundles path-to-regexp 8, which rejects these path strings when
// the route is REGISTERED — so the process dies at boot, on the line that
// mounts the router, with no request ever made. That is why the upgrade
// looks like "express 5 crashes on startup" rather than "my route 404s".
export const V4_ROUTE_PATHS = {
  optionalParam: '/users/:id?', // TypeError: Unexpected ? at index 10
  bareWildcard: '*', //            TypeError: Missing parameter name at index 1
  trailingWildcard: '/files/*', // TypeError: Missing parameter name at index 8
  inlineRegex: '/rx/:id(\\d+)', // TypeError: Unexpected ( at index 7
};

// Returns the Error express 5 throws for a v4 path, or null if it registered.
export function registerOrThrow(app, path) {
  try {
    app.get(path, (_req, res) => res.end());
    return null;
  } catch (err) {
    return err;
  }
}

// express 4 let middleware replace req.query wholesale. In express 5 req.query
// is a prototype getter, so the assignment throws in strict mode (ESM is
// always strict).
export function overwriteQuery(req) {
  req.query = { normalized: true };
}

// The quieter half of the same change: the getter re-parses the query string
// on EVERY access and hands back a fresh object, so writing onto the object it
// returned does not throw — the value is just gone by the next read.
export function mutateQuery(req) {
  req.query.normalized = 'yes';
}
