#!/usr/bin/env python3
"""Verify a retained host commit; archive only its proven owner, never deploy.

The canonical controller streams this reviewed source under the same command
flock as deploy.ps1. No retained script is executed. Verification is read-only;
release writes a receipt and atomically renames the original lock directory.
"""
import base64
import datetime
import hashlib
import json
import os
from pathlib import Path
import re
import signal
import stat
import subprocess
import sys
import time


class Refusal(Exception):
    pass


def require(condition, stage):
    if not condition:
        raise Refusal(stage)


def matches(value, pattern):
    return isinstance(value, str) and re.fullmatch(pattern, value) is not None


def strict_json(raw):
    def pairs(items):
        result = {}
        for key, value in items:
            require(key not in result, "duplicate-json-key")
            result[key] = value
        return result
    return json.loads(raw, object_pairs_hook=pairs,
                      parse_constant=lambda _: (_ for _ in ()).throw(Refusal("nonfinite-json")))


def utc():
    return datetime.datetime.now(datetime.timezone.utc).isoformat()


def timestamp(value):
    require(matches(value, r"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?(?:Z|\+00:00)"), "timestamp")
    # Python 3.8/3.10 reject nanosecond fractions. Compare integer nanoseconds
    # without rewriting the original identity string or losing precision.
    seconds = int(datetime.datetime.fromisoformat(value[:19] + "+00:00").timestamp())
    fraction = re.search(r"\.(\d+)", value)
    return seconds * 1_000_000_000 + (int(fraction[1].ljust(9, "0")) if fraction else 0)


def normalize_index(value):
    return " ".join(value.replace("public.", "").split())


def metadata(info):
    return (info.st_dev, info.st_ino, info.st_mode, info.st_uid, info.st_gid,
            info.st_nlink, info.st_size, info.st_mtime_ns, info.st_ctime_ns)


def verify_permissions(info, private_group_directory=False):
    require(info.st_uid in (0, os.geteuid()), "untrusted-retained-owner")
    require(not info.st_mode & 0o002, "writable-retained-state")
    if not info.st_mode & 0o020:
        return
    # Canonical mkdir can create deploy/ as 0775 under Ubuntu's 002 umask.
    # Only that directory may use an owner-private primary group. Never apply
    # this exception to the lock, state, evidence, receipt, or deploy parent.
    require(private_group_directory and stat.S_ISDIR(info.st_mode) and
            stat.S_IMODE(info.st_mode) == 0o775 and info.st_uid == os.geteuid() and
            info.st_gid == os.getegid(), "writable-retained-state")
    import grp
    import pwd
    account = pwd.getpwuid(os.geteuid())
    group = grp.getgrgid(info.st_gid)
    accounts = pwd.getpwall()
    require(account.pw_uid == info.st_uid and account.pw_gid == info.st_gid and
            group.gr_gid == info.st_gid and group.gr_name == account.pw_name and
            set(group.gr_mem).issubset({account.pw_name, "root"}),
            "untrusted-deploy-directory-group")
    require(any(row.pw_uid == account.pw_uid and row.pw_gid == account.pw_gid and
                row.pw_name == account.pw_name for row in accounts) and
            all(row.pw_uid in (0, account.pw_uid) for row in accounts if row.pw_gid == info.st_gid),
            "untrusted-deploy-directory-group")


def verify_no_access_acl(path):
    # A private owning group does not exclude named-user/group ACL writers.
    # Reject extended access ACLs instead of interpreting or trusting them.
    try:
        attributes = os.listxattr(path, follow_symlinks=False)
    except OSError:
        raise Refusal("unavailable-deploy-directory-acl") from None
    require("system.posix_acl_access" not in attributes, "untrusted-deploy-directory-acl")


# The same reviewed ledger/index contract as offline-migration.py. This
# verifier deliberately has no migration, repair-barrier or rollback methods.
INDEXES = {
    "evidence_agg_builder_coord_idx": "CREATE INDEX evidence_agg_builder_coord_idx ON evidence_agg USING btree (builder_purl_coord(purl), purl, symbol)",
    "snapshots_builder_coord_idx": "CREATE INDEX snapshots_builder_coord_idx ON compatibility_snapshots USING btree (builder_purl_coord(purl), purl, symbol)",
    "evidence_agg_builder_changed_idx": "CREATE INDEX evidence_agg_builder_changed_idx ON evidence_agg USING btree (last_seen, purl, symbol)",
    "samples_builder_created_idx": "CREATE INDEX samples_builder_created_idx ON samples USING btree (created_at, sample_id)",
}
INDEXES37 = dict(INDEXES, **{
    "failure_clusters_pkg_count_idx": "CREATE INDEX failure_clusters_pkg_count_idx ON failure_clusters USING btree (package_name, observation_count DESC, id)",
    "samples_live_created_id_idx": "CREATE INDEX samples_live_created_id_idx ON samples USING btree (created_at DESC, sample_id) WHERE (NOT quarantined)",
})
MIGRATIONS = {"0036_builder_projections.sql": (37, INDEXES),
              "0037_slow_query_indexes.sql": (38, INDEXES37)}


def verify_indexes(rows, expected):
    require(isinstance(rows, list) and len(rows) == len(expected), "index-count")
    require({r["name"] for r in rows} == set(expected), "index-names")
    for row in rows:
        require(row["valid"] is True and row["ready"] is True and
                normalize_index(row["definition"]) == expected[row["name"]], "index-definition")


def make_binding(request):
    require(request.get("mode") in ("verify", "release"), "mode")
    require(matches(request.get("repository"), r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+"), "repository")
    for key in ("sourceRunId", "reconciliationRunId"):
        require(matches(request.get(key), r"[1-9][0-9]*"), "run-id")
    for key in ("sourceRunAttempt", "sourceArtifactId", "reconciliationRunAttempt"):
        require(type(request.get(key)) is int and request[key] > 0, "run-or-artifact-number")
    for key in ("sourceArtifactSha256", "hostEvidenceSha256"):
        require(matches(request.get(key), r"[0-9a-f]{64}"), "evidence-digest")
    for key in ("previousSha", "operationalSha"):
        require(matches(request.get(key), r"[0-9a-f]{40}"), "revision")
    require(matches(request.get("previousImageDigest"), r"sha256:[0-9a-f]{64}"), "previous-image")
    evidence = request["hostEvidence"]
    for key, expected in {"schemaVersion": 1, "phase": "committed", "conclusion": "success",
                          "acceptanceAuthority": "host", "controllerSmoke": "host-verified",
                          "health": "ok", "proxyHealth": "ok", "smoke": "pass",
                          "representativeSmoke": "pass", "cleanup": "pass",
                          "migrationVerification": "pass", "rollback": "not-started"}.items():
        require(type(evidence.get(key)) is type(expected) and evidence[key] == expected, "retained-" + key)
    require(matches(evidence.get("owner"), r"[0-9a-f]{32}"), "owner")
    require(evidence.get("unit") == "csx-migration-" + evidence["owner"] + ".service", "unit")
    for key in ("targetSha", "operationalSha", "servedRevision"):
        require(matches(evidence.get(key), r"[0-9a-f]{40}"), "retained-revision")
    require(evidence["targetSha"] == evidence["servedRevision"] != request["previousSha"], "target-previous")
    require(matches(evidence.get("imageDigest"), r"sha256:[0-9a-f]{64}"), "image")
    require(matches(evidence.get("releaseTag"), r"v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)"), "release")
    ledger = evidence["migrationLedger"]
    require(ledger.get("version") in MIGRATIONS, "reviewed-migration")
    count, indexes = MIGRATIONS[ledger["version"]]
    require(type(ledger.get("count")) is int and ledger == {"version": ledger["version"], "count": count}, "ledger")
    verify_indexes(evidence["indexes"], indexes)
    started = timestamp(evidence["serverStartedAt"])
    require(timestamp(evidence["activationStartedAt"]) <= started <= timestamp(evidence["completedAt"]), "activation-window")
    require(timestamp(evidence["cleanupCompletedAt"]) <= started, "cleanup-window")
    require(evidence["phaseTimings"]["activation"]["outcome"] == "pass", "activation-outcome")
    require(type(evidence.get("migrationTimeoutSeconds")) is int and
            60 <= evidence["migrationTimeoutSeconds"] <= 1800, "migration-budget")
    binding = {key: request[key] for key in ("repository", "sourceRunId", "sourceRunAttempt", "sourceArtifactId",
                                           "sourceArtifactSha256", "hostEvidenceSha256", "previousSha", "previousImageDigest")}
    binding.update({key: evidence[key] for key in ("owner", "targetSha", "operationalSha", "imageDigest",
                                                "migrationLedger", "serverStartedAt", "releaseTag",
                                                "migrationTimeoutSeconds")})
    return binding


class Host:
    def __init__(self, request, root=Path("/opt/codesamplex"), control_home=None):
        self.request = request
        self.binding = make_binding(request)
        self.root = root
        self.deploy = root / "deploy"
        self.state = self.deploy / (".migration-" + self.binding["owner"])
        self.lock = root / ".deploy-lock"
        self.archive = root / (".deploy-reconciled-" + self.binding["owner"])
        self.abort = (control_home or Path.home()) / (".csx-deploy-aborted-" + self.binding["owner"])
        self.deadline = time.monotonic() + 170
        self.snapshots = {}

    def remember(self, path, value, receipt_created=False):
        previous = self.snapshots.get(path)
        if previous is not None:
            if receipt_created:
                # Adding our exclusive receipt legitimately changes only the
                # lock directory size/times. Its inode, owner, mode and link
                # count must remain the original ones; other snapshots stay exact.
                require(path == self.lock and value[1] is None and previous[1] is None and
                        value[0][:6] == previous[0][:6], "retained-state-changed")
            else:
                require(value == previous, "retained-state-changed")
        self.snapshots[path] = value

    def directory(self, path, receipt_created=False):
        require(not path.is_symlink() and path.is_dir() and path.resolve() == path.absolute(), "unsafe-directory")
        info = path.lstat()
        if os.name == "posix":
            verify_permissions(info, private_group_directory=path == self.deploy)
            if path == self.deploy and info.st_mode & 0o020:
                verify_no_access_acl(path)
        self.remember(path, (metadata(info), None), receipt_created)

    def regular(self, path):
        require(not path.is_symlink() and path.is_file() and path.stat().st_nlink == 1, "unsafe-file")
        info = path.lstat()
        if os.name == "posix":
            verify_permissions(info)
        require(stat.S_ISREG(info.st_mode) and info.st_nlink == 1, "unsafe-file")
        require(info.st_size <= 2 * 1024 * 1024, "retained-file-size")
        fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
        with os.fdopen(fd, "rb") as file:
            require(metadata(os.fstat(file.fileno())) == metadata(info), "retained-state-changed")
            raw = file.read(2 * 1024 * 1024 + 1)
            require(len(raw) <= 2 * 1024 * 1024, "retained-file-size")
            require(metadata(os.fstat(file.fileno())) == metadata(info), "retained-state-changed")
        require(metadata(path.lstat()) == metadata(info), "retained-state-changed")
        self.remember(path, (metadata(info), raw))
        return raw

    def stable(self):
        for path, (_, raw) in list(self.snapshots.items()):
            if raw is None:
                self.directory(path)
            else:
                self.regular(path)

    def retained(self):
        for path in (self.root, self.deploy, self.state):
            self.directory(path)
        raw = self.regular(self.state / "evidence.json")
        require(hashlib.sha256(raw).hexdigest() == self.binding["hostEvidenceSha256"], "retained-evidence-digest")
        require(strict_json(raw) == self.request["hostEvidence"], "retained-evidence")
        config = strict_json(self.regular(self.state / "config.json"))
        expected = {key: self.binding[key] for key in ("targetSha", "previousSha", "operationalSha", "imageDigest", "previousImageDigest")}
        expected.update(expectedMigration=self.binding["migrationLedger"]["version"],
                        expectedMigrationCount=self.binding["migrationLedger"]["count"],
                        expectedReleaseTag=self.binding["releaseTag"],
                        migrationTimeoutSeconds=self.binding["migrationTimeoutSeconds"])
        for key, value in expected.items():
            require(type(config.get(key)) is type(value) and config[key] == value, "config-" + key)

    def receipt(self, path):
        value = strict_json(self.regular(path / "reconciliation.json"))
        require(type(value.get("schemaVersion")) is int and value["schemaVersion"] == 1 and
                value.get("binding") == self.binding, "receipt-binding")
        require(matches(value.get("reconciliationRunId"), r"[1-9][0-9]*") and
                type(value.get("reconciliationRunAttempt")) is int and value["reconciliationRunAttempt"] > 0 and
                matches(value.get("operationalSha"), r"[0-9a-f]{40}"), "receipt-provenance")
        timestamp(value["verifiedAt"])
        self.prepared(value["preparedArtifact"])
        return value

    def ownership(self):
        require(not self.abort.exists() and not self.abort.is_symlink(), "owner-aborted")
        require(not self.lock.is_symlink() and not self.archive.is_symlink(), "unsafe-lock")
        if self.lock.exists():
            require(not self.archive.exists(), "lock-archive-collision")
            path, state = self.lock, "owned"
        else:
            require(self.archive.exists(), "missing-owner-proof")
            path, state = self.archive, "archived"
        self.directory(path)
        require(self.regular(path / "owner") == (self.binding["owner"] + "\n").encode(), "lock-owner")
        names = {entry.name for entry in path.iterdir()}
        require(names in ({"owner"}, {"owner", "reconciliation.json"}), "lock-contents")
        receipt = self.receipt(path) if "reconciliation.json" in names else None
        require(state != "archived" or receipt is not None, "archive-without-receipt")
        return state, receipt

    @staticmethod
    def prepared(artifact):
        require(isinstance(artifact, dict) and set(artifact) == {"id", "digest", "runId", "runAttempt"}, "prepared-shape")
        require(type(artifact["id"]) is int and artifact["id"] > 0 and
                matches(artifact["digest"], r"sha256:[0-9a-f]{64}") and
                matches(artifact["runId"], r"[1-9][0-9]*") and
                type(artifact["runAttempt"]) is int and artifact["runAttempt"] > 0, "prepared-provenance")

    def command(self, args, seconds=10):
        remaining = min(seconds, self.deadline - time.monotonic())
        require(remaining > 0, "deadline")
        process = subprocess.Popen(args, cwd=self.deploy, text=True, stdin=subprocess.DEVNULL,
                                   stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=True)
        try:
            out, _ = process.communicate(timeout=remaining)
        except BaseException:
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            process.wait(timeout=5)
            raise Refusal("command-timeout")
        require(process.returncode == 0, "command-failed")
        return out

    def query(self, sql):
        return strict_json(self.command(["docker", "compose", "exec", "-T", "-e",
                          "PGOPTIONS=-c default_transaction_read_only=on -c statement_timeout=5000 -c lock_timeout=3000",
                          "db", "psql", "-X", "-U", "csx", "-d", "csx", "-v", "ON_ERROR_STOP=1", "-Atqc", sql]))

    def identity(self):
        rows = strict_json(self.command(["docker", "inspect", "codesamplex-server-1"]))
        require(len(rows) == 1, "container-count")
        row = rows[0]
        require(row["Image"] == self.binding["imageDigest"], "live-image")
        require([v for v in row["Config"]["Env"] if v.startswith("CSX_VERSION=")] ==
                ["CSX_VERSION=" + self.binding["targetSha"]], "configured-revision")
        require(row["State"].get("Running") is True and row["State"].get("OOMKilled") is False and
                row["RestartCount"] == 0 and row["State"]["StartedAt"] == self.binding["serverStartedAt"], "live-start-or-restart")
        images = strict_json(self.command(["docker", "image", "inspect", self.binding["imageDigest"]]))
        require(len(images) == 1 and images[0]["Id"] == self.binding["imageDigest"] and
                images[0]["Config"]["Labels"].get("org.opencontainers.image.revision") == self.binding["targetSha"], "image-revision")
        return row["Id"]

    def proxy(self, path):
        raw = self.command(["curl", "--noproxy", "*", "--connect-timeout", "3", "--max-time", "10",
                            "--resolve", "codesamplex.dev:443:127.0.0.1", "-sS", "-w", "\n%{http_code}",
                            "https://codesamplex.dev" + path], 12)
        body, sep, status = raw.rpartition("\n")
        require(sep and status == "200", "proxy-smoke")
        return body

    def terminal_and_cleanup(self):
        # Collected transient units may have LoadState=not-found; `show`
        # still returns these properties successfully. Unknown/empty fails.
        unit = self.command(["systemctl", "show", "csx-migration-" + self.binding["owner"] + ".service",
                             "--property=ActiveState,SubState,MainPID,ControlPID"])
        properties = dict(line.split("=", 1) for line in unit.splitlines())
        require(properties.get("ActiveState") == "inactive" and properties.get("SubState") == "dead" and
                properties.get("MainPID") == "0" and properties.get("ControlPID") == "0", "supervisor-not-terminal")
        for args in (["docker", "ps", "-aq", "--filter", "name=^/csx-migrate-" + self.binding["owner"] + "$"],
                     ["docker", "ps", "-aq", "--filter", "label=codesamplex.deploy-owner=" + self.binding["owner"]]):
            require(not self.command(args).strip(), "helper-remains")
        cleanup = self.query("SELECT json_build_object('owned', (SELECT count(*) FROM pg_stat_activity WHERE "
                             "application_name='csx-migrate-" + self.binding["owner"] + "'),"
                             "'ddl',(SELECT count(*) FROM pg_stat_progress_create_index))")
        require(cleanup == {"owned": 0, "ddl": 0}, "database-helper-remains")

    def live(self):
        self.terminal_and_cleanup()
        original_id = self.identity()
        ledger = self.query("SELECT json_build_object('version',max(version),'count',count(*)) FROM schema_migrations")
        require(ledger == self.binding["migrationLedger"], "live-ledger")
        expected = MIGRATIONS[ledger["version"]][1]
        names = ",".join("'" + name + "'" for name in expected)
        indexes = self.query("SELECT COALESCE(json_agg(json_build_object('name',c.relname,'valid',i.indisvalid,"
                             "'ready',i.indisready,'definition',pg_get_indexdef(c.oid))), '[]'::json) "
                             "FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid JOIN pg_namespace n "
                             "ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relname IN (" + names + ")")
        verify_indexes(indexes, expected)
        for path in ("healthz", "version"):
            body = self.command(["docker", "compose", "exec", "-T", "server", "wget", "-q", "-T", "5",
                                 "-t", "1", "-O-", "http://127.0.0.1:8080/" + path])
            require(body.strip() == "ok" if path == "healthz" else
                    strict_json(body).get("revision") == self.binding["targetSha"], "loopback-" + path)
        tag = self.command(["docker", "exec", "codesamplex-server-1", "cat", "/data/dist/.release-tag"])
        require(tag.strip() == self.binding["releaseTag"], "live-release")
        require(self.proxy("/healthz").strip() == "ok", "proxy-health")
        require(strict_json(self.proxy("/version")).get("revision") == self.binding["targetSha"], "proxy-revision")
        require('<link rel="canonical" href="https://codesamplex.dev/features">' in self.proxy("/features"), "features-identity")
        # Fresh DB-backed user routes must pass too; no cache freshness claim.
        require(isinstance(strict_json(self.proxy("/v1/stats")), dict), "stats-smoke")
        self.proxy("/samples")
        require(self.identity() == original_id, "container-changed-during-verification")
        return original_id

    @staticmethod
    def sync_directory(path):
        fd = os.open(path, os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(fd)
        finally:
            os.close(fd)

    def sync_receipt(self, directory):
        path = directory / "reconciliation.json"
        self.regular(path)
        # O_RDWR permits fsync on Windows contract fixtures too; no bytes are
        # changed, and production checks a regular, singly-linked owner file.
        fd = os.open(path, os.O_RDWR | os.O_NOFOLLOW)
        try:
            os.fsync(fd)
        finally:
            os.close(fd)
        self.sync_directory(directory)

    def release(self, state, receipt):
        prepared = self.request["preparedArtifact"]
        self.prepared(prepared)
        if receipt is not None:
            # Controller must authenticate this historical prepared artifact
            # before sending it back. A new unverified proof cannot replace it.
            require(receipt["preparedArtifact"] == prepared, "receipt-prepared-mismatch")
        else:
            require(prepared["runId"] == self.request["reconciliationRunId"] and
                    prepared["runAttempt"] == self.request["reconciliationRunAttempt"], "prepared-run-mismatch")
            receipt = {"schemaVersion": 1, "binding": self.binding, "preparedArtifact": prepared,
                       "reconciliationRunId": self.request["reconciliationRunId"],
                       "reconciliationRunAttempt": self.request["reconciliationRunAttempt"],
                       "operationalSha": self.request["operationalSha"], "verifiedAt": utc()}
            raw = (json.dumps(receipt, sort_keys=True, separators=(",", ":")) + "\n").encode()
            # Exclusive creation fails closed on a foreign/pre-existing file;
            # a partial write is retained for inspection, never overwritten.
            fd = os.open(self.lock / "reconciliation.json", os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
            with os.fdopen(fd, "wb") as file:
                file.write(raw)
                file.flush()
                os.fsync(file.fileno())
            self.directory(self.lock, receipt_created=True)
            require(self.regular(self.lock / "reconciliation.json") == raw, "receipt-write-changed")
        # Re-establish durability on retries too: a readable complete receipt
        # does not prove that the previous controller reached its fsync.
        self.sync_receipt(self.lock if state == "owned" else self.archive)
        require(self.ownership() == (state, receipt), "owner-changed-before-release")
        # Receipt creation/fsync must not conceal replacement of any retained
        # namespace or original owner file before the atomic archive operation.
        self.stable()
        if state == "owned":
            # No deployment command can run concurrently: caller holds the
            # existing command flock throughout verification and this rename.
            require(not self.archive.exists(), "archive-collision")
            os.rename(self.lock, self.archive)
        # An archived retry may be recovering a loss immediately after rename.
        self.sync_directory(self.root)
        return receipt

    def run(self):
        self.retained()
        state, receipt = self.ownership()
        original_id = self.live()
        self.retained()
        # The systemd supervisor and PostgreSQL work are independent of the
        # controller command flock. Refresh these gates after the slower probes.
        self.terminal_and_cleanup()
        require(self.identity() == original_id, "container-changed-before-release")
        require(self.ownership() == (state, receipt), "owner-changed-during-verification")
        self.stable()
        if self.request["mode"] == "release":
            receipt = self.release(state, receipt)
            state = "archived"
        return {"schemaVersion": 1, "verifiedAt": utc(), "binding": self.binding,
                "lockState": state, "health": "ok", "smoke": "pass", "cleanup": "pass", "receipt": receipt}


if __name__ == "__main__":
    try:
        request = strict_json(base64.b64decode(sys.argv[1], validate=True))
        print(json.dumps(Host(request).run(), sort_keys=True))
    except Exception as error:
        # Never publish raw Docker output/configuration, SQL, or SSH errors.
        stage = str(error) if isinstance(error, Refusal) else "invalid-or-unavailable-evidence"
        print("CSX-RECONCILE-REFUSED " + stage, file=sys.stderr)
        sys.exit(1)
