#!/usr/bin/env python3
"""Host-owned migration/activation lease for the canonical production deploy.

The transient systemd unit owns the complete interval from stopping the old
builder to the controller's final smoke acknowledgement. ExecStopPost calls
finalize even when the main process was killed. No credentials enter argv or
evidence; Compose obtains the existing DSN from the existing host environment.
"""
import datetime
import json
import ipaddress
import os
from pathlib import Path
import re
import signal
import subprocess
import sys
import time
from urllib.parse import parse_qsl, urlencode, urlsplit, urlunsplit

ROOT = Path("/opt/codesamplex/deploy")
HEX32 = re.compile(r"^[0-9a-f]{32}$")
SHA = re.compile(r"^[0-9a-f]{40}$")
IMAGE = re.compile(r"^sha256:[0-9a-f]{64}$")


def utc():
    return datetime.datetime.now(datetime.timezone.utc).isoformat()


def atomic_json(path, value):
    temp = path.with_suffix(".pending")
    temp.write_text(json.dumps(value, separators=(",", ":")) + "\n", encoding="utf-8")
    os.chmod(temp, 0o600)
    os.replace(temp, path)


def owned_dsn(dsn, application):
    # URI parsing mirrors pgx/libpq query semantics. Keyword DSNs need a
    # separately reviewed parser; do not silently fall back to PGAPPNAME.
    if not isinstance(dsn, str) or any(c in dsn for c in "\r\n"):
        raise RuntimeError("unsupported canonical database configuration")
    uri = urlsplit(dsn)
    if uri.scheme not in ("postgres", "postgresql") or not uri.hostname or uri.fragment:
        raise RuntimeError("offline migration requires a PostgreSQL URI")
    options = [(k, v) for k, v in parse_qsl(uri.query, keep_blank_values=True)
               if k != "application_name"]
    options.append(("application_name", application))
    return urlunsplit((uri.scheme, uri.netloc, uri.path, urlencode(options), ""))


class Host:
    def __init__(self, owner, root=ROOT):
        if not HEX32.fullmatch(owner):
            raise ValueError("invalid deploy owner")
        self.owner = owner
        self.operation_deadline = None
        self.root = root
        self.state = root / (".migration-" + owner)
        if self.state.is_symlink() or self.state.resolve().parent != root.resolve():
            raise ValueError("invalid migration state directory")
        self.config = json.loads((self.state / "config.json").read_text(encoding="utf-8"))
        for key in ("targetSha", "previousSha", "operationalSha"):
            if not SHA.fullmatch(self.config.get(key, "")):
                raise ValueError("invalid " + key)
        for key in ("imageDigest", "previousImageDigest"):
            if not IMAGE.fullmatch(self.config.get(key, "")):
                raise ValueError("invalid " + key)
        budget = self.config.get("migrationTimeoutSeconds")
        if type(budget) is not int or not 60 <= budget <= 1800:
            raise ValueError("migration budget must be 60..1800 seconds")
        if self.config.get("expectedMigration") != "0036_builder_projections.sql":
            raise ValueError("offline migration supports only reviewed migration 0036")
        self.helper = "csx-migrate-" + owner
        self.application = self.helper
        self.evidence_file = self.state / "evidence.json"
        self.evidence = {
            "schemaVersion": 1, "owner": owner,
            "unit": "csx-migration-" + owner + ".service",
            "operationalSha": self.config["operationalSha"],
            "targetSha": self.config["targetSha"],
            "imageDigest": self.config["imageDigest"],
            "migrationTimeoutSeconds": budget,
            "phase": "preflight", "conclusion": "pending",
            "startedAt": utc(), "backends": [], "cleanup": "not-started",
            "rollback": "not-started", "controllerSmoke": "not-acknowledged",
        }
        if self.evidence_file.exists():
            self.evidence = json.loads(self.evidence_file.read_text(encoding="utf-8"))

    def save(self, **values):
        self.evidence.update(values)
        atomic_json(self.evidence_file, self.evidence)

    def command(self, args, seconds=30, check=True, environment=None):
        # Never include stdout/stderr or argv in exception messages: Docker
        # inspection includes the DSN. Commands are arrays, never shell text.
        if self.operation_deadline is not None:
            seconds = min(seconds, self.operation_deadline - time.monotonic())
            if seconds <= 0:
                raise RuntimeError("host cleanup deadline exceeded")
        process = subprocess.Popen(args, cwd=self.root, text=True,
                                   stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                   start_new_session=True, env=environment)
        try:
            stdout, stderr = process.communicate(timeout=seconds)
        except BaseException:
            # A timeout or systemd TERM must not orphan a shell's Docker child.
            # PostgreSQL backends are separately cancelled by exact identity.
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            process.wait(timeout=5)
            raise
        result = subprocess.CompletedProcess(args, process.returncode, stdout, stderr)
        if check and result.returncode:
            raise RuntimeError("host operation failed: " + args[0] +
                               " (exit " + str(result.returncode) + ")")
        return result

    def docker(self, *args, seconds=30, check=True, environment=None):
        return self.command(["docker", *args], seconds, check, environment)

    def query(self, sql):
        result = self.docker("compose", "exec", "-T",
                             "-e", "PGAPPNAME=csx-ops-" + self.owner,
                             "-e", "PGOPTIONS=-c statement_timeout=10000 -c lock_timeout=5000",
                             "db", "psql", "-X", "-U", "csx", "-d", "csx",
                             "-v", "ON_ERROR_STOP=1", "-Atqc", sql, seconds=20)
        return json.loads(result.stdout.strip())

    def inspect(self, name):
        return json.loads(self.docker("inspect", name).stdout)[0]

    def check_lock(self):
        lock = self.root.parent / ".deploy-lock"
        if lock.is_symlink() or (lock / "owner").is_symlink():
            raise RuntimeError("unsafe deployment lock")
        if (lock / "owner").read_text().strip() != self.owner:
            raise RuntimeError("deployment lock ownership changed")

    def clients(self, owned_only=False):
        where = " AND application_name='" + self.application + "' AND usename='csx'" if owned_only else ""
        return self.query("""
            SELECT COALESCE(json_agg(json_build_object(
                'pid',pid,'backendStart',backend_start::text,
                'queryStart',query_start::text,'applicationName',application_name,'userName',usename,
                'clientAddress',client_addr::text,
                'queryHash',md5(query))), '[]'::json)
            FROM pg_stat_activity WHERE datname=current_database()
            AND backend_type='client backend' AND pid<>pg_backend_pid()""" + where)

    def remember_backends(self):
        rows = self.clients(True)
        known = {(r["pid"], r["backendStart"]) for r in self.evidence["backends"]}
        for row in rows:
            if (row["pid"], row["backendStart"]) not in known:
                self.evidence["backends"].append(row)
        self.save(lastBackendObservation=rows)
        return rows


    def verify_image(self, name, image, revision):
        container = self.inspect(name)
        if container["Image"] != image:
            raise RuntimeError("container image identity mismatch")
        built = json.loads(self.docker("image", "inspect", image).stdout)[0]
        if built["Config"]["Labels"].get("org.opencontainers.image.revision") != revision:
            raise RuntimeError("immutable image revision mismatch")

    def preflight(self):
        self.check_lock()
        # The original service must still be precisely the snapshotted rollback.
        self.verify_image("codesamplex-server-1", self.config["previousImageDigest"],
                          self.config["previousSha"])
        candidate = json.loads(self.docker("image", "inspect", self.config["imageDigest"]).stdout)[0]
        if candidate["Config"]["Labels"].get("org.opencontainers.image.revision") != self.config["targetSha"]:
            raise RuntimeError("migration image is not the immutable payload")
        running = self.docker("ps", "-q").stdout.split()
        for container_id in running:
            row = self.inspect(container_id)
            name = row["Name"].lstrip("/")
            if name == "codesamplex-server-1":
                continue
            env = row["Config"].get("Env") or []
            argv = (row["Config"].get("Entrypoint") or []) + (row["Config"].get("Cmd") or [])
            if any(v.startswith("CSX_DSN=") for v in env) or any("csx-server" in v for v in argv):
                raise RuntimeError("another possible builder/helper is running")
        if self.docker("ps", "-aq", "--filter", "name=^/" + self.helper + "$").stdout.strip():
            raise RuntimeError("migration helper name already exists")
        # Refuse an unresolved prior attempt; never invoke prestage's DROP fallback.
        invalid = self.query("""
            SELECT count(*) FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid
            JOIN pg_namespace n ON n.oid=c.relnamespace
            WHERE n.nspname='public' AND (NOT i.indisvalid OR NOT i.indisready)""")
        progress = self.query("SELECT count(*) FROM pg_stat_progress_create_index")
        if invalid or progress:
            raise RuntimeError("unresolved invalid index or active index DDL")
        resolved = json.loads(self.docker("compose", "config", "--format", "json").stdout)
        dsn = resolved["services"]["server"]["environment"]["CSX_DSN"]
        self.helper_environment = os.environ.copy()
        self.helper_environment["CSX_DSN"] = owned_dsn(dsn, self.application)
        self.helper_environment["PGAPPNAME"] = self.application
        self.save(preflight="pass", backendOwnership="explicit-dsn-application-name")


    def stop_builders(self):
        # Durable intent precedes the mutation, so SIGKILL/ExecStopPost cannot
        # mistake a stopped server for a read-only preflight failure.
        self.save(phase="quiescing", serverStopStarted=True,
                  originalServerNetwork=self.server_network(self.inspect("codesamplex-server-1")))
        self.docker("stop", "--time", "30", "codesamplex-server-1", seconds=45)
        deadline = time.monotonic() + 30
        while self.clients():
            if time.monotonic() >= deadline:
                raise RuntimeError("database clients remain after stopping the builder")
            time.sleep(1)
        self.save(quiescence="pass", quiescentAt=utc())

    def migrate(self):
        started = time.monotonic()
        self.save(phase="migrating", migrationStartedAt=utc())
        self.operation_deadline = started + self.config["migrationTimeoutSeconds"]
        # Fixed unique application_name identifies the PostgreSQL session even
        # if Docker exits while its DDL backend survives the socket disconnect.
        self.docker("compose", "run", "--detach", "--no-deps", "--name", self.helper,
                    "--label", "codesamplex.deploy-owner=" + self.owner,
                    "-e", "CSX_DSN", "-e", "PGAPPNAME",
                    "-e", "PGOPTIONS=-c lock_timeout=5000",
                    "--entrypoint", "csx-server", "server", "migrate", seconds=45,
                    environment=self.helper_environment)
        # Inspect only in memory. Neither the URI nor any container environment
        # can reach command arguments, journal messages or release evidence.
        actual = dict(v.split("=", 1) for v in self.inspect(self.helper)["Config"]["Env"] if "=" in v)
        if actual.get("CSX_DSN") != self.helper_environment["CSX_DSN"]:
            raise RuntimeError("helper database identity override was not applied")
        self.helper_environment.clear()
        self.verify_image(self.helper, self.config["imageDigest"], self.config["targetSha"])
        deadline = started + self.config["migrationTimeoutSeconds"]
        while True:
            self.remember_backends()
            self.save(migrationElapsedSeconds=round(time.monotonic() - started, 3))
            status = self.inspect(self.helper)["State"]
            if not status["Running"]:
                if status["ExitCode"] != 0 or status.get("OOMKilled"):
                    raise RuntimeError("migration helper failed")
                break
            if time.monotonic() >= deadline:
                raise RuntimeError("offline migration deadline exceeded")
            time.sleep(2)
        self.save(migrationElapsedSeconds=round(time.monotonic() - started, 3),
                  migrationCompletedAt=utc())
        self.operation_deadline = None
        self.cleanup_helper()

    def cleanup_helper(self):
        if self.operation_deadline is None:
            self.operation_deadline = time.monotonic() + 90
        self.save(cleanup="running")
        names = self.docker("ps", "-aq", "--filter", "name=^/" + self.helper + "$").stdout.split()
        if len(names) > 1:
            raise RuntimeError("ambiguous helper ownership")
        if names:
            row = self.inspect(self.helper)
            if row["Config"].get("Labels", {}).get("codesamplex.deploy-owner") != self.owner:
                raise RuntimeError("helper ownership mismatch")
            if row["Image"] != self.config["imageDigest"]:
                raise RuntimeError("helper image changed")
            self.docker("stop", "--time", "10", self.helper, seconds=20)
        # Docker stop was insufficient in the actual #174 incident. Reidentify
        # exact app+PID+backend_start tuples, cancel, then terminate if needed.
        rows = self.remember_backends()
        for row in rows:
            self.backend_signal(row, terminate=False)
        deadline = time.monotonic() + 5
        while self.clients(True) and time.monotonic() < deadline:
            time.sleep(0.25)
        for row in self.remember_backends():
            self.backend_signal(row, terminate=True)
        deadline = time.monotonic() + 10
        while self.clients(True):
            if time.monotonic() >= deadline:
                raise RuntimeError("owned PostgreSQL backend survived termination")
            time.sleep(0.25)
        if names:
            self.docker("rm", self.helper)
        if self.docker("ps", "-aq", "--filter", "name=^/" + self.helper + "$").stdout.strip():
            raise RuntimeError("owned helper survived cleanup")
        # With the old builder stopped, no DDL should outlive the owned helper.
        if self.query("SELECT count(*) FROM pg_stat_progress_create_index"):
            raise RuntimeError("index DDL remains after helper cleanup")
        if self.evidence.get("quiescence") == "pass" and self.clients():
            raise RuntimeError("unowned database clients remain after helper cleanup")
        self.save(cleanup="pass", cleanupCompletedAt=utc())
        self.operation_deadline = None

    def backend_signal(self, row, terminate, server=False):
        if type(row["pid"]) is not int or (not server and row["applicationName"] != self.application):
            raise RuntimeError("invalid backend ownership evidence")
        if server and row not in self.evidence.get("rollbackServerBackends", []):
            raise RuntimeError("unrecorded rollback server backend")
        # Timestamps originate in PostgreSQL but still quote them as data.
        stamp = row["backendStart"].replace("'", "''")
        fn = "pg_terminate_backend" if terminate else "pg_cancel_backend"
        self.query("SELECT COALESCE(json_agg(" + fn + "(pid)), '[]'::json) "
                   "FROM pg_stat_activity WHERE pid=" + str(row["pid"]) +
                   " AND backend_start='" + stamp + "'::timestamptz"
                   " AND application_name='" + row["applicationName"].replace("'", "''") + "'"
                   " AND datname=current_database() AND usename='csx' AND backend_type='client backend'")

    def verify_migration(self):
        schema = self.query("""
            SELECT json_build_object('version',max(version),'count',count(*))
            FROM schema_migrations""")
        if schema != {"version": "0036_builder_projections.sql", "count": 37}:
            raise RuntimeError("migration ledger does not match the target")
        # Full-table projection/hash/source audits are independent observation.
        # The existing transactional migration applies the backfill itself.
        self.save(migrationLedger=schema, migrationVerification="pass")

    def activate(self):
        self.save(phase="activating", serverActivationStarted=True)
        # The new image's startup migration is now a bounded no-op. The old
        # binary never runs between backfill and the new builder's full repair.
        self.docker("compose", "up", "-d", "--no-build", "--force-recreate", "server", seconds=60)
        deadline = time.monotonic() + 120
        while True:
            result = self.docker("compose", "exec", "-T", "server", "wget", "-q", "-T", "5", "-t", "1", "-O-",
                                 "http://127.0.0.1:8080/healthz", seconds=10, check=False)
            if result.returncode == 0 and result.stdout.strip() == "ok":
                break
            if time.monotonic() >= deadline:
                raise RuntimeError("target health deadline exceeded")
            time.sleep(5)
        self.verify_image("codesamplex-server-1", self.config["imageDigest"], self.config["targetSha"])
        served = json.loads(self.docker("compose", "exec", "-T", "server", "wget", "-q", "-T", "5", "-t", "1", "-O-",
                                      "http://127.0.0.1:8080/version", seconds=10).stdout)
        if served.get("revision") != self.config["targetSha"]:
            raise RuntimeError("target process serves the wrong revision")
        self.save(phase="candidate-ready", candidateReadyAt=utc())

    def await_commit(self):
        deadline = time.monotonic() + 600
        while True:
            ack = self.state / "commit.json"
            if ack.exists():
                value = json.loads(ack.read_text(encoding="utf-8"))
                expected = {k: self.evidence[k] for k in
                            ("owner", "operationalSha", "targetSha", "imageDigest")}
                if value != expected:
                    raise RuntimeError("controller acknowledgement identity mismatch")
                self.check_lock()
                self.verify_image("codesamplex-server-1", self.config["imageDigest"], self.config["targetSha"])
                self.save(phase="committed", conclusion="success",
                          controllerSmoke="acknowledged", completedAt=utc())
                return
            if time.monotonic() >= deadline:
                raise RuntimeError("controller smoke acknowledgement deadline exceeded")
            time.sleep(1)

    def run(self):
        self.preflight()
        self.stop_builders()
        self.migrate()
        self.verify_migration()
        self.activate()
        self.await_commit()

    def server_network(self, container):
        addresses = []
        for network in container.get("NetworkSettings", {}).get("Networks", {}).values():
            for key in ("IPAddress", "GlobalIPv6Address"):
                value = network.get(key)
                if value:
                    addresses.append(str(ipaddress.ip_address(value)))
        return {"addresses": addresses, "startedAt": container["State"]["StartedAt"]}

    def stop_server_for_rollback(self):
        # The candidate may have its own startup/builder backend. Capture it by
        # exact container image + network + lifetime before Docker stop, then
        # cancel only the recorded PID/backend_start/application tuples.
        result = self.docker("inspect", "codesamplex-server-1", check=False)
        networks = [self.evidence.get("originalServerNetwork", {})]
        if result.returncode == 0:
            container = json.loads(result.stdout)[0]
            image = container["Image"]
            if image not in (self.config["previousImageDigest"], self.config["imageDigest"]):
                raise RuntimeError("server image changed before rollback cleanup")
            revision = (self.config["targetSha"] if image == self.config["imageDigest"]
                        else self.config["previousSha"])
            self.verify_image("codesamplex-server-1", image, revision)
            networks.append(self.server_network(container))
        def capture():
            owned = []
            for network in networks:
                addresses = network.get("addresses") or []
                if not addresses:
                    continue
                # Addresses and timestamp originate in the inspected container.
                literals = ",".join("'" + str(ipaddress.ip_address(a)) + "'" for a in addresses)
                since = network["startedAt"].replace("'", "''")
                rows = self.query("""SELECT COALESCE(json_agg(json_build_object(
                    'pid',pid,'backendStart',backend_start::text,'queryStart',query_start::text,
                    'applicationName',application_name,'userName',usename,
                    'clientAddress',client_addr::text,'queryHash',md5(query))), '[]'::json)
                    FROM pg_stat_activity WHERE datname=current_database()
                    AND backend_type='client backend' AND usename='csx'
                    AND client_addr=ANY(ARRAY[""" + literals + "]::inet[])"
                    " AND backend_start>='" + since + "'::timestamptz")
                owned.extend(rows)
            known = self.evidence.get("rollbackServerBackends", [])
            for row in owned:
                if row not in known:
                    known.append(row)
            self.save(rollbackServerBackends=known)
            return owned
        capture()
        if result.returncode == 0:
            self.docker("stop", "--time", "10", "codesamplex-server-1", seconds=20)
        for row in capture():
            self.backend_signal(row, terminate=False, server=True)
        deadline = time.monotonic() + 5
        while capture() and time.monotonic() < deadline:
            time.sleep(0.25)
        for row in capture():
            self.backend_signal(row, terminate=True, server=True)
        deadline = time.monotonic() + 10
        while capture():
            if time.monotonic() >= deadline:
                raise RuntimeError("server PostgreSQL backend survived termination")
            time.sleep(0.25)
        self.save(rollbackServerCleanup="pass")

    def finalize(self):
        # ExecStopPost is idempotent and never resurrects the old builder after
        # a committed target. A pending/failed phase always has to clean DDL
        # before restoring the exact prior images/configuration.
        if self.evidence.get("phase") in ("committed", "rolled-back"):
            return
        self.check_lock()
        # 90 seconds cleanup + two 180-second artifact rollback budgets fit
        # inside systemd's separate 480-second stop/finalizer allowance.
        self.operation_deadline = time.monotonic() + 90
        self.stop_server_for_rollback()
        self.cleanup_helper()
        self.operation_deadline = None
        self.save(phase="rolling-back", conclusion="failure")
        errors = []
        for name in ("rollback-server.sh", "rollback-caddy.sh"):
            try:
                self.command(["sh", str(self.state / name)], seconds=180)
            except Exception:
                errors.append(name)
        if errors:
            self.save(phase="rollback-failed", rollback="failed", rollbackFailures=errors)
            raise RuntimeError("exact rollback failed")
        self.save(phase="rolled-back", rollback="succeeded", completedAt=utc())


def main(argv):
    if len(argv) != 3 or argv[1] not in ("run", "finalize"):
        raise ValueError("usage: offline-migration.py run|finalize <deploy-owner>")
    host = Host(argv[2])
    if Path(__file__).resolve().parent != host.state.resolve():
        raise ValueError("supervisor must run from its owned host state directory")
    def interrupted(_signum, _frame):
        raise RuntimeError("host supervisor interrupted")
    signal.signal(signal.SIGTERM, interrupted)
    signal.signal(signal.SIGINT, interrupted)
    try:
        if argv[1] == "run":
            host.run()
        else:
            host.finalize()
        return 0
    except Exception as exc:
        # Exception strings are intentionally fixed above; never emit subprocess
        # output, which can contain environment or data from the running service.
        safe = str(exc) if type(exc) is RuntimeError else type(exc).__name__
        host.save(failure=safe, conclusion="failure")
        return 1


if __name__ == "__main__":
    sys.exit(main(sys.argv))
