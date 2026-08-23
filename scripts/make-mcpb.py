#!/usr/bin/env python3
"""Builds the MCPB bundle from already-built binaries in dist/.

Usage: python scripts/make-mcpb.py <version>

Writes dist/codesamplex-mcp.mcpb and prints its sha256 on stdout, which the
release workflow feeds into server.json. Keeping this in the repository
rather than in the workflow means the bundle can be rebuilt and inspected
locally, byte for byte, without a CI run.
"""
import hashlib
import io
import json
import os
import sys
import zipfile

# One binary per platform. The MCPB manifest keys platform_overrides by OS
# and has no way to express CPU architecture, so a bundle cannot carry both
# arm64 and amd64 for the same platform. These are the mainstream targets;
# every other architecture is covered by the install script, which picks
# from all six release binaries.
TARGETS = {
    "darwin": "csx-darwin-arm64",
    "linux": "csx-linux-amd64",
    "win32": "csx-windows-amd64.exe",
}

# The tool list is NOT written here. It is generated from toolDefs() in
# internal/mcp/tools.go into scripts/mcp-tools.json, and
# internal/mcp/mcpbcatalog_test.go fails if that file falls behind. A manifest
# is what a directory renders when it cannot run the server, so a second
# hand-maintained list here would eventually publish a tool the binary inside
# the same bundle does not answer.
#
# Only `summary` is used: the MCPB manifest schema allows exactly `name` and
# `description` per tool entry (additionalProperties: false), so titles and
# annotations cannot travel in the bundle and reach clients through
# tools/list instead.
CATALOG = os.path.join(os.path.dirname(os.path.abspath(__file__)), "mcp-tools.json")

# Anthropic's local-connector submission requires a "Privacy Policy" section
# in the README, a privacy_policies array here, and an HTTPS URL serving the
# policy. This URL is the repository's own PRIVACY.md, which is live the
# moment a change to it merges and needs no separate deployment to stay in
# step with the behaviour it describes.
PRIVACY_POLICY_URL = "https://github.com/r2cuerdame/CodeSampleX/blob/main/PRIVACY.md"


def tools():
    with io.open(CATALOG, encoding="utf-8") as fh:
        catalog = json.load(fh)
    entries = []
    for t in catalog["tools"]:
        summary = t.get("summary", "").strip()
        if not summary:
            sys.exit("mcp-tools.json: tool %r has no summary" % t.get("name"))
        entries.append({"name": t["name"], "description": summary})
    if not entries:
        sys.exit("mcp-tools.json lists no tools")
    return entries


LONG_DESCRIPTION = (
    "CodeSampleX answers the question documentation cannot: does this API actually work on "
    "these exact versions, runtime and execution context, and if not, at which stage does it "
    "break? Answers come from compatibility evidence contributed by real development "
    "environments, and each returned sample had its contract executed in a sandbox. When "
    "nothing matches safely it returns NO_SAFE_MATCH instead of a plausible guess.\n\n"
    "Local-first and anonymous: build and test results are reduced to evidence on the machine, "
    "and source, paths, project names and raw logs are never transmitted. Run `csx init` to "
    "choose community or local-only mode; local-only sends nothing at all.\n\n"
    "Privacy policy: " + PRIVACY_POLICY_URL
)


def manifest(version):
    return {
        "manifest_version": "0.3",
        "name": "codesamplex",
        "display_name": "CodeSampleX",
        "version": version,
        "description": "Compatibility evidence from real builds: does this library API work on "
                       "your versions and runtime?",
        "long_description": LONG_DESCRIPTION,
        "author": {"name": "r2cuerdame", "url": "https://codesamplex.dev"},
        "homepage": "https://codesamplex.dev",
        "documentation": "https://github.com/r2cuerdame/CodeSampleX#readme",
        "repository": {"type": "git", "url": "https://github.com/r2cuerdame/CodeSampleX"},
        "support": "https://github.com/r2cuerdame/CodeSampleX/issues",
        "license": "Apache-2.0",
        "keywords": ["compatibility", "dependencies", "npm", "pypi", "golang", "cargo",
                     "evidence", "verification", "developer-tools"],
        "server": {
            "type": "binary",
            "entry_point": "server/" + TARGETS["linux"],
            "mcp_config": {
                "command": "${__dirname}/server/" + TARGETS["linux"],
                "args": ["mcp"],
                "env": {},
                "platform_overrides": {
                    "darwin": {"command": "${__dirname}/server/" + TARGETS["darwin"],
                               "args": ["mcp"]},
                    "win32": {"command": "${__dirname}/server/" + TARGETS["win32"],
                              "args": ["mcp"]},
                },
            },
        },
        "privacy_policies": [PRIVACY_POLICY_URL],
        "tools": tools(),
        "compatibility": {"platforms": ["darwin", "linux", "win32"]},
    }


# A zip stores each entry's mtime and permission bits, so the same inputs
# would otherwise produce a different digest on every build — and the digest
# is what MCP clients verify before installing. Pinning both makes the
# bundle reproducible: given the same binaries, anyone can rebuild it and
# get the hash the registry publishes.
ZIP_EPOCH = (1980, 1, 1, 0, 0, 0)


def _add(z, name, data, executable):
    info = zipfile.ZipInfo(name, date_time=ZIP_EPOCH)
    info.compress_type = zipfile.ZIP_DEFLATED
    info.create_system = 3  # unix, so the mode bits below are honoured
    info.external_attr = (0o755 if executable else 0o644) << 16
    z.writestr(info, data)


def main():
    if len(sys.argv) != 2:
        sys.exit("usage: make-mcpb.py <version>   (e.g. 0.1.0, no leading v)")
    version = sys.argv[1].lstrip("v")

    dist = "dist"
    missing = [f for f in TARGETS.values() if not os.path.exists(os.path.join(dist, f))]
    if missing:
        sys.exit("missing binaries in dist/: " + ", ".join(missing))

    out = os.path.join(dist, "codesamplex-mcp.mcpb")
    with zipfile.ZipFile(out, "w", zipfile.ZIP_DEFLATED, compresslevel=9) as z:
        _add(z, "manifest.json",
             (json.dumps(manifest(version), indent=2, ensure_ascii=False) + "\n").encode("utf-8"),
             executable=False)
        for _, fname in sorted(TARGETS.items()):
            with io.open(os.path.join(dist, fname), "rb") as fh:
                _add(z, "server/" + fname, fh.read(), executable=True)

    digest = hashlib.sha256(io.open(out, "rb").read()).hexdigest()
    io.open(out + ".sha256", "w", newline="\n").write(
        digest + "  codesamplex-mcp.mcpb\n")
    print(digest)


if __name__ == "__main__":
    main()
