import picomatch from 'picomatch';

// Three rules cause most glob surprises here:
//   1. `*` stops at a path separator; `**` crosses them.
//   2. Dotfiles are NOT matched unless dot:true, so "src/**/*.js" misses
//      "src/.config/a.js".
//   3. An ARRAY of patterns is OR-ed. Adding "!**/*.test.js" to the array
//      therefore excludes nothing — the file still matches the positive
//      pattern, so the result is true. Exclusion needs the `ignore`
//      option (or a lone negated pattern, which inverts by itself).
export function matcher(pattern, options = {}) {
  return picomatch(pattern, options);
}

// The correct way to say "these, except those".
export function matcherExcluding(pattern, ignore) {
  return picomatch(pattern, { ignore });
}
