import copy
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

ROOT = Path(__file__).resolve().parent
SPEC = importlib.util.spec_from_file_location("throughput_controller", ROOT / "read-farm-throughput.py")
controller = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(controller)
collector = controller.throughput
SHA, PEER = "c" * 40, "ed25519:" + "d" * 16
SLOTS = ("1" * 64, "2" * 64, "3" * 64)
SECRET = "DO-NOT-EMIT-token-host-session-coordinate"
IDENTITY = {"id": "a" * 64, "imageDigest": "sha256:" + "b" * 64,
            "revision": SHA, "startedAt": "2026-09-11T18:00:00Z"}
ENV = {"EXPECTED_REVISION": SHA, "VERIFIER_PEER": PEER,
       **{"SLOT%d_LABEL_SHA256" % n: value for n, value in enumerate(SLOTS, 1)}}


def encoded(value):
    return (json.dumps(value) + "\n").encode()


def counts():
    return {"windowStart": "2026-09-11T19:00:00.000000Z", "windowEnd": "2026-09-11T20:00:00.000000Z",
            "receipts": {"accepted": 4, "pass": 3},
            "sampleFirstPass": {"unambiguousNodeSamples": 1, "sharedFirstTimestampSamples": 1,
                                "unknownHistoricalTimestampSamples": 1},
            "gen": {"currentSlotAttributedDrafts": 2, "createdAndUpdatedTimestampEqual": 1,
                    "postcreationTimestampChanged": 1,
                    "slots": [{"slot": n, "currentSlotAttributedDrafts": int(n != 3),
                               "createdAndUpdatedTimestampEqual": int(n == 1),
                               "postcreationTimestampChanged": int(n == 2)} for n in (1, 2, 3)]}}


def collect_fixture(raw=None, before=None, after=None):
    replies = [encoded(before or IDENTITY), encoded(counts()) if raw is None else raw,
               encoded(after or before or IDENTITY)]
    calls = []

    def run(argv, seconds, limit):
        calls.append((argv, seconds, limit))
        return replies.pop(0)

    return collector.collect(SHA, PEER, SLOTS, run), calls


class ScalarEvidenceTests(unittest.TestCase):
    def test_one_fixed_select_privacy_bounds_and_identity(self):
        result, calls = collect_fixture()
        self.assertEqual(result["availability"], "available")
        self.assertTrue(result["cleanWindowEligible"])
        self.assertEqual(result["counts"], counts())
        self.assertEqual(len(calls), 3)
        self.assertEqual(calls[0][0], controller.transport.INSPECT)
        self.assertEqual(calls[2][0], controller.transport.INSPECT)
        sql = collector.throughput_sql(PEER, SLOTS)
        self.assertEqual(calls[1][0], collector.sql_command(sql))
        command = calls[1][0]
        self.assertEqual(command[:6], ("docker", "compose", "-f", "/opt/codesamplex/deploy/docker-compose.yml", "exec", "-T"))
        self.assertIn("PGOPTIONS=-c default_transaction_read_only=on -c statement_timeout=1000 -c lock_timeout=500 -c jit=off -c standard_conforming_strings=on", command)
        self.assertIn("ON_ERROR_STOP=1", command)
        self.assertIn("-X", command)
        self.assertNotIn(";", sql)
        for _, seconds, limit in calls:
            self.assertLessEqual(seconds, 3)
            self.assertEqual(limit, 8192)
        output = encoded(result)
        for private in (IDENTITY["id"], PEER, *SLOTS, SECRET):
            self.assertNotIn(private.encode(), output)
        self.assertEqual(collector.validate(output, SHA, PEER, SLOTS), result)

    def test_warmup_retains_valid_counts_without_clean_hour_claim(self):
        warm = {**IDENTITY, "startedAt": "2026-09-11T19:30:00Z"}
        result, _ = collect_fixture(before=warm)
        self.assertEqual(result["availability"], "available")
        self.assertFalse(result["cleanWindowEligible"])
        self.assertEqual(result["counts"], counts())
        self.assertIn("requires_separate_farm_health", result["cleanWindowScope"])
        self.assertEqual(result["timestampEqualityScope"], "timestamp_equality_not_immutable_history")

    def test_zero_counts_remain_available(self):
        value = counts()
        value["receipts"] = dict.fromkeys(value["receipts"], 0)
        value["sampleFirstPass"] = dict.fromkeys(value["sampleFirstPass"], 0)
        for key in collector.GEN_COUNTS:
            value["gen"][key] = 0
            for slot in value["gen"]["slots"]:
                slot[key] = 0
        result, _ = collect_fixture(encoded(value))
        self.assertEqual(result["availability"], "available")
        self.assertEqual(result["counts"], value)

    def test_invalid_public_binding_never_calls_transport(self):
        values = [dict(ENV, EXPECTED_REVISION=SECRET), dict(ENV, VERIFIER_PEER=SECRET),
                  dict(ENV, SLOT1_LABEL_SHA256=""), dict(ENV, SLOT3_LABEL_SHA256=SLOTS[0]),
                  dict(ENV, SLOT1_LABEL_SHA256=SLOTS[0] + "\n"), dict(ENV, VERIFIER_PEER=PEER.upper())]
        for env in values:
            with self.subTest(env=env):
                def forbidden(_):
                    self.fail("invalid binding entered transport")
                result = controller.diagnose(env, forbidden)
                self.assertEqual(result["availability"], "unavailable")
                self.assertNotIn(SECRET, json.dumps(result))

    def test_malformed_and_secret_bearing_results_fail_closed(self):
        bad = []
        for path, value in [(('receipts', 'accepted'), -1), (('receipts', 'accepted'), True),
                            (('receipts', 'accepted'), 2**54), (('receipts', 'pass'), 5),
                            (('sampleFirstPass', 'unambiguousNodeSamples'), 4),
                            (('gen', 'postcreationTimestampChanged'), 2)]:
            item = counts()
            item[path[0]][path[1]] = value
            bad.append(encoded(item))
        item = counts()
        item["gen"]["slots"][1]["slot"] = 1
        bad.append(encoded(item))
        item = counts()
        item["windowStart"] = "2026-09-11T18:00:00Z"
        bad.append(encoded(item))
        item = counts()
        item[SECRET] = SECRET
        bad.extend([encoded(item), encoded(counts())[:-1], b'{}\xff\n', b'{"x":1,"x":2}\n',
                    (SECRET * 1000).encode(), b'{"accepted":NaN}\n'])
        for raw in bad:
            with self.subTest(raw=raw[:30]):
                result, _ = collect_fixture(raw)
                self.assertEqual(result["availability"], "unavailable")
                self.assertIsNone(result["counts"])
                self.assertIsNone(result["identity"])
                self.assertNotIn(SECRET, json.dumps(result))

    def test_revision_identity_change_and_command_failures(self):
        for change in ({"revision": "f" * 40}, {"id": "f" * 64}, {"startedAt": "2026-09-11T18:01:00Z"}, {SECRET: SECRET}):
            result, _ = collect_fixture(after={**IDENTITY, **change})
            self.assertEqual(result["availability"], "unavailable")
            self.assertIsNone(result["counts"])
        for reason in ("command_failed", "command_timeout", "byte_limit"):
            def fail(*_):
                raise controller.transport.Unavailable(reason)
            result = collector.collect(SHA, PEER, SLOTS, fail)
            self.assertEqual(result["failureClass"], reason)
        def secret_failure(*_):
            raise ValueError(SECRET)
        self.assertNotIn(SECRET, json.dumps(collector.collect(SHA, PEER, SLOTS, secret_failure)))
        clock = iter((0, 13))
        result = collector.collect(SHA, PEER, SLOTS, lambda *_: self.fail("expired budget ran command"), lambda: next(clock))
        self.assertEqual(result["failureClass"], "window_timeout")

    def test_transport_uses_pinned_identity_no_config_or_forwarding(self):
        with tempfile.TemporaryDirectory() as directory:
            identity = Path(directory) / "id"
            known = Path(directory) / "known"
            identity.write_text("fixture")
            known.write_text("fixture")
            env = dict(ENV, PRODUCTION_HOST="example.invalid", PRODUCTION_USER="ubuntu",
                       PRODUCTION_KEY_PATH=str(identity), PRODUCTION_KNOWN_HOSTS_PATH=str(known))
            def spy(argv, seconds, limit, source):
                self.assertEqual(argv[:4], ("ssh", "-F", "/dev/null", "-T"))
                for option in ("IdentitiesOnly=yes", "BatchMode=yes", "StrictHostKeyChecking=yes",
                               "ClearAllForwardings=yes", "PermitLocalCommand=no"):
                    self.assertIn(option, argv)
                self.assertEqual(argv[-1], controller.REMOTE_COMMAND + " " + " ".join((SHA, PEER) + SLOTS))
                self.assertEqual((seconds, limit), (22, 8192))
                self.assertLessEqual(len(source), 65536)
                namespace = {"__name__": "source_fixture"}
                exec(compile(source, "<fixture>", "exec"), namespace)
                self.assertEqual(namespace["throughput_sql"](PEER, SLOTS), collector.throughput_sql(PEER, SLOTS))
                return b"fixture"
            self.assertEqual(controller.remote(env, spy), b"fixture")

    def test_outer_timeout_and_partial_stdout_keep_unavailable_envelope(self):
        def timeout(_):
            raise TimeoutError(SECRET)
        result = controller.diagnose(ENV, timeout)
        self.assertEqual(result["failureClass"], "transport_failed")
        for field in ("identity", "counts", "cleanWindowEligible"):
            self.assertIsNone(result[field])
        self.assertNotIn(SECRET, json.dumps(result))
        partial = encoded(collect_fixture()[0])[:-2] + SECRET.encode()
        result = controller.diagnose(ENV, lambda _: partial)
        self.assertEqual(result["failureClass"], "invalid_summary")
        self.assertIsNone(result["counts"])
        self.assertNotIn(SECRET, json.dumps(result))
        initial = controller.diagnose(ENV, lambda _: self.fail("initialize entered transport"), initialize=True)
        self.assertEqual(initial["failureClass"], "not_collected")
        self.assertIsNone(initial["counts"])

    def test_workflow_canonical_gates_and_scalar_only_artifact(self):
        workflow = (ROOT.parents[1] / ".github/workflows/farm-throughput.yml").read_text()
        for required in ('test "$GITHUB_EVENT_NAME" = workflow_dispatch', 'test "$GITHUB_REF" = refs/heads/main',
                         'test "$GITHUB_REPOSITORY" = r2cuerdame/CodeSampleX',
                         'test "$(git rev-parse HEAD)" = "$GITHUB_SHA"',
                         '.path == ".github/workflows/ci.yml"', '.head_sha == $sha', '.head_branch == "main"',
                         '.conclusion == "success"', '.event == "push" or .event == "workflow_dispatch"',
                         '.repository.full_name == $repo', '.head_repository.full_name == $repo',
                         'persist-credentials: false', 'group: codesamplex-production', 'cancel-in-progress: false',
                         'environment: codesamplex-production', 'path: farm-throughput.json', 'if: always()'):
            self.assertIn(required, workflow)
        self.assertLess(workflow.index("--initialize"), workflow.index("CSX_PRODUCTION_SSH_KEY"))
        self.assertLess(workflow.index("Require successful exact canonical main CI"), workflow.index("CSX_PRODUCTION_SSH_KEY"))
        self.assertNotIn("pull_request", workflow)
        self.assertNotIn("schedule:", workflow)
        self.assertNotIn("ssh-keyscan", workflow)


if __name__ == "__main__":
    unittest.main()
