"""What pip actually installed for orjson on Alpine, read off the installed
distribution instead of assumed from the version number.

orjson is a compiled Rust extension, so on a musl image there is exactly one
wheel pip can take: the musllinux one. The wheel that landed here during
resolve was

    orjson-3.11.9-cp312-cp312-musllinux_1_2_x86_64.whl   (136 kB)

and the only receipt for it that survives into an offline stage is the Tag
line in orjson-3.11.9.dist-info/WHEEL. That is what these helpers read.

The trap is what happens when the tag does not match. pip does not say
"wrong platform" — it falls back to the 5.6 MB sdist and tries to build it,
and the failure that follows is not the missing compiler people assume.
Forced here with --no-binary :all:, orjson's build backend (maturin, via
puccinialin) downloads rustup-init for x86_64-unknown-linux-musl, installs
21 MB of toolchain into the root user's .cache/puccinialin, and only then dies on
`cargo --version` returning exit status 127, which pip reports as
metadata-generation-failed. Installing build-base does not touch that. The
real question is which wheel tags exist for the interpreter in front of you.

The surprise from measuring the binary, which is the point of this half of
the sample: the extension is not statically linked, and the one library it
does need does not exist as a file. Its whole DT_NEEDED list is

    ['libc.so']

and no file called libc.so exists under any directory the loader searches —
the only libc file present is /lib/libc.musl-x86_64.so.1. It loads anyway
because musl's dynamic loader answers to that name as itself; running the
loader by hand on the extension prints "libc.so => /lib/ld-musl-x86_64.so.1".

Carried to a glibc image the same file is stopped twice, and the first stop
is not the interesting one. On python:3.12-slim `import orjson` fails before
any loader runs, with "No module named 'orjson.orjson'", because that
interpreter's EXT_SUFFIX is .cpython-312-x86_64-linux-gnu.so and the file on
disk ends in -musl.so. Push past that with ctypes.CDLL and the DT_NEEDED
name is what stops it: OSError, "libc.so: cannot open shared object file".
That image has no libc.so either, only /usr/lib/x86_64-linux-gnu/libc.so.6.
Both of those were measured on python:3.12-slim; a contract running on
Alpine cannot check them, so what it asserts is this half — the filename
against this interpreter's own EXT_SUFFIX, and the resolution against this
loader.

The extension links nothing for CPython either. Its Py* symbols are left
undefined and resolved from the interpreter that imports it, which is why
python's own binary lists libpython3.12.so.1.0 and the extension lists
nothing of the kind — and why running the loader on it standalone reports
those symbols as missing.
"""

import email
import importlib.metadata as metadata
import os
import subprocess
import sysconfig


def installed_wheel() -> dict:
    """The dist-info receipt: version, wheel tags, purelib flag, deps."""
    dist = metadata.distribution("orjson")
    wheel = email.message_from_string(dist.read_text("WHEEL") or "")
    return {
        "version": dist.version,
        "tags": wheel.get_all("Tag") or [],
        "root_is_purelib": (wheel["Root-Is-Purelib"] or "").strip() == "true",
        "requires_dist": dist.metadata.get_all("Requires-Dist") or [],
    }


def wheel_filename() -> str:
    """Reconstruct the filename pip downloaded from the recorded tag.

    A wheel name is name-version-tag.whl, so a single-tag dist-info pins the
    exact file. Comparing against a literal is how the sample proves the
    musllinux wheel was used rather than a locally built sdist.
    """
    info = installed_wheel()
    if len(info["tags"]) != 1:
        raise AssertionError(f"expected one wheel tag, got {info['tags']}")
    return f"orjson-{info['version']}-{info['tags'][0]}.whl"


def extension_module_path() -> str:
    """Absolute path of the compiled .so, taken from the installed RECORD."""
    dist = metadata.distribution("orjson")
    shared = [f for f in (dist.files or []) if str(f).endswith(".so")]
    if len(shared) != 1:
        raise AssertionError(f"expected one .so, got {shared}")
    return os.fspath(dist.locate_file(shared[0]))


def elf_needed_libraries(path: str) -> list[str]:
    """DT_NEEDED entries of an ELF64 file, read from its dynamic section.

    Walks the program headers to PT_DYNAMIC, collects the DT_NEEDED string
    offsets and DT_STRTAB, then maps that virtual address back through the
    PT_LOAD segments to a file offset. An empty list means nothing is loaded
    at runtime — the only way to check "statically linked" rather than trust
    the claim.
    """
    with open(path, "rb") as handle:
        raw = handle.read()
    if raw[:4] != b"\x7fELF":
        raise ValueError(f"{path} is not an ELF file")
    if raw[4] != 2:
        raise ValueError(f"{path} is not 64-bit ELF")

    def u16(at: int) -> int:
        return int.from_bytes(raw[at:at + 2], "little")

    def u64(at: int) -> int:
        return int.from_bytes(raw[at:at + 8], "little")

    phoff, phentsize, phnum = u64(0x20), u16(0x36), u16(0x38)
    loads: list[tuple[int, int, int]] = []
    dynamic: tuple[int, int] | None = None
    for index in range(phnum):
        at = phoff + index * phentsize
        p_type = int.from_bytes(raw[at:at + 4], "little")
        p_offset, p_vaddr, p_filesz = u64(at + 8), u64(at + 16), u64(at + 32)
        if p_type == 1:  # PT_LOAD
            loads.append((p_vaddr, p_offset, p_filesz))
        elif p_type == 2:  # PT_DYNAMIC
            dynamic = (p_offset, p_filesz)
    if dynamic is None:
        return []

    needed: list[int] = []
    strtab: int | None = None
    start, size = dynamic
    for at in range(start, start + size, 16):
        tag = int.from_bytes(raw[at:at + 8], "little", signed=True)
        value = u64(at + 8)
        if tag == 0:  # DT_NULL ends the table
            break
        if tag == 1:  # DT_NEEDED
            needed.append(value)
        elif tag == 5:  # DT_STRTAB
            strtab = value
    if not needed:
        return []
    if strtab is None:
        raise ValueError("DT_NEEDED without DT_STRTAB")

    base = None
    for vaddr, offset, filesz in loads:
        if vaddr <= strtab < vaddr + filesz:
            base = offset + (strtab - vaddr)
            break
    if base is None:
        raise ValueError("DT_STRTAB falls outside every PT_LOAD segment")

    names = []
    for offset in needed:
        at = base + offset
        names.append(raw[at:raw.index(b"\x00", at)].decode())
    return names


MUSL_LOADER = "/lib/ld-musl-x86_64.so.1"
LOADER_SEARCH_DIRS = ("/lib", "/usr/lib", "/usr/local/lib", "/lib64")


def libc_so_exists() -> bool:
    """Is there a file named libc.so anywhere the loader would look?

    Walks the search directories rather than stat-ing four paths, because the
    claim being checked is an absence and a shallow check would not earn it.
    """
    for root in LOADER_SEARCH_DIRS:
        if not os.path.isdir(root):
            continue
        for _dirpath, _dirnames, filenames in os.walk(root, onerror=lambda error: None):
            if "libc.so" in filenames:
                return True
    return False


def musl_loader_report(path: str) -> str:
    """Ask musl's loader to resolve an ELF file's needs, and read the answer.

    `ldso --list` prints the resolved map. It exits non-zero here because the
    extension's CPython symbols only exist inside a running interpreter, so
    both streams are returned together and the caller reads what it needs.
    """
    done = subprocess.run(
        [MUSL_LOADER, "--list", path],
        capture_output=True,
        text=True,
        check=False,
    )
    return done.stdout + done.stderr


def interpreter_libc() -> dict:
    """Evidence that this interpreter is a musl one, not just named alpine."""
    return {
        "soabi": sysconfig.get_config_var("SOABI"),
        "host": sysconfig.get_config_var("HOST_GNU_TYPE"),
        "ext_suffix": sysconfig.get_config_var("EXT_SUFFIX"),
        "musl_loader": os.path.exists(MUSL_LOADER),
        "glibc_loader": os.path.exists("/lib64/ld-linux-x86-64.so.2"),
        "musl_libc_file": os.path.exists("/lib/libc.musl-x86_64.so.1"),
        "libc_so_file": libc_so_exists(),
    }
