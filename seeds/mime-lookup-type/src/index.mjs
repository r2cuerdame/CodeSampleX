import mime from 'mime';

// mime v4 is ESM-only: require('mime') throws ERR_REQUIRE_ESM, and the v2
// helpers mime.lookup/mime.extension were renamed to getType/getExtension.
// An unknown extension returns null — not '' and not a default type — so
// callers must decide the fallback themselves.
export function typeFor(path) {
  return mime.getType(path);
}

export function extensionFor(type) {
  return mime.getExtension(type);
}
