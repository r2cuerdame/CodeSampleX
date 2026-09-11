#!/usr/bin/env python3
"""Read-only reconciliation of an exact, already committed deployment.

The caller binds EXPECTED to an immutable failed GitHub deployment artifact.
This program never executes retained supervisor code, updates its evidence,
restarts services, runs migrations, or releases/adopts the original owner lock.
A success is fresh acceptance evidence; the retained lock still fences deploys.
"""
import base64
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import signal
import sys
import time

# Standalone execution must not write a sibling __pycache__ on the host.
sys.dont_write_bytecode = True

# SSH may preload the reviewed sibling source in memory to avoid host writes.
migration = sys.modules.get("csx_reconciliation_migration")
if migration is None:
    spec = importlib.util.spec_from_file_location(
        "csx_reconciliation_migration", Path(__file__).with_name("offline-migration.py"))
    migration = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(migration)

CONFIG_FIELDS = ("operationalSha", "targetSha", "previousSha", "imageDigest",
                 "previousImageDigest", "expectedReleaseTag", "expectedMigration",
                 "expectedMigrationCount", "migrationTimeoutSeconds")
IDENTITY_FIELDS = ("owner", *CONFIG_FIELDS, "serverStartedAt", "evidenceSha256")
DIGEST = re.compile(r"^[0-9a-f]{64}$")
STARTED = re.compile(r"^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?(?:Z|\+00:00)$")
BUDGET_SECONDS = 180


class Refusal(RuntimeError):
    """Only fixed, reviewed codes from this module may cross the SSH boundary."""


def require(condition, code):
    if not condition:
        raise Refusal(code)


def exact_json(raw):
    def pairs(rows):
        value = {}
        for key, item in rows:
            require(key not in value, "duplicate-json-property")
            value[key] = item
        return value
    return json.loads(raw, object_pairs_hook=pairs,
                      parse_constant=lambda _: (_ for _ in ()).throw(Refusal("invalid-json-number")))


def validate_expected(expected):
    require(type(expected) is dict and type(expected.get("schemaVersion")) is int
            and expected["schemaVersion"] == 1, "invalid-expected-schema")
    for key, pattern in (("owner", migration.HEX32), ("operationalSha", migration.SHA),
                         ("targetSha", migration.SHA), ("previousSha", migration.SHA),
                         ("imageDigest", migration.IMAGE), ("previousImageDigest", migration.IMAGE),
                         ("expectedReleaseTag", migration.RELEASE),
                         ("serverStartedAt", STARTED), ("evidenceSha256", DIGEST)):
        value = expected.get(key)
        require(type(value) is str and pattern.fullmatch(value), "invalid-expected-" + key)
    require(expected["targetSha"] != expected["previousSha"], "same-target-and-previous")
    version = expected.get("expectedMigration")
    require(type(version) is str and version in migration.REVIEWED_MIGRATIONS,
            "unsupported-migration")
    require(type(expected.get("expectedMigrationCount")) is int and
            expected["expectedMigrationCount"] == migration.REVIEWED_MIGRATIONS[version]["count"],
            "invalid-expected-migration-count")
    budget = expected.get("migrationTimeoutSeconds")
    require(type(budget) is int and 60 <= budget <= 1800, "invalid-migration-budget")


def metadata(info):
    return (info.st_dev, info.st_ino, info.st_mode, info.st_uid, info.st_gid,
            info.st_size, info.st_mtime_ns, info.st_ctime_ns)


def verify_permissions(info, private_group_directory=False):
    require(info.st_uid in (0, os.geteuid()), "untrusted-retained-owner")
    require(not info.st_mode & 0o002, "writable-retained-state")
    if not info.st_mode & 0o020:
        return
    # Canonical mkdir inherits Ubuntu's 002 umask for deploy/. Its owner-private
    # primary group is no additional writer. This exception applies ONLY to
    # deploy/ itself, never its parent, owner lock, migration state or files.
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


def snapshot(path, directory=False, private_group_directory=False):
    """Reject links/special files and detect replacement during the read."""
    info = path.lstat()
    require(not stat.S_ISLNK(info.st_mode), "unsafe-retained-path")
    require(stat.S_ISDIR(info.st_mode) if directory else stat.S_ISREG(info.st_mode),
            "unsafe-retained-file-type")
    if os.name == "posix":
        verify_permissions(info, private_group_directory)
    if directory:
        return metadata(info), None
    require(info.st_size <= 262144, "oversized-retained-evidence")
    descriptor = os.open(path, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0))
    with os.fdopen(descriptor, "rb") as stream:
        require(metadata(os.fstat(stream.fileno())) == metadata(info), "retained-state-raced")
        raw = stream.read(262145)
        require(len(raw) <= 262144, "oversized-retained-evidence")
        require(metadata(os.fstat(stream.fileno())) == metadata(info), "retained-state-raced")
    require(metadata(path.lstat()) == metadata(info), "retained-state-raced")
    return metadata(info), raw


def verify_indexes(rows, expected_migration):
    target = migration.REVIEWED_MIGRATIONS[expected_migration]["indexes"]
    require(type(rows) is list and len(rows) == len(target), "required-indexes-missing")
    require(all(type(row) is dict for row in rows), "malformed-index-evidence")
    require({row.get("name") for row in rows} == set(target), "required-indexes-missing")
    for row in rows:
        definition = row.get("definition")
        require(type(definition) is str and row.get("valid") is True and row.get("ready") is True,
                "invalid-required-index")
        normalized = " ".join(definition.replace("public.", "").split())
        require(normalized == target[row["name"]], "wrong-required-index-definition")


class ReadOnlyHost(migration.Host):
    def __init__(self, expected, root=migration.ROOT, proc_root=Path("/proc")):
        # Do not invoke Host.__init__ or load executable code from retained state.
        self.owner = expected["owner"]
        self.config = {key: expected[key] for key in CONFIG_FIELDS}
        self.expected = expected
        self.root = Path(root)
        self.state = self.root / (".migration-" + self.owner)
        self.helper = "csx-migrate-" + self.owner
        self.application = self.helper
        self.unit = "csx-migration-" + self.owner + ".service"
        self.lock = self.root.parent / ".deploy-lock"
        self.proc_root = proc_root
        self.operation_deadline = time.monotonic() + BUDGET_SECONDS
        self.snapshots = {}

    def command(self, args, seconds=30, check=True, environment=None):
        seconds = min(seconds, self.operation_deadline - time.monotonic())
        require(seconds > 0, "reconciliation-deadline-exceeded")
        # Never let Docker/systemctl consume the streamed Python program.
        process = subprocess.Popen(args, cwd=self.root, text=True,
                                   stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                                   stderr=subprocess.PIPE, start_new_session=True, env=environment)
        try:
            stdout, stderr = process.communicate(timeout=seconds)
        except BaseException:
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            process.wait(timeout=5)
            raise
        require(not check or process.returncode == 0, "read-only-command-failed")
        return subprocess.CompletedProcess(args, process.returncode, stdout, stderr)

    def save(self, **_values):
        raise Refusal("read-only-reconciliation-cannot-save")

    def read_retained(self):
        require(self.root.absolute() == self.root.resolve(), "unsafe-deploy-directory")
        paths = ((self.root.parent, True), (self.root, True), (self.state, True),
                 (self.lock, True), (self.lock / "owner", False),
                 (self.state / "config.json", False), (self.state / "evidence.json", False))
        self.snapshots = {path: snapshot(path, directory, private_group_directory=path == self.root)
                          for path, directory in paths}
        require({path.name for path in self.lock.iterdir()} == {"owner"}, "unexpected-lock-contents")
        owner_raw = self.snapshots[self.lock / "owner"][1]
        require(owner_raw.decode("utf-8").strip() == self.owner, "owner-mismatch")
        config_raw = self.snapshots[self.state / "config.json"][1]
        config = exact_json(config_raw)
        require(type(config) is dict, "invalid-config")
        require(all(type(config.get(key)) is type(value) and config.get(key) == value
                    for key, value in self.config.items()), "retained-config-mismatch")
        raw = self.snapshots[self.state / "evidence.json"][1]
        require(hashlib.sha256(raw).hexdigest() == self.expected["evidenceSha256"],
                "retained-evidence-hash-mismatch")
        self.evidence = exact_json(raw)
        require(type(self.evidence) is dict, "invalid-retained-evidence")
        expected_evidence = {
            "schemaVersion": 1, "owner": self.owner, "unit": self.unit,
            "operationalSha": self.config["operationalSha"], "targetSha": self.config["targetSha"],
            "imageDigest": self.config["imageDigest"],
            "migrationTimeoutSeconds": self.config["migrationTimeoutSeconds"],
            "phase": "committed", "conclusion": "success", "acceptanceAuthority": "host",
            "controllerSmoke": "host-verified", "health": "ok", "proxyHealth": "ok",
            "smoke": "pass", "representativeSmoke": "pass", "cleanup": "pass",
            "migrationVerification": "pass", "servedRevision": self.config["targetSha"],
            "releaseTag": self.config["expectedReleaseTag"],
            "serverStartedAt": self.expected["serverStartedAt"],
        }
        require(all(type(self.evidence.get(key)) is type(value) and self.evidence.get(key) == value
                    for key, value in expected_evidence.items()), "retained-acceptance-mismatch")
        require(self.evidence.get("migrationLedger") == self.ledger(), "retained-ledger-mismatch")
        verify_indexes(self.evidence.get("indexes"), self.config["expectedMigration"])
        require(self.evidence.get("rollback") == "not-started" and
                not any(key in self.evidence for key in ("rollbackFailures", "rollbackServerCleanup")),
                "retained-rollback-evidence")
        return {"retainedEvidenceSha256": hashlib.sha256(raw).hexdigest(),
                "retainedConfigSha256": hashlib.sha256(config_raw).hexdigest()}

    def stable_retained(self):
        for path, original in self.snapshots.items():
            require(snapshot(path, directory=original[1] is None,
                             private_group_directory=path == self.root) == original,
                    "retained-state-changed")
        require({path.name for path in self.lock.iterdir()} == {"owner"}, "unexpected-lock-contents")

    def terminal_supervisor(self):
        properties = ("Id", "LoadState", "ActiveState", "SubState", "MainPID", "ControlPID", "ControlGroup")
        result = self.command(["systemctl", "show", self.unit, "--no-pager",
                               "--property=" + ",".join(properties)], seconds=10, check=False)
        rows = {}
        for line in result.stdout.splitlines():
            key, separator, value = line.partition("=")
            require(separator and key in properties and key not in rows, "invalid-supervisor-state")
            rows[key] = value
        require(set(rows) == set(properties) and rows["Id"] == self.unit, "missing-supervisor-state")
        # --collect may unload the finished transient unit. Missing output is
        # never enough; require its explicit dead state and scan live processes.
        require(result.returncode in (0, 1) and rows["LoadState"] in ("loaded", "not-found") and
                rows["ActiveState"] == "inactive" and rows["SubState"] == "dead" and
                rows["MainPID"] == "0" and rows["ControlPID"] == "0",
                "supervisor-not-terminal")
        require(rows["ControlGroup"] in ("", "/system.slice/" + self.unit), "unexpected-supervisor-cgroup")
        require(self.proc_root.is_dir(), "process-inventory-unavailable")
        retained_program = str(self.state / "offline-migration.py").encode()
        for entry in self.proc_root.iterdir():
            if not entry.name.isdecimal():
                continue
            try:
                argv = (entry / "cmdline").read_bytes().split(b"\0")
                groups = (entry / "cgroup").read_text(encoding="utf-8")
            except (FileNotFoundError, ProcessLookupError):
                continue
            require(retained_program not in argv, "supervisor-process-active")
            require(not any(self.unit in line.split(":")[-1].split("/")
                            for line in groups.splitlines()), "supervisor-cgroup-active")
        return rows

    def ledger(self):
        return {"version": self.config["expectedMigration"],
                "count": self.config["expectedMigrationCount"]}

    def query(self, sql):
        # All statements are fixed SELECTs. PostgreSQL additionally enforces
        # read-only transactions; do not reuse verify_migration's repair UPDATE.
        require(sql.lstrip().upper().startswith("SELECT "), "non-read-only-query")
        result = self.docker("compose", "exec", "-T",
                             "-e", "PGAPPNAME=csx-reconcile-" + self.owner,
                             "-e", "PGOPTIONS=-c default_transaction_read_only=on -c statement_timeout=10000 -c lock_timeout=5000",
                             "db", "psql", "-X", "-U", "csx", "-d", "csx",
                             "-v", "ON_ERROR_STOP=1", "-Atqc", sql, seconds=20)
        return exact_json(result.stdout.strip())

    def verify_database(self):
        ledger = self.query("SELECT json_build_object('version',max(version),'count',count(*)) FROM schema_migrations")
        require(ledger == self.ledger() and type(ledger.get("count")) is int, "fresh-ledger-mismatch")
        names = ",".join("'" + name + "'" for name in migration.REVIEWED_MIGRATIONS[self.config["expectedMigration"]]["indexes"])
        indexes = self.query("""SELECT COALESCE(json_agg(json_build_object('name',c.relname,
            'valid',i.indisvalid,'ready',i.indisready,'definition',pg_get_indexdef(c.oid))), '[]'::json)
            FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid
            JOIN pg_namespace n ON n.oid=c.relnamespace
            WHERE n.nspname='public' AND c.relname IN (""" + names + ")")
        verify_indexes(indexes, self.config["expectedMigration"])
        invalid = self.query("""SELECT count(*) FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid
            JOIN pg_namespace n ON n.oid=c.relnamespace
            WHERE n.nspname='public' AND (NOT i.indisvalid OR NOT i.indisready)""")
        require(type(invalid) is int and invalid == 0, "invalid-index-remains")
        self.verify_cleanup()
        return {"migrationLedger": ledger, "indexes": indexes, "cleanup": "pass"}

    def verify_cleanup(self):
        for selector in ("name=^/" + self.helper + "$", "label=codesamplex.deploy-owner=" + self.owner):
            require(not self.docker("ps", "-aq", "--filter", selector, seconds=10).stdout.strip(),
                    "owned-helper-remains")
        backends = self.evidence.get("backends")
        require(type(backends) is list and len(backends) <= 100, "invalid-retained-backends")
        clauses = ["application_name='" + self.application + "'"]
        for row in backends:
            require(type(row) is dict and type(row.get("pid")) is int and row["pid"] > 0 and
                    type(row.get("backendStart")) is str and len(row["backendStart"]) <= 80 and
                    row.get("applicationName") == self.application and row.get("userName") == "csx",
                    "invalid-retained-backend-identity")
            stamp = row["backendStart"].replace("'", "''")
            clauses.append("(pid=" + str(row["pid"]) + " AND backend_start='" + stamp + "'::timestamptz)")
        count = self.query("SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND (" +
                           " OR ".join(clauses) + ")")
        require(type(count) is int and count == 0, "owned-backend-remains")
        progress = self.query("SELECT count(*) FROM pg_stat_progress_create_index")
        require(type(progress) is int and progress == 0, "index-ddl-remains")

    def identity(self):
        container = self.verify_image("codesamplex-server-1", self.config["imageDigest"], self.config["targetSha"])
        state = container["State"]
        require(state.get("Running") is True and state.get("OOMKilled") is False and
                state.get("Paused") is False and state.get("Restarting") is False,
                "server-not-running")
        require(type(container.get("RestartCount")) is int and container["RestartCount"] == 0,
                "server-restarted")
        require(state.get("StartedAt") == self.expected["serverStartedAt"], "server-start-changed")
        versions = [item[len("CSX_VERSION="):] for item in container["Config"].get("Env", [])
                    if item.startswith("CSX_VERSION=")]
        require(versions == [self.config["targetSha"]], "configured-revision-mismatch")
        release = self.docker("exec", "codesamplex-server-1", "cat", "/data/dist/.release-tag", seconds=10).stdout.strip()
        require(release == self.config["expectedReleaseTag"], "installer-release-mismatch")
        return {"imageDigest": container["Image"], "configuredRevision": versions[0],
                "serverStartedAt": state["StartedAt"], "releaseTag": release,
                "restartCount": container["RestartCount"]}

    def probe(self):
        health = self.docker("compose", "exec", "-T", "server", "wget", "-q", "-T", "5", "-t", "1", "-O-",
                             "http://127.0.0.1:8080/healthz", seconds=10).stdout
        require(health.strip() == "ok", "fresh-health-failed")
        version = exact_json(self.docker("compose", "exec", "-T", "server", "wget", "-q", "-T", "5", "-t", "1", "-O-",
                                        "http://127.0.0.1:8080/version", seconds=10).stdout)
        require(type(version) is dict and version.get("revision") == self.config["targetSha"],
                "served-revision-mismatch")
        require(self.representative("/healthz").strip() == "ok", "fresh-proxy-health-failed")
        features = self.representative("/features")
        require('<link rel="canonical" href="https://' + migration.CANONICAL_DOMAIN + '/features">' in features,
                "fresh-features-smoke-failed")
        routed = exact_json(self.representative("/version"))
        require(type(routed) is dict and routed.get("revision") == self.config["targetSha"],
                "proxy-revision-mismatch")
        return {"servedRevision": version["revision"], "health": "ok", "proxyHealth": "ok",
                "smoke": "pass", "representativeSmoke": "pass"}


def collect(expected, root=migration.ROOT, host_factory=ReadOnlyHost):
    report = {"schemaVersion": 1, "conclusion": "failure", "lockDisposition": "unverified",
              "acceptanceAuthority": "host-reconciliation", "checks": {},
              "startedAt": migration.utc(), "budgetSeconds": BUDGET_SECONDS}
    stage = "expected"
    try:
        validate_expected(expected)
        report.update({key: expected[key] for key in IDENTITY_FIELDS})
        host = host_factory(expected, root)
        for stage, action in (("retained", host.read_retained),
                              ("supervisor-before", lambda: {"supervisorBefore": host.terminal_supervisor()}),
                              ("identity-before", lambda: {"identityBefore": host.identity()}),
                              ("database", host.verify_database), ("health-smoke", host.probe),
                              ("identity-after", lambda: {"identityAfter": host.identity()}),
                              ("cleanup-after", host.verify_cleanup),
                              ("supervisor-after", lambda: {"supervisorAfter": host.terminal_supervisor()}),
                              ("retained-stable", host.stable_retained)):
            values = action()
            if values:
                report.update(values)
            report["checks"][stage] = "pass"
            require(time.monotonic() < host.operation_deadline, "reconciliation-deadline-exceeded")
        report.update(conclusion="success", lockDisposition="retained")
    except Exception as exc:
        report.update(failureStage=stage,
                      failureCode=str(exc) if type(exc) is Refusal else "evidence-invalid-or-unavailable")
    report["completedAt"] = migration.utc()
    return report


def main(argv):
    try:
        require(len(argv) == 3 and argv[1] == "--expected-base64", "invalid-arguments")
        expected = exact_json(base64.b64decode(argv[2], validate=True))
    except Exception:
        expected = None
    result = collect(expected)
    print(json.dumps(result, separators=(",", ":")))
    return 0 if result["conclusion"] == "success" else 1


if __name__ == "__main__":
    sys.exit(main(sys.argv))
