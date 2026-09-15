#!/usr/bin/env python3
"""Verify an authenticated retained deployment lock; archive it atomically.

The canonical controller streams this reviewed source under the same command
flock as deploy.ps1. No retained script is executed. Verification is read-only;
release writes a receipt and atomically renames the original lock directory.
"""
import base64
import datetime
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


def metadata(info):
    return (info.st_dev, info.st_ino, info.st_mode, info.st_uid, info.st_gid,
            info.st_nlink, info.st_size, info.st_mtime_ns, info.st_ctime_ns)


def verify_permissions(info):
    require(info.st_uid in (0, os.geteuid()), "untrusted-retained-owner")
    require(not info.st_mode & 0o002, "writable-retained-state")


class RecoverHost:
    REQUIRED_CONSECUTIVE_HEALTHCHECKS = 3
    HEALTH_OBSERVATION_ATTEMPTS = 6
    HEALTH_OBSERVATION_INTERVAL_SECONDS = 5
    DATABASE_VERIFICATION_COMMAND_TIMEOUT_SECONDS = 30

    def __init__(self, request, root=Path("/opt/codesamplex")):
        self.request = request
        self.root = root
        self.deploy = root / "deploy"
        self.lock = root / ".deploy-lock"
        self.deadline = time.monotonic() + 170
        self.validate_request()

    def validate_request(self):
        req = self.request
        require(req.get("mode") in ("verify", "release"), "invalid-mode")
        require(req.get("recoveryClass") in ("pre-activation-retained-lock",
                                              "pre-migration-rollback-failed-retained-lock"),
                "invalid-recoveryClass")
        require(matches(req.get("repository"), r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+"), "invalid-repository")
        for k in ("sourceRunId", "recoveryRunId"):
            require(matches(str(req.get(k)), r"[1-9][0-9]*"), "invalid-" + k)
        for k in ("sourceRunAttempt", "sourceArtifactId", "recoveryRunAttempt"):
            require(type(req.get(k)) is int and req[k] > 0, "invalid-" + k)
        for k in ("sourceArtifactSha256", "sourceEvidenceSha256"):
            require(matches(req.get(k), r"[0-9a-f]{64}"), "invalid-" + k)
        migration_digest = req.get("sourceMigrationEvidenceSha256")
        ledger = req.get("migrationLedgerBefore")
        expected_owner = req.get("expectedLockOwner")
        if req["recoveryClass"] == "pre-migration-rollback-failed-retained-lock":
            require(matches(migration_digest, r"[0-9a-f]{64}"), "invalid-sourceMigrationEvidenceSha256")
            require(isinstance(ledger, dict) and matches(ledger.get("version"), r"[0-9]{4}_[A-Za-z0-9_]+\.sql") and
                    type(ledger.get("count")) is int and ledger["count"] > 0,
                    "invalid-migrationLedgerBefore")
            require(matches(expected_owner, r"[0-9a-f]{32}"), "invalid-expectedLockOwner")
        else:
            require(migration_digest is None and ledger is None and expected_owner is None,
                    "unexpected-migration-baseline")
        for k in ("targetSha", "previousProductionSha", "operationalSha"):
            require(matches(req.get(k), r"[0-9a-f]{40}"), "invalid-" + k)
        require(matches(req.get("previousImageDigest"), r"sha256:[0-9a-f]{64}"), "invalid-previousImageDigest")
        require(req["targetSha"] != req["previousProductionSha"], "target-equals-previous")

    @staticmethod
    def sync_directory(path):
        if os.name != "posix":
            return
        fd = os.open(path, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0))
        try:
            os.fsync(fd)
        finally:
            os.close(fd)

    def directory(self, path):
        require(not path.is_symlink() and path.is_dir(), "unsafe-directory")
        info = path.lstat()
        if os.name == "posix":
            require(path.resolve() == path.absolute(), "unsafe-directory")
            verify_permissions(info)

    def command(self, args, seconds=10):
        remaining = min(seconds, self.deadline - time.monotonic())
        require(remaining > 0, "deadline")
        cwd = self.deploy if self.deploy.is_dir() else None
        process = subprocess.Popen(args, cwd=cwd, text=True, stdin=subprocess.DEVNULL,
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
        require(process.returncode == 0, "command-failed-" + args[0])
        return out

    def verify_lock(self):
        self.directory(self.root)
        if self.lock.exists():
            require(not self.lock.is_symlink() and self.lock.is_dir(), "lock-unsafe-directory")
            if os.name == "posix":
                require(self.lock.resolve() == self.lock.absolute(), "lock-unsafe-directory")
                verify_permissions(self.lock.lstat())
            entries = list(self.lock.iterdir())
            names = {e.name for e in entries}
            require(names in ({"owner"}, {"owner", "recovery.json"}), "lock-directory-unexpected-contents")
            owner_path = self.lock / "owner"
            require(not owner_path.is_symlink() and owner_path.is_file(), "owner-file-unsafe")
            st = owner_path.lstat()
            require(stat.S_ISREG(st.st_mode) and st.st_nlink == 1, "owner-not-regular-singly-linked")
            require(st.st_size <= 1024, "owner-file-size")
            if os.name == "posix":
                verify_permissions(st)
            token = owner_path.read_bytes().decode("utf-8").strip()
            require(matches(token, r"[0-9a-f]{32}"), "owner-token-not-32-hex")
            archive = self.root / f".deploy-lock.recovered-{self.request['sourceRunId']}-{token}"
            require(not archive.exists() and not archive.is_symlink(), "archive-collision")
            state = "owned"
            receipt = None
            if "recovery.json" in names:
                receipt = strict_json((self.lock / "recovery.json").read_text(encoding="utf-8"))
                require(receipt.get("owner") == token and receipt.get("schemaVersion") == 1, "receipt-invalid")
            return state, token, archive, receipt
        else:
            # Check if this exact owner was already archived on an earlier retry of this recovery run
            prefix = f".deploy-lock.recovered-{self.request['sourceRunId']}-"
            matched_archives = [p for p in self.root.glob(prefix + "[0-9a-f]*") if p.is_dir() and not p.is_symlink()]
            for arch in matched_archives:
                token = arch.name[len(prefix):]
                receipt_path = arch / "recovery.json"
                if receipt_path.is_file():
                    try:
                        rec = strict_json(receipt_path.read_text(encoding="utf-8"))
                        if (rec.get("owner") == token and
                            rec.get("sourceRunId") == str(self.request["sourceRunId"]) and
                            rec.get("sourceArtifactId") == self.request["sourceArtifactId"]):
                            return "archived", token, arch, rec
                    except Exception:
                        pass
            raise Refusal("missing-deploy-lock")

    def verify_no_supervisor_or_mutation(self, token):
        # 1. Systemd unit for this token
        unit_out = self.command(["systemctl", "show", f"csx-migration-{token}.service",
                                 "--property=ActiveState,SubState,MainPID,ControlPID"])
        props = dict(line.split("=", 1) for line in unit_out.splitlines() if "=" in line)
        require(props.get("ActiveState") in ("inactive", "not-found", "") and
                props.get("SubState") in ("dead", "not-found", "") and
                props.get("MainPID") in ("0", "") and
                props.get("ControlPID") in ("0", ""), "supervisor-not-terminal")

        # 2. Check no other csx-migration units are active
        active_units = self.command(["systemctl", "list-units", "--type=service", "csx-migration-*.service",
                                     "--state=active", "--no-legend", "--no-pager"])
        require(not active_units.strip(), "active-migration-supervisor-remains")

        # 3. Check helper containers
        for args in (["docker", "ps", "-aq", "--filter", f"name=^/csx-migrate-"],
                     ["docker", "ps", "-aq", "--filter", f"label=codesamplex.deploy-owner={token}"]):
            require(not self.command(args).strip(), "migration-helper-container-remains")

        # 4. Check database activity for migration helpers
        ledger_sql = ""
        if self.request.get("migrationLedgerBefore") is not None:
            ledger_sql = ", 'ledger', (SELECT json_build_object('version',max(version),'count',count(*)) FROM schema_migrations)"
        db_out = self.command(
            ["docker", "compose", "exec", "-T", "-e",
             "PGOPTIONS=-c default_transaction_read_only=on -c statement_timeout=5000 -c lock_timeout=3000",
             "db", "psql", "-X", "-U", "csx", "-d", "csx", "-v", "ON_ERROR_STOP=1", "-Atqc",
             "SELECT json_build_object('owned', (SELECT count(*) FROM pg_stat_activity WHERE application_name LIKE 'csx-migrate-%'), 'ddl', (SELECT count(*) FROM pg_stat_progress_create_index)" + ledger_sql + ")"],
            seconds=self.DATABASE_VERIFICATION_COMMAND_TIMEOUT_SECONDS)
        cleanup = strict_json(db_out)
        expected_cleanup = {"owned": 0, "ddl": 0}
        if self.request.get("migrationLedgerBefore") is not None:
            expected_cleanup["ledger"] = self.request["migrationLedgerBefore"]
        require(cleanup == expected_cleanup, "database-helper-or-ledger-mismatch")

        # 5. Check no deploy mutation processes are currently running
        ps_out = self.command(["ps", "-eo", "pid,args"])
        current_pid = os.getpid()
        for line in ps_out.splitlines():
            parts = line.strip().split(None, 1)
            if len(parts) < 2:
                continue
            try:
                pid = int(parts[0])
            except ValueError:
                continue
            if pid == current_pid or pid == os.getppid():
                continue
            cmd = parts[1]
            for forbidden in ("deploy.ps1", "deploy-production.ps1", "offline-migration.py", "offline-migration.ps1",
                              "docker load", "docker-compose up", "docker compose up"):
                if forbidden in cmd:
                    raise Refusal(f"deploy-mutation-process-active: {cmd}")
        return cleanup

    def inspect_live_container(self, prev_sha, prev_image, require_healthy=True):
        rows = strict_json(self.command(["docker", "inspect", "codesamplex-server-1"]))
        require(len(rows) == 1, "container-count")
        row = rows[0]
        require(row["Image"] == prev_image, "live-image-mismatch")
        require([v for v in row["Config"]["Env"] if v.startswith("CSX_VERSION=")] ==
                ["CSX_VERSION=" + prev_sha], "configured-revision-mismatch")

        state = row.get("State")
        require(isinstance(state, dict) and state.get("Running") is True and
                state.get("OOMKilled") is False, "container-not-healthy")
        restart_count = row.get("RestartCount")
        require(type(restart_count) is int and restart_count >= 0, "container-restart-count-invalid")
        health = state.get("Health")
        logs = health.get("Log") if isinstance(health, dict) else None
        recent = logs[-self.REQUIRED_CONSECUTIVE_HEALTHCHECKS:] if isinstance(logs, list) else []
        healthy = (isinstance(health, dict) and health.get("Status") == "healthy" and
                   len(recent) == self.REQUIRED_CONSECUTIVE_HEALTHCHECKS and
                   all(isinstance(entry, dict) and entry.get("ExitCode") == 0 for entry in recent))
        if require_healthy:
            require(healthy, "container-not-healthy")
        return row, restart_count, healthy

    def wait_for_healthy_container(self, prev_sha, prev_image):
        for attempt in range(self.HEALTH_OBSERVATION_ATTEMPTS):
            row, restart_count, healthy = self.inspect_live_container(
                prev_sha, prev_image, require_healthy=False)
            if healthy:
                return row, restart_count
            if attempt + 1 < self.HEALTH_OBSERVATION_ATTEMPTS:
                time.sleep(self.HEALTH_OBSERVATION_INTERVAL_SECONDS)
        raise Refusal("container-not-healthy")

    def verify_live_acceptance(self):
        prev_sha = self.request["previousProductionSha"]
        prev_image = self.request["previousImageDigest"]

        # 1. Container inspect: historical restarts are diagnostic; current
        # health requires Docker's healthy state and three consecutive passing
        # checks from its bounded log.
        row, restart_count = self.wait_for_healthy_container(prev_sha, prev_image)
        container_id = row["Id"]
        server_started_at = row["State"]["StartedAt"]

        # 2. Image inspect
        images = strict_json(self.command(["docker", "image", "inspect", prev_image]))
        require(len(images) == 1 and images[0]["Id"] == prev_image and
                images[0]["Config"]["Labels"].get("org.opencontainers.image.revision") == prev_sha,
                "image-label-revision-mismatch")

        # 3. Server loopback
        for path in ("healthz", "version"):
            body = self.command(["docker", "compose", "exec", "-T", "server", "wget", "-q", "-T", "5",
                                 "-t", "1", "-O-", "http://127.0.0.1:8080/" + path])
            require(body.strip() == "ok" if path == "healthz" else
                    strict_json(body).get("revision") == prev_sha, "loopback-" + path)

        # 4. Proxy smoke
        raw = self.command(["curl", "--noproxy", "*", "--connect-timeout", "3", "--max-time", "10",
                            "--resolve", "codesamplex.dev:443:127.0.0.1", "-sS", "-w", "\n%{http_code}",
                            "https://codesamplex.dev/healthz"], 12)
        body, sep, status = raw.rpartition("\n")
        require(sep and status == "200" and body.strip() == "ok", "proxy-health")

        raw_ver = self.command(["curl", "--noproxy", "*", "--connect-timeout", "3", "--max-time", "10",
                                "--resolve", "codesamplex.dev:443:127.0.0.1", "-sS", "-w", "\n%{http_code}",
                                "https://codesamplex.dev/version"], 12)
        body_ver, sep_ver, status_ver = raw_ver.rpartition("\n")
        require(sep_ver and status_ver == "200" and strict_json(body_ver).get("revision") == prev_sha, "proxy-version")

        # Re-verify the exact healthy container did not restart or change while
        # loopback and proxy evidence was collected.
        final_row, final_restart_count, _ = self.inspect_live_container(prev_sha, prev_image)
        require(final_row["Id"] == container_id and
                final_row["State"]["StartedAt"] == server_started_at and
                final_restart_count == restart_count,
                "container-changed-during-verification")

        return container_id, server_started_at, restart_count

    def release(self, token, archive, receipt):
        if receipt is None:
            receipt = {
                "schemaVersion": 1,
                "recoveryClass": self.request["recoveryClass"],
                "owner": token,
                "sourceRunId": str(self.request["sourceRunId"]),
                "sourceRunAttempt": self.request["sourceRunAttempt"],
                "sourceArtifactId": self.request["sourceArtifactId"],
                "sourceArtifactSha256": self.request["sourceArtifactSha256"],
                "sourceMigrationEvidenceSha256": self.request.get("sourceMigrationEvidenceSha256"),
                "targetSha": self.request["targetSha"],
                "previousProductionSha": self.request["previousProductionSha"],
                "previousImageDigest": self.request["previousImageDigest"],
                "migrationLedgerBefore": self.request.get("migrationLedgerBefore"),
                "recoveryRunId": str(self.request["recoveryRunId"]),
                "recoveryRunAttempt": self.request["recoveryRunAttempt"],
                "operationalSha": self.request["operationalSha"],
                "verifiedAt": utc(),
            }
            raw = (json.dumps(receipt, sort_keys=True, separators=(",", ":")) + "\n").encode()
            receipt_path = self.lock / "recovery.json"
            no_follow = getattr(os, "O_NOFOLLOW", 0)
            fd = os.open(receipt_path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | no_follow, 0o600)
            with os.fdopen(fd, "wb") as file:
                file.write(raw)
                file.flush()
                os.fsync(file.fileno())
            if os.name == "posix":
                fd_lock = os.open(self.lock, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0))
                try:
                    os.fsync(fd_lock)
                finally:
                    os.close(fd_lock)

        require(not archive.exists(), "archive-collision")
        os.rename(self.lock, archive)
        self.sync_directory(self.root)
        require(not self.lock.exists() and archive.is_dir(), "lock-archive-failed")

        # Clean up abort marker if present
        abort_marker = Path.home() / f".csx-deploy-aborted-{token}"
        if abort_marker.is_file() and not abort_marker.is_symlink():
            try:
                abort_marker.unlink()
            except OSError:
                pass

        return receipt

    def run(self):
        state, token, archive, receipt = self.verify_lock()
        expected_owner = self.request.get("expectedLockOwner")
        require(expected_owner is None or token == expected_owner, "lock-owner-does-not-match-source-evidence")
        database_evidence = self.verify_no_supervisor_or_mutation(token)
        cid, started_at, restart_count = self.verify_live_acceptance()
        if self.request["mode"] == "release":
            if state == "owned":
                receipt = self.release(token, archive, receipt)
                state = "archived"
        return {
            "schemaVersion": 1,
            "recoveryClass": self.request["recoveryClass"],
            "verifiedAt": utc(),
            "owner": token,
            "archive": str(archive),
            "lockState": state,
            "health": "ok",
            "containerId": cid,
            "containerStartedAt": started_at,
            "containerRestartCount": restart_count,
            "consecutiveHealthyChecks": self.REQUIRED_CONSECUTIVE_HEALTHCHECKS,
            "migrationLedger": database_evidence.get("ledger"),
            "receipt": receipt,
        }


if __name__ == "__main__":
    try:
        req = strict_json(base64.b64decode(sys.argv[1], validate=True))
        result = RecoverHost(req).run()
        print(json.dumps(result, sort_keys=True))
    except Exception as err:
        stage = str(err) if isinstance(err, Refusal) else "invalid-or-unavailable-evidence"
        print(f"CSX-LOCK-RECOVERY-REFUSED {stage}", file=sys.stderr)
        sys.exit(1)
