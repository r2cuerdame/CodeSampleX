import { Minimatch, escape, filter, minimatch, unescape } from 'minimatch';

export function matchesBracePattern(pathname) {
  return minimatch(pathname, 'src/*.{js,ts}');
}

export function isPartialMatch(pathname) {
  return minimatch(pathname, 'src/**/index.ts', { partial: true });
}

export function isStrictMatch(pathname) {
  return minimatch(pathname, 'src/**/index.ts');
}

export function describeMagic(pattern, options) {
  return new Minimatch(pattern, options).hasMagic();
}

export function keepMatching(values, pattern) {
  return values.filter(filter(pattern));
}

export function escapeWindowsLiteral(pathname) {
  return escape(pathname, { windowsPathsNoEscape: true });
}

export function unescapeWindowsLiteral(pattern) {
  return unescape(pattern, { windowsPathsNoEscape: true });
}

export function matchesEscapedWindowsLiteral(pathname, normalizedPath = pathname.replaceAll('\\', '/')) {
  return minimatch(normalizedPath, escapeWindowsLiteral(pathname), {
    windowsPathsNoEscape: true
  });
}
