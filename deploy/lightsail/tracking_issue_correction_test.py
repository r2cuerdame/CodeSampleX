import copy
import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location("tracking", Path(__file__).with_name("tracking-issue-correction.py"))
tracking = importlib.util.module_from_spec(spec)
spec.loader.exec_module(tracking)

REPO = "r2cuerdame/CodeSampleX"
TARGET = "decd00ebf5ce5d222deefcaaa0f747f264b623d3"

class TrackingCorrectionTest(unittest.TestCase):
    def setUp(self):
        self.calls = []
        self.pull = {"merged": True, "merge_commit_sha": TARGET,
                     "base": {"repo": {"full_name": REPO}}}
        self.issue = {"number": 548, "body": "Canonical target " + TARGET}

    def api(self, path):
        self.calls.append(path)
        if path == "repos/" + REPO + "/pulls/555":
            return self.pull
        if path == "repos/" + REPO + "/issues/548":
            return self.issue
        raise ValueError("unexpected or unauthenticated resource")

    def resolve(self, **kwargs):
        args = dict(repository=REPO, original="555", corrected="#548",
                    target=TARGET, event="workflow_dispatch")
        args.update(kwargs)
        return tracking.correct_issue(self.api, **args)

    def test_recover_deployed_merged_pr_to_canonical_issue_without_mutation(self):
        before = copy.deepcopy((self.pull, self.issue))
        self.assertEqual(self.resolve(), "548")
        self.assertEqual(self.calls, ["repos/" + REPO + "/pulls/555", "repos/" + REPO + "/issues/548"])
        self.assertEqual((self.pull, self.issue), before)

    def test_same_repository_issue_urls(self):
        self.assertEqual(self.resolve(original="https://github.com/" + REPO + "/issues/555",
                                      corrected="https://github.com/" + REPO + "/issues/548"), "548")

    def test_other_repository_and_noncanonical_references_fail_before_api(self):
        for value in ("https://github.com/other/repo/issues/548", "../548", "0", "-1",
                      "548?override=1", "548\n", "548/"):
            with self.subTest(value=value):
                self.calls = []
                with self.assertRaises(ValueError):
                    self.resolve(corrected=value)
                self.assertEqual(self.calls, [])

    def test_automatic_observation_cannot_rebind(self):
        with self.assertRaises(ValueError):
            self.resolve(event="workflow_run")
        self.assertEqual(self.calls, [])

    def test_unmerged_pr_cannot_rebind(self):
        self.pull["merged"] = False
        with self.assertRaises(ValueError):
            self.resolve()
        self.assertEqual(len(self.calls), 1)

    def test_other_commit_and_base_repository_cannot_rebind(self):
        for key in ("commit", "repository"):
            with self.subTest(key=key):
                self.setUp()
                if key == "commit":
                    self.pull["merge_commit_sha"] = "0" * 40
                else:
                    self.pull["base"]["repo"]["full_name"] = "other/repo"
                with self.assertRaises(ValueError):
                    self.resolve()
                self.assertEqual(len(self.calls), 1)

    def test_pr_destination_cannot_replace_an_issue(self):
        self.issue["pull_request"] = {"url": "public-pr"}
        with self.assertRaises(ValueError):
            self.resolve()

    def test_wrong_issue_number_or_missing_api_proof_fails(self):
        self.issue["number"] = 549
        with self.assertRaises(ValueError):
            self.resolve()
        with self.assertRaises(ValueError):
            self.resolve(original="548")

    def test_invalid_target_fails_before_api(self):
        with self.assertRaises(ValueError):
            self.resolve(target="main")
        self.assertEqual(self.calls, [])

if __name__ == "__main__":
    unittest.main()
