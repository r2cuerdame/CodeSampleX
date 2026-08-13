import semver from 'semver';

// semver is CommonJS, so the default import is the whole namespace. Two
// traps live here: prereleases are excluded from ordinary ranges unless you
// pass includePrerelease, and tags like "v3.2.1" must be coerced before any
// comparison — semver.major('v3.2.1') throws on the raw string.
export function fits(version, range, includePrerelease = false) {
  return semver.satisfies(version, range, { includePrerelease });
}

export function majorOfTag(tag) {
  const coerced = semver.coerce(tag);
  return coerced ? semver.major(coerced) : null;
}
