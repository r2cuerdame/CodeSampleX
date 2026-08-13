"""Version range checks with packaging, the library pip itself uses."""

from packaging.specifiers import SpecifierSet
from packaging.version import Version


def fits(version, spec, allow_prerelease=False):
    """Report whether version satisfies spec.

    Prereleases are excluded from an ordinary specifier even when they fall
    numerically inside it — the single most surprising rule here, and the
    reason "1.13.0rc1" fails a ">=1.0,<2.0" check that "1.13.0" passes.
    Comparing version strings directly instead would order 1.9 above 1.10.
    """
    return SpecifierSet(spec, prereleases=allow_prerelease).contains(Version(version))


def newest(versions):
    """Return the highest version, ordered by PEP 440 rather than by string."""
    return str(max((Version(v) for v in versions)))
