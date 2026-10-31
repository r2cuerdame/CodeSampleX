"""Safety and window-shape checks for the production diagnostic."""
import ast
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest import mock

ROOT = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("p95", ROOT / "collect-p95-window.py")
p95 = importlib.util.module_from_spec(spec)
spec.loader.exec_module(p95)
reader_spec = importlib.util.spec_from_file_location("p95_reader", ROOT / "read-p95-window.py")
reader = importlib.util.module_from_spec(reader_spec)
reader_spec.loader.exec_module(reader)


class P95WindowTest(unittest.TestCase):
    def test_runner_only_sends_fixed_collector_over_pinned_ssh(self):
        with tempfile.TemporaryDirectory() as folder:
            key = Path(folder) / "id"
            known = Path(folder) / "known"
            key.touch()
            known.touch()
            env = {"EXPECTED_REVISION": "a" * 40, "PRODUCTION_HOST": "example.org",
                   "PRODUCTION_KEY_PATH": str(key), "PRODUCTION_KNOWN_HOSTS_PATH": str(known)}
            response = mock.Mock(returncode=0, stdout=json.dumps({"schema": 1, "rounds": 20,
                "samples": [{}] * 60, "postgres": [{}] * 20}).encode())
            with mock.patch.object(reader.subprocess, "run", return_value=response) as invoke, \
                 mock.patch.object(reader, "OUTPUT", Path(folder) / "out.json"):
                reader.run(env)
            command = invoke.call_args.args[0]
            self.assertEqual(command[0], "ssh")
            self.assertIn("StrictHostKeyChecking=yes", command)
            self.assertIn("UserKnownHostsFile=" + str(known), command)
            self.assertEqual(command[-1], "EXPECTED_REVISION=" + "a" * 40 + " timeout --kill-after=2s 360s python3 -I -B -")
            self.assertEqual(invoke.call_args.kwargs["input"], reader.SOURCE.read_bytes())

    def test_remote_operations_are_fixed_reads(self):
        source = (ROOT / "collect-p95-window.py").read_text(encoding="utf-8")
        tree = ast.parse(source)
        commands = [n for n in ast.walk(tree) if isinstance(n, ast.Call)
                    and isinstance(n.func, ast.Attribute) and n.func.attr == "run"
                    and isinstance(n.func.value, ast.Name) and n.func.value.id == "subprocess"]
        self.assertEqual(len(commands), 2)  # fixed docker inspect and fixed psql
        self.assertIn("default_transaction_read_only=on", " ".join(p95.DB_COMMAND))
        self.assertIn("statement_timeout=1500", " ".join(p95.DB_COMMAND))
        self.assertEqual(p95.DB_COMMAND[-2], "-Atqc")
        self.assertTrue(p95.DB_COMMAND[-1].lstrip().startswith("SELECT json_build_object("))
        self.assertNotRegex(p95.SQL.upper(), r"\b(INSERT|UPDATE|DELETE|CREATE|ALTER|DROP|TRUNCATE|CALL|COPY)\b")
        self.assertEqual(p95.PATHS, ("/v1/stats", "/healthz", "/v1/shards/npm/zod/3"))
        self.assertEqual([n.value for n in ast.walk(tree) if isinstance(n, ast.Constant) and n.value == "GET"], ["GET"])

    def test_database_timeout_is_an_unavailable_snapshot(self):
        with mock.patch.object(p95.subprocess, "run", side_effect=p95.subprocess.TimeoutExpired(p95.DB_COMMAND, 4)):
            self.assertEqual(p95.database(), {"available": False, "reason": "db_command_timeout"})

    def test_ttfb_includes_local_tcp_and_tls_setup(self):
        response = mock.Mock(status=200)
        response.getheader.return_value = "middleware;dur=1, db_wait;dur=0, query_handler;dur=2, serialize;dur=1"
        connection = mock.Mock()
        connection.getresponse.return_value = response
        tls = mock.Mock()
        tls.wrap_socket.return_value = mock.Mock()
        with mock.patch.object(p95.http.client, "HTTPSConnection", return_value=connection), \
             mock.patch.object(p95.socket, "create_connection", return_value=mock.Mock()), \
             mock.patch.object(p95.ssl, "create_default_context", return_value=tls), \
             mock.patch.object(p95.time, "monotonic", side_effect=(1.0, 1.4)):
            sample = p95.request("/healthz")
        self.assertEqual(sample["ttfbMs"], 400)
        self.assertEqual(sample["elapsedMs"], 400)

    def test_one_window_contains_all_three_routes_and_db_per_round(self):
        fake_identity = mock.Mock(returncode=0, stdout=b"a" * 40)
        fake_response = {"status": 200, "ttfbMs": 10, "phasesMs": {"middleware": 1, "db_wait": 2,
                         "query_handler": 3, "serialize": 1}, "at": "now"}
        ticks = iter((i * 100, i * 20) for i in range(122))
        with mock.patch.dict(p95.os.environ, {"EXPECTED_REVISION": "a" * 40}), \
             mock.patch.object(p95.subprocess, "run", return_value=fake_identity), \
             mock.patch.object(p95, "request", side_effect=lambda path: {**fake_response, "phasesMs": dict(fake_response["phasesMs"])}), \
             mock.patch.object(p95, "database", return_value={"available": True, "data": {"waits": []}}), \
             mock.patch.object(p95, "cpu", side_effect=lambda: next(ticks)), \
             mock.patch.object(p95.time, "sleep"):
            result = p95.collect()
        self.assertEqual(len(result["samples"]), 60)
        self.assertEqual(len(result["postgres"]), 20)
        self.assertEqual({x["path"] for x in result["samples"]}, set(p95.PATHS))
        self.assertEqual(result["routes"]["/healthz"]["caddyResidualP95Ms"], 3)
        self.assertEqual(result["routes"]["/healthz"]["caddyForwardedUpstreamAppP95Ms"], 7)
        self.assertEqual(result["timingStart"], "before_local_tcp_tls")
        self.assertIn("flush writes", result["probeEffectNote"])


if __name__ == "__main__":
    unittest.main()
