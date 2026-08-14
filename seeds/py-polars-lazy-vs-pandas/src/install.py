"""What `pip install polars` actually puts on disk on Alpine, read off the
installed distributions rather than assumed from the version number.

Since the 1.4x line, polars on PyPI is two distributions, and this is the
part that breaks pinned installs. `polars` itself is pure Python:

    polars-1.43.2-py3-none-any.whl                             (847 kB)

and the compiled Rust engine lives in a separate distribution it depends on,
which is where the wheel tags are and where all 55 MB went:

    polars_runtime_32-1.43.2-cp310-abi3-musllinux_1_2_x86_64.whl  (55.2 MB)

Both of those are the files this seed's resolve stage downloaded. The
musllinux wheel is the one that matters here, and it belongs to the engine
rather than to polars: "does polars install on Alpine" is entirely a
question about polars-runtime-32. It does — 1.43.2 publishes a
musllinux_1_2 wheel for this arch, and pip took it.

The tag says cp310 on a 3.12 interpreter and that is not a mismatch. It is
abi3, the CPython stable ABI, so one build serves 3.10 upward and the shared
object is named _polars_runtime.abi3.so rather than carrying this
interpreter's EXT_SUFFIX (.cpython-312-x86_64-linux-musl.so). A version
matrix that expects a wheel per minor version will not find one.

The trap in the split, measured: a requirements file that pins only
`polars` and installs with --no-deps does not fail at import. It warns.

    UserWarning: Polars binary is missing!

`import polars` then succeeds, polars.__version__ is the empty string, and
the first real call dies with NameError: name 'PySeries' is not defined —
a NameError, not an ImportError, from inside the library. So a smoke test
that imports the package passes on a broken install, and the failure
surfaces later at an unrelated line. The check worth making is the one
below: that polars.__version__ is non-empty and matches the distribution
metadata. Pinning the full closure, polars and polars-runtime-32 together,
is what actually prevents it.
"""

import email
import importlib.metadata as metadata
import sysconfig


def installed(name: str) -> dict:
    """The dist-info receipt for one distribution: version, tags, deps."""
    dist = metadata.distribution(name)
    wheel = email.message_from_string(dist.read_text("WHEEL") or "")
    return {
        "name": dist.metadata["Name"],
        "version": dist.version,
        "tags": wheel.get_all("Tag") or [],
        "root_is_purelib": (wheel["Root-Is-Purelib"] or "").strip() == "true",
        "requires_dist": dist.metadata.get_all("Requires-Dist") or [],
        "shared_objects": [str(f) for f in (dist.files or []) if str(f).endswith(".so")],
    }


def wheel_filename(name: str) -> str:
    """Reconstruct the wheel pip downloaded, from the tag it recorded.

    A wheel is named name-version-tag.whl with the name normalized to
    underscores, so a dist-info carrying a single Tag pins the exact file.
    That makes "which wheel did this environment get" checkable offline,
    without a pip log.
    """
    info = installed(name)
    if len(info["tags"]) != 1:
        raise AssertionError(f"expected one wheel tag, got {info['tags']}")
    normalized = info["name"].replace("-", "_")
    return f"{normalized}-{info['version']}-{info['tags'][0]}.whl"


def interpreter_libc() -> dict:
    """Evidence that this really is a musl interpreter, not just an image name."""
    return {
        "soabi": sysconfig.get_config_var("SOABI"),
        "ext_suffix": sysconfig.get_config_var("EXT_SUFFIX"),
    }
