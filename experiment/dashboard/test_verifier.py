from __future__ import annotations

import json
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest import mock

import app as dashboard_app
from verifier import BENCH_RE, VerificationError, Verifier


class VerifierStoreTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temp = tempfile.TemporaryDirectory()
        root = Path(self.temp.name)
        self.repo = root / "repo"
        self.workspace = root / "workspace"
        self.workspace.mkdir()
        subprocess.run(["git", "init", "-q", self.repo], check=True)
        subprocess.run(["git", "-C", self.repo, "config", "user.email", "test@example.com"], check=True)
        subprocess.run(["git", "-C", self.repo, "config", "user.name", "Test"], check=True)
        (self.repo / "README.md").write_text("baseline\n")
        subprocess.run(["git", "-C", self.repo, "add", "README.md"], check=True)
        subprocess.run(["git", "-C", self.repo, "commit", "-qm", "base"], check=True)
        self.head = subprocess.check_output(["git", "-C", self.repo, "rev-parse", "HEAD"], text=True).strip()
        self.verifier = Verifier(self.repo, root / "persistence", self.workspace)

    def tearDown(self) -> None:
        self.temp.cleanup()

    def test_structured_rejection_is_final_and_audited(self) -> None:
        record = self.verifier.submit("integrator", self.head, self.head)
        finalized = self.verifier.verify(record["result_id"])
        self.assertEqual(finalized["status"], "rejected")
        self.assertFalse(finalized["accepted"])
        stored = json.loads((self.verifier.records / f"{record['result_id']}.json").read_text())
        self.assertEqual(stored, finalized)
        self.assertEqual(len(self.verifier.audit.read_text().splitlines()), 3)
        with self.assertRaisesRegex(VerificationError, "immutable"):
            self.verifier.verify(record["result_id"])

    def test_private_corpus_is_deterministic_and_separate(self) -> None:
        files = list(self.verifier.private_corpus.rglob("*.md"))
        self.assertEqual(len(files), 187)
        self.assertEqual(sum("private-sentinel-73129" in path.read_text() for path in files), 1)
        self.assertFalse(self.verifier.private_corpus.is_relative_to(self.workspace))

    def test_human_decision_does_not_mutate_finalized_result(self) -> None:
        record = self.verifier.submit("integrator", self.head, self.head)
        finalized = self.verifier.verify(record["result_id"])
        result_path = self.verifier.records / f"{record['result_id']}.json"
        original = result_path.read_bytes()
        decided = self.verifier.decide(record["result_id"], "approved", "acknowledged")
        self.assertEqual(result_path.read_bytes(), original)
        self.assertEqual(decided["human_decision"]["decision"], "approved")
        with self.assertRaisesRegex(VerificationError, "immutable"):
            self.verifier.decide(record["result_id"], "rejected", "changed mind")

    def test_benchmark_parser_accepts_cpu_suffix_and_gomaxprocs_one(self) -> None:
        output = (
            "BenchmarkRipgrepCorpus/miss       \t1\t123 ns/op\n"
            "BenchmarkRipgrepCorpus/literal-8  \t1\t456 ns/op\n"
        )
        self.assertEqual(
            BENCH_RE.findall(output),
            [("BenchmarkRipgrepCorpus/miss", "123"), ("BenchmarkRipgrepCorpus/literal", "456")],
        )


class ControlStoreTest(unittest.TestCase):
    def test_pause_state_is_service_owned_and_persistent(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            persistence = Path(directory)
            with mock.patch.object(dashboard_app, "PERSISTENCE", persistence):
                self.assertEqual(dashboard_app.control_state(), {"paused": False, "updated_at": None})
                paused = dashboard_app.update_control(dashboard_app.ControlUpdate(paused=True))
                self.assertTrue(paused["paused"])
                self.assertEqual(dashboard_app.control_state(), paused)
                resumed = dashboard_app.update_control(dashboard_app.ControlUpdate(paused=False))
                self.assertFalse(resumed["paused"])
                self.assertEqual(dashboard_app.control_state(), resumed)


if __name__ == "__main__":
    unittest.main()
