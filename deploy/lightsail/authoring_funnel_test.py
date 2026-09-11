import copy
import importlib.util
import json
from pathlib import Path
import sys
import tempfile
import time
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parent
SPEC = importlib.util.spec_from_file_location("funnel_controller", ROOT / "read-authoring-funnel.py")
controller = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(controller)
collector = controller.funnel
END = collector.timestamp_ns("2026-09-11T20:00:00Z")
START = END - 3600 * 1000000000
SECRET = "credential-DO-NOT-EMIT-private-coordinate"
IDENTITY = {"id": "a" * 64, "imageDigest": "sha256:" + "b" * 64,
            "startedAt": "2026-09-11T19:08:54.123456789Z", "revision": "c" * 40}
EXPECTED_ENV = {"EXPECTED_REVISION": IDENTITY["revision"]}


def line(message, at="2026-09-11T19:50:00.123456789Z"):
    return (at + " 2026/09/11 19:50:00 " + message + "\n").encode()


def poll(wanted="200/5", expansion="400/3", served="NO_WORK", age="1m2s", session="privateSessionId"):
    return collector.POLL_PREFIX + "session=" + session + " wanted=" + wanted + " expansion=" + expansion + " served=" + served + " snapshotAge=" + age


def fallback(error):
    return collector.FALLBACK_PREFIX + error + collector.FALLBACK_SUFFIX


def read_fixture(raw, identity=IDENTITY):
    calls = []

    def run(argv, timeout, byte_limit, **kwargs):
        calls.append((argv, timeout, byte_limit, kwargs))
        return json.dumps(identity).encode() if argv[1] == "inspect" else raw

    result = collector.collect(IDENTITY["revision"], run, lambda: END)
    return result, calls


class FunnelParsingTests(unittest.TestCase):
    def test_exact_source_counts_last_poll_and_safe_fallback_classes(self):
        raw = line(SECRET) + line(poll()) + line(poll("3/2", "4/1", "DEPENDENCY", "-1s"), "2026-09-11T19:55:00Z")
        raw += line(fallback("ERROR: canceling statement due to statement timeout (SQLSTATE 57014)"))
        raw += line(fallback("serverstore: no database connection available within the wait budget (class background, waited 0s)"))
        result, _ = read_fixture(raw)
        self.assertEqual(result["availability"], "available")
        self.assertEqual(result["funnel"]["wantedReadSum"], 203)
        self.assertEqual(result["funnel"]["wantedEligibleSum"], 7)
        self.assertEqual(result["funnel"]["expansionReadSum"], 404)
        self.assertEqual(result["funnel"]["expansionEligibleSum"], 4)
        self.assertEqual(result["funnel"]["servedCounts"]["NO_WORK"], 1)
        self.assertEqual(result["funnel"]["lastPoll"]["served"], "DEPENDENCY")
        self.assertEqual(result["funnel"]["snapshotAgeNsMin"], -1000000000)
        self.assertEqual(result["fallback"]["byClass"], {"statement_timeout": 1, "pool_busy": 1})
        encoded = json.dumps(result)
        self.assertNotIn(SECRET, encoded)
        self.assertNotIn("privateSessionId", encoded)
        self.assertNotIn("SQLSTATE", encoded)
        self.assertNotIn("AfterDependency", encoded)
        self.assertEqual(controller.validate(encoded.encode()), result)

    def test_no_logged_poll_is_scoped_and_has_no_last_measurement(self):
        for raw in (b"", line(SECRET)):
            result, _ = read_fixture(raw)
            self.assertEqual(result["scope"], "retained_current_container_logs")
            self.assertEqual(result["windowCoverage"], "retention_not_proven")
            self.assertEqual(result["funnelScope"], "logged_successful_poll_snapshot_counts_not_distinct_work")
            self.assertEqual(result["funnel"]["polls"], 0)
            self.assertIsNone(result["funnel"]["lastPoll"])
            self.assertIsNone(result["funnel"]["snapshotAgeNsMax"])

    def test_unknown_malformed_truncated_and_poison_fail_closed(self):
        fixtures = [line(poll(served=SECRET)), line(poll(wanted="1/2")), line(poll(expansion="401/1")),
                    line(poll() + " " + SECRET), line(poll(session=SECRET)),
                    line(fallback(SECRET)), line(fallback("context deadline exceeded")),
                    line(fallback("ERROR: canceling statement due to statement timeout (SQLSTATE 57014) " + SECRET)),
                    line(poll(age="NaN")), line(poll(age="999999h0m0s")), line(poll())[:-1],
                    line(collector.POLL_PREFIX.rstrip()), line(collector.FALLBACK_PREFIX[:-2] + " " + SECRET),
                    line(poll().replace("poll session=", "pollXsession=")),
                    line(poll()).replace(b"session=", b"session=\xff"),
                    line(poll()).replace(b"20", b"99", 1),
                    line(poll(), "2026-09-11T18:59:59Z"), line(poll(), "2026-09-11T20:00:01Z"),
                    line(SECRET + "\x00"), b"Docker failed: " + SECRET.encode() + b"\n"]
        for raw in fixtures:
            with self.subTest(raw=raw[:45]):
                result, _ = read_fixture(raw)
                self.assertEqual(result["availability"], "unavailable")
                self.assertTrue(all(result[k] is None for k in ("read", "funnel", "fallback", "identity")))
                self.assertNotIn(SECRET, json.dumps(result))
                controller.validate(json.dumps(result).encode())

    def test_log_byte_line_and_tail_limits_are_not_zero_measurements(self):
        cases = [(b"x" * (collector.MAX_BYTES + 1), "byte_limit"),
                 (line("x" * collector.MAX_LINE_BYTES), "line_limit"),
                 (line("irrelevant") * (collector.MAX_LINES + 1), "tail_limit")]
        for raw, reason in cases:
            result, _ = read_fixture(raw)
            self.assertEqual(result["failureClass"], reason)
            self.assertIsNone(result["funnel"])

    def test_go_duration_fraction_sign_and_bounds(self):
        for value, expected in {"0s": 0, "1h2m3s": 3723000000000, "-1s": -1000000000,
                                "1.5ms": 1500000, "1µs": 1000, "1us": 1000, "1ns": 1,
                                "0.000000001s": 1, "720h0m0s": collector.MAX_AGE_NS}.items():
            self.assertEqual(collector.duration_ns(value), expected)
        for value in ("", "nan", "1e9s", "1h60m0s", "1m60s", "1s1s", "0.1ns", "721h0m0s", "+1s", "١s", "1" * 100 + "s"):
            with self.assertRaises(collector.Unavailable):
                collector.duration_ns(value)

    def test_only_fixed_read_commands_and_stable_identity(self):
        _, calls = read_fixture(line(poll()))
        self.assertEqual(len(calls), 3)
        self.assertEqual(calls[0][:3], (collector.INSPECT, 3, 4096))
        self.assertEqual(calls[2][:3], (collector.INSPECT, 3, 4096))
        self.assertEqual(calls[1], (("docker", "logs", "--timestamps", "--since", collector.stamp(START), "--until", collector.stamp(END),
                                    "--tail", "10001", "codesamplex-server-1"), 10, collector.MAX_BYTES, {"merge_stderr": True}))
        self.assertNotIn(".Config.Env", " ".join(collector.INSPECT))
        counter = 0

        def replaced(argv, *args, **kwargs):
            nonlocal counter
            if argv[1] == "logs":
                return line(poll())
            counter += 1
            changed = dict(IDENTITY, id=str(counter) * 64)
            return json.dumps(changed).encode()

        result = collector.collect(IDENTITY["revision"], replaced, lambda: END)
        self.assertEqual(result["failureClass"], "identity_changed")
        self.assertIsNone(result["funnel"])

    def test_command_failure_and_timeout_discard_partial_results(self):
        for reason in ("command_failed", "command_timeout", "byte_limit"):
            def failed(*args, **kwargs):
                raise collector.Unavailable(reason)
            result = collector.collect(IDENTITY["revision"], failed, lambda: END)
            self.assertEqual(result["failureClass"], reason)
            self.assertIsNone(result["funnel"])

    def test_expected_revision_matches_both_identity_reads(self):
        for mismatch_read in (1, 2):
            calls = []
            inspections = 0

            def run(argv, *args, **kwargs):
                nonlocal inspections
                calls.append(argv)
                if argv[1] == "logs":
                    return line(poll())
                inspections += 1
                identity = dict(IDENTITY)
                if inspections == mismatch_read:
                    identity["revision"] = "e" * 40
                return json.dumps(identity).encode()

            result = collector.collect(IDENTITY["revision"], run, lambda: END)
            self.assertEqual(result["failureClass"], "revision_mismatch")
            self.assertEqual(result["expectedRevision"], IDENTITY["revision"])
            self.assertTrue(all(result[k] is None for k in ("identity", "read", "funnel", "fallback")))
            self.assertEqual(len(calls), 1 if mismatch_read == 1 else 3)
            controller.validate(json.dumps(result).encode(), IDENTITY["revision"])
        matched, _ = read_fixture(line(poll()))
        self.assertEqual(matched["availability"], "available")
        self.assertEqual(matched["identity"]["revision"], matched["expectedRevision"])


class TransportTests(unittest.TestCase):
    def test_invalid_expected_revision_is_rejected_before_transport_or_secret_paths(self):
        for value in (None, "", "C" * 40, "c" * 39, "c" * 41, SECRET, "c" * 40 + ";id"):
            with patch.object(Path, "is_file", side_effect=AssertionError("credential path read")), \
                    patch.object(collector.subprocess, "Popen", side_effect=AssertionError("SSH invoked")) as process:
                result = controller.diagnose({"EXPECTED_REVISION": value})
                self.assertEqual(result["failureClass"], "invalid_expected_revision")
                self.assertIsNone(result["expectedRevision"])
                with self.assertRaises(ValueError):
                    controller.remote({"EXPECTED_REVISION": value})
                collected = collector.collect(value, now=lambda: END)
                self.assertEqual(collected["failureClass"], "invalid_expected_revision")
                process.assert_not_called()
                self.assertNotIn(SECRET, json.dumps(result))

    def test_initialization_retains_complete_unavailable_envelope_without_transport(self):
        # Only workflow identity is available before credentials are installed.
        # Any diagnostic/SSH/subprocess invocation here is a regression.
        with patch.object(controller.os, "environ", dict(EXPECTED_ENV, GITHUB_SHA="d" * 40, GITHUB_RUN_ID="123")), \
                patch.object(controller, "diagnose", side_effect=AssertionError("diagnostic invoked")) as diagnose, \
                patch.object(collector.subprocess, "Popen", side_effect=AssertionError("SSH invoked")) as process, \
                patch.object(Path, "write_text") as write:
            self.assertEqual(controller.main(initialize=True), 0)
        diagnose.assert_not_called()
        process.assert_not_called()
        write.assert_called_once()
        envelope = json.loads(write.call_args.args[0])
        self.assertEqual(set(envelope), {"operationalSha", "workflowRunId", "diagnostic"})
        self.assertEqual((envelope["operationalSha"], envelope["workflowRunId"]), ("d" * 40, 123))
        diagnostic = controller.validate(json.dumps(envelope["diagnostic"]).encode())
        self.assertEqual(diagnostic["availability"], "unavailable")
        self.assertEqual(diagnostic["failureClass"], "not_collected")
        self.assertEqual(diagnostic["expectedRevision"], IDENTITY["revision"])
        self.assertTrue(all(diagnostic[k] is None for k in ("identity", "read", "funnel", "fallback")))

    def test_bounded_reader_enforces_deadline_and_memory_while_reading(self):
        commands = [("import time; time.sleep(5)", 0.05, 100, "command_timeout"),
                    ("import sys; sys.stdout.write('x'*10000); sys.stdout.flush()", 2, 100, "byte_limit"),
                    ("import sys; print('" + SECRET + "'); sys.exit(2)", 2, 1000, "command_failed")]
        for code, timeout, limit, reason in commands:
            started = time.monotonic()
            with self.assertRaises(collector.Unavailable) as raised:
                collector.bounded_command((sys.executable, "-I", "-c", code), timeout, limit)
            self.assertEqual(raised.exception.reason, reason)
            self.assertLess(time.monotonic() - started, 3)
            self.assertNotIn(SECRET, str(raised.exception))
        self.assertEqual(collector.bounded_command((sys.executable, "-I", "-c", "print('ok')"), 2, 100).strip(), b"ok")
        self.assertEqual(collector.bounded_command((sys.executable, "-I", "-c", "import sys; sys.stderr.write('" + SECRET + "'); print('ok')"), 2, 100).strip(), b"ok")

    def test_runner_rejects_secret_stdout_unknown_keys_duplicates_and_impossible_counts(self):
        valid, _ = read_fixture(line(poll()))
        variants = [SECRET.encode(), json.dumps(valid).encode() + b"\n" + SECRET.encode(), b"x" * 16385]
        for path, replacement in [("scope", SECRET), ("read", {"secret": SECRET}), ("identity", dict(IDENTITY, revision=SECRET))]:
            value = copy.deepcopy(valid)
            value[path] = replacement
            variants.append(json.dumps(value).encode())
        wrong_expected = copy.deepcopy(valid)
        wrong_expected["expectedRevision"] = "e" * 40
        variants.append(json.dumps(wrong_expected).encode())
        bad = copy.deepcopy(valid)
        bad["funnel"]["servedCounts"]["NO_WORK"] = 10
        variants.append(json.dumps(bad).encode())
        bad = copy.deepcopy(valid)
        bad["read"]["lines"] = True
        variants.append(json.dumps(bad).encode())
        variants.append(json.dumps(valid).replace('"schemaVersion": 1', '"schemaVersion": 1, "schemaVersion": 1').encode())
        for raw in variants:
            result = controller.diagnose(EXPECTED_ENV, lambda env: raw)
            self.assertEqual(result["failureClass"], "invalid_summary")
            self.assertIsNone(result["funnel"])
            self.assertNotIn(SECRET, json.dumps(result))
        def failed(env):
            raise RuntimeError(SECRET)
        self.assertEqual(controller.diagnose(EXPECTED_ENV, failed)["failureClass"], "transport_failed")

    def test_pinned_ssh_has_fixed_stdin_program_and_no_output_passthrough(self):
        with tempfile.TemporaryDirectory() as directory:
            key, known = Path(directory) / "id", Path(directory) / "known_hosts"
            key.write_text("fixture")
            known.write_text("fixture")
            env = {"PRODUCTION_HOST": "example.invalid", "PRODUCTION_USER": "ubuntu",
                   "PRODUCTION_KEY_PATH": str(key), "PRODUCTION_KNOWN_HOSTS_PATH": str(known), **EXPECTED_ENV}
            calls = []
            def run(*args, **kwargs):
                calls.append((args, kwargs))
                return b"{}"
            controller.remote(env, run)
            argv, timeout, limit = calls[0][0]
            self.assertEqual(argv[-2:], ("ubuntu@example.invalid", "timeout --kill-after=2s 25s python3 -I -B - " + IDENTITY["revision"]))
            for option in ("BatchMode=yes", "IdentitiesOnly=yes", "StrictHostKeyChecking=yes", "UserKnownHostsFile=" + str(known.resolve())):
                self.assertIn(option, argv)
            self.assertEqual((timeout, limit), (35, 16384))
            self.assertEqual(calls[0][1]["source"], (ROOT / "collect-authoring-funnel.py").read_bytes())
            for field, poison in (("PRODUCTION_HOST", "host; " + SECRET), ("PRODUCTION_USER", "root -- " + SECRET)):
                with self.assertRaises(ValueError):
                    controller.remote(dict(env, **{field: poison}), run)
            self.assertEqual(len(calls), 1)

    def test_workflow_manual_canonical_gated_and_production_serialized(self):
        workflow = (ROOT.parents[1] / ".github/workflows/authoring-funnel-diagnostic.yml").read_text()
        for required in ("workflow_dispatch:", 'test "$GITHUB_EVENT_NAME" = workflow_dispatch',
                         'test "$GITHUB_REF" = refs/heads/main', 'test "$GITHUB_REPOSITORY" = r2cuerdame/CodeSampleX',
                         '.head_sha == $sha', '.head_branch == "main"', '.conclusion == "success"',
                         '.head_repository.full_name == $repo', '.event == "push" or .event == "workflow_dispatch"',
                         'test "$(git rev-parse HEAD)" = "$GITHUB_SHA"', "persist-credentials: false",
                         "group: codesamplex-production", "cancel-in-progress: false", "environment: codesamplex-production",
                         "secrets.CSX_PRODUCTION_SSH_KEY", "secrets.CSX_PRODUCTION_KNOWN_HOSTS", "if: always()",
                         "expected_revision:", "required: true", "EXPECTED_REVISION: ${{ inputs.expected_revision }}",
                         "run: python3 -I deploy/lightsail/read-authoring-funnel.py --initialize",
                         'rm -f "$RUNNER_TEMP/csx-funnel-ssh/id" "$RUNNER_TEMP/csx-funnel-ssh/known_hosts"',
                         "actions/checkout@fbc6f3992d24b796d5a048ff273f7fcc4a7b6c09",
                         "actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a"):
            self.assertIn(required, workflow)
        self.assertLess(workflow.index("Require successful exact canonical main CI"), workflow.index("SSH_PRIVATE_KEY:"))
        self.assertLess(workflow.index("actions/checkout@"), workflow.index("Initialize unavailable evidence"))
        for forbidden in ("workflow_run:", "schedule:", "pull_request:", "issues: write", "contents: write", "continue-on-error", "StrictHostKeyChecking=no", "scp ", "ssh "):
            self.assertNotIn(forbidden, workflow)


if __name__ == "__main__":
    unittest.main()
