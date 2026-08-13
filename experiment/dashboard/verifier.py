"""Trusted verifier and append-only structured result store for the rg experiment."""

from __future__ import annotations

import hashlib
import json
import math
import os
import re
import shutil
import statistics
import subprocess
import tempfile
import threading
import time
import uuid
from pathlib import Path
from typing import Any


BENCH_RE = re.compile(r"^(BenchmarkRipgrepCorpus/\S+?)(?:-\d+)?\s+\d+\s+(\d+) ns/op", re.MULTILINE)
FINAL = {"verified", "rejected", "error"}


class VerificationError(RuntimeError):
    pass


class Verifier:
    def __init__(self, repo: Path, persistence: Path, workspace: Path):
        self.repo = repo.resolve()
        self.persistence = persistence.resolve()
        self.workspace = workspace.resolve()
        self.records = self.persistence / "records"
        self.decisions = self.persistence / "decisions"
        self.artifacts = self.persistence / "artifacts"
        self.private_corpus = self.persistence / "private-corpus"
        self.audit = self.persistence / "audit" / "events.jsonl"
        for directory in (self.records, self.decisions, self.artifacts, self.private_corpus, self.audit.parent):
            directory.mkdir(parents=True, exist_ok=True)
        self._lock = threading.Lock()
        self._prepare_private_corpus()

    def submit(self, agent: str, candidate_commit: str, base_commit: str) -> dict[str, Any]:
        if not re.fullmatch(r"[a-z0-9][a-z0-9-]{0,39}", agent):
            raise VerificationError("invalid agent handle")
        result_id = f"{time.strftime('%Y%m%dT%H%M%SZ', time.gmtime())}-{uuid.uuid4().hex[:10]}"
        record = {
            "schema_version": 1,
            "result_id": result_id,
            "agent": agent,
            "candidate_commit": candidate_commit,
            "base_commit": base_commit,
            "submitted_at": now_iso(),
            "status": "queued",
            "verifier_version": "rg-verifier-v1",
            "corpora": {
                "mdn_commit": "c808a24d4e4f7bda00e7117f315965ed39b780e5",
                "private_seed": 73129,
            },
        }
        with self._lock:
            self._write_record(record)
            self._audit("submitted", record)
        return record

    def verify(self, result_id: str) -> dict[str, Any]:
        with self._lock:
            record = self.get(result_id)
            if record["status"] in FINAL:
                raise VerificationError("finalized records are immutable")
            if record["status"] == "running":
                raise VerificationError("verification is already running")
            record["status"] = "running"
            record["started_at"] = now_iso()
            self._write_record(record)
            self._audit("started", record)
        try:
            details = self._evaluate(record)
            record.update(details)
            record["status"] = "verified" if details["accepted"] else "rejected"
        except VerificationError as exc:
            record["status"] = "rejected"
            record["accepted"] = False
            record["rejection_reason"] = str(exc)
        except Exception as exc:
            record["status"] = "error"
            record["accepted"] = False
            record["error"] = str(exc)
        with self._lock:
            record["finished_at"] = now_iso()
            self._write_record(record)
            self._write_markdown(record)
            self._audit("finalized", record)
            return record

    def get(self, result_id: str) -> dict[str, Any]:
        if not re.fullmatch(r"[A-Za-z0-9_-]+", result_id):
            raise VerificationError("invalid result ID")
        path = self.records / f"{result_id}.json"
        if not path.is_file():
            raise VerificationError("unknown result")
        record = json.loads(path.read_text())
        decision = self.decisions / f"{result_id}.json"
        if decision.is_file():
            record["human_decision"] = json.loads(decision.read_text())
        return record

    def list(self) -> list[dict[str, Any]]:
        return [self.get(path.stem) for path in sorted(self.records.glob("*.json"), reverse=True)]

    def decide(self, result_id: str, decision: str, comment: str) -> dict[str, Any]:
        if decision not in {"approved", "rejected"}:
            raise VerificationError("decision must be approved or rejected")
        with self._lock:
            record = self.get(result_id)
            if record["status"] not in FINAL:
                raise VerificationError("verification must finish before a human decision")
            decision_path = self.decisions / f"{result_id}.json"
            if decision_path.exists():
                raise VerificationError("human decisions are immutable")
            record["human_decision"] = {"decision": decision, "comment": comment[:2000], "at": now_iso()}
            atomic_write(decision_path, json.dumps(record["human_decision"], indent=2, sort_keys=True) + "\n")
            self._write_markdown(record)
            self._audit("human_decision", record)
            return record

    def _evaluate(self, record: dict[str, Any]) -> dict[str, Any]:
        candidate = self._resolve_commit(record["candidate_commit"])
        base = self._resolve_commit(record["base_commit"])
        self._run(["git", "merge-base", "--is-ancestor", base, candidate], self.repo)
        changed = self._run(["git", "diff", "--name-only", base, candidate], self.repo).stdout.splitlines()
        if not changed or any(name != "pkg/shell/cmds/rg.go" for name in changed):
            raise VerificationError(f"candidate diff is outside pkg/shell/cmds/rg.go: {changed}")

        run_dir = self.artifacts / record["result_id"]
        run_dir.mkdir(parents=True, exist_ok=False)
        with tempfile.TemporaryDirectory(prefix="openlore-verify-") as temp:
            root = Path(temp)
            base_tree, candidate_tree = root / "base", root / "candidate"
            self._run(["git", "worktree", "add", "--detach", str(base_tree), base], self.repo)
            self._run(["git", "worktree", "add", "--detach", str(candidate_tree), candidate], self.repo)
            try:
                source = (candidate_tree / "pkg/shell/cmds/rg.go").read_text()
                forbidden = [token for token in ("time.Sleep", "os/exec", "syscall.", "OPENLORE_RG_CORPUS", ".benchdata") if token in source]
                if forbidden:
                    raise VerificationError(f"anti-cheat source check rejected: {forbidden}")
                private_test = candidate_tree / "pkg/shell/cmds/rg_verifier_private_test.go"
                private_test.write_text(PRIVATE_TEST)
                checks = {}
                checks["tests"] = self._checked(
                    "go-test", ["go", "test", "./..."], candidate_tree, run_dir,
                    {"OPENLORE_PRIVATE_CORPUS": str(self.private_corpus)},
                )
                checks["race"] = self._checked(
                    "go-race", ["go", "test", "-race", "./pkg/shell/cmds", "-run", "Rg|VerifierPrivate"],
                    candidate_tree, run_dir, {"OPENLORE_PRIVATE_CORPUS": str(self.private_corpus)},
                )
                checks["vet"] = self._checked("go-vet", ["go", "vet", "./..."], candidate_tree, run_dir)
                private_test.unlink()

                samples: dict[str, Any] = {}
                case_speedups: list[float] = []
                for corpus_name, corpus in self._corpora().items():
                    baseline = self._benchmark(base_tree, corpus, run_dir / f"baseline-{corpus_name}.txt")
                    contender = self._benchmark(candidate_tree, corpus, run_dir / f"candidate-{corpus_name}.txt")
                    samples[corpus_name] = {}
                    for case in sorted(baseline):
                        if case not in contender:
                            raise VerificationError(f"candidate omitted benchmark case {case}")
                        base_median = statistics.median(baseline[case])
                        candidate_median = statistics.median(contender[case])
                        speedup = base_median / candidate_median
                        case_speedups.append(speedup)
                        samples[corpus_name][case] = {
                            "baseline_ns": baseline[case],
                            "candidate_ns": contender[case],
                            "speedup": round(speedup, 6),
                        }
                overall = math.exp(sum(math.log(value) for value in case_speedups) / len(case_speedups))
                no_regression = all(value >= 0.95 for value in case_speedups)
                accepted = all(item["passed"] for item in checks.values()) and overall >= 1.05 and no_regression
                failed_criteria = []
                if not all(item["passed"] for item in checks.values()):
                    failed_criteria.append("one or more correctness checks failed")
                if overall < 1.05:
                    failed_criteria.append(f"overall speedup {overall:.4f} is below 1.05")
                if not no_regression:
                    failed_criteria.append("one or more benchmark cases regressed by over 5%")
                hashes = {
                    path.name: hashlib.sha256(path.read_bytes()).hexdigest()
                    for path in sorted(run_dir.iterdir()) if path.is_file()
                }
                return {
                    "candidate_commit": candidate,
                    "base_commit": base,
                    "changed_files": changed,
                    "checks": checks,
                    "benchmark": {
                        "gomaxprocs": 1,
                        "samples_per_case": 10,
                        "benchtime": "1x",
                        "comparison": "median per case; geometric mean across cases",
                        "cases": samples,
                        "overall_speedup": round(overall, 6),
                        "minimum_speedup": 1.05,
                        "no_case_regression_below": 0.95,
                    },
                    "artifact_hashes": hashes,
                    "accepted": accepted,
                    "failed_criteria": failed_criteria,
                }
            finally:
                for tree in (candidate_tree, base_tree):
                    subprocess.run(["git", "worktree", "remove", "--force", str(tree)], cwd=self.repo, capture_output=True)

    def _resolve_commit(self, value: str) -> str:
        if not re.fullmatch(r"[0-9a-fA-F]{7,64}", value):
            raise VerificationError("commits must be hexadecimal object IDs")
        return self._run(["git", "rev-parse", "--verify", f"{value}^{{commit}}"], self.repo).stdout.strip()

    def _checked(self, label: str, command: list[str], cwd: Path, run_dir: Path, extra_env: dict[str, str] | None = None) -> dict[str, Any]:
        result = self._run(command, cwd, extra_env, check=False)
        log = run_dir / f"{label}.log"
        log.write_text(result.stdout + result.stderr)
        return {"passed": result.returncode == 0, "exit_code": result.returncode, "log": log.name}

    def _benchmark(self, tree: Path, corpus: Path, output: Path) -> dict[str, list[int]]:
        result = self._run(
            ["go", "test", "./pkg/shell/cmds", "-run", "^$", "-bench", "^BenchmarkRipgrepCorpus/", "-benchtime=1x", "-count=10"],
            tree, {"OPENLORE_RG_CORPUS": str(corpus), "GOMAXPROCS": "1"}, check=False, timeout=900,
        )
        output.write_text(result.stdout + result.stderr)
        if result.returncode:
            raise VerificationError(f"benchmark failed; see {output}")
        samples: dict[str, list[int]] = {}
        for name, ns in BENCH_RE.findall(result.stdout):
            samples.setdefault(name, []).append(int(ns))
        if not samples or any(len(values) != 10 for values in samples.values()):
            raise VerificationError(f"incomplete benchmark samples in {output}")
        return samples

    def _corpora(self) -> dict[str, Path]:
        corpora = {
            "mdn": self.repo / ".benchdata/mdn-content/files/en-us",
            "deep": self.repo / ".benchdata/deep-markdown",
            "private": self.private_corpus,
        }
        missing = [str(path) for path in corpora.values() if not path.is_dir()]
        if missing:
            raise VerificationError(f"benchmark corpora missing: {missing}")
        return corpora

    def _prepare_private_corpus(self) -> None:
        marker = self.private_corpus / ".seed-73129"
        if marker.exists():
            return
        shutil.rmtree(self.private_corpus)
        self.private_corpus.mkdir(parents=True)
        current = self.private_corpus
        for depth in range(17):
            current = current / f"private-{depth:02d}"
            current.mkdir()
            for leaf in range(11):
                ending = "\r\n" if (depth + leaf) % 3 == 0 else "\n"
                text = f"# private {depth}/{leaf}{ending}alpha-{depth}-{leaf} javascript{ending}"
                if depth == 16 and leaf == 10:
                    text += "private-sentinel-73129"
                (current / f"doc-{leaf:02d}.md").write_bytes(text.encode())
        marker.write_text("seed=73129\n")

    def _run(self, command: list[str], cwd: Path, extra_env: dict[str, str] | None = None, *, check: bool = True, timeout: int = 300) -> subprocess.CompletedProcess[str]:
        env = os.environ.copy()
        if extra_env:
            env.update(extra_env)
        result = subprocess.run(command, cwd=cwd, env=env, text=True, capture_output=True, timeout=timeout)
        if check and result.returncode:
            raise VerificationError(f"{' '.join(command)} failed: {result.stderr.strip()}")
        return result

    def _write_record(self, record: dict[str, Any]) -> None:
        atomic_write(self.records / f"{record['result_id']}.json", json.dumps(record, indent=2, sort_keys=True) + "\n")

    def _write_markdown(self, record: dict[str, Any]) -> None:
        target = self.workspace / "trusted-results" / f"{record['result_id']}.md"
        speedup = record.get("benchmark", {}).get("overall_speedup", 0)
        content = (
            f"---\nagent: {record['agent']}\nstatus: {record['status']}\n"
            f"score: {speedup}\nspeedup: {speedup}\nresult_id: {record['result_id']}\n"
            f"candidate: {record['candidate_commit']}\n---\n"
            f"# Verification {record['result_id']}\n\nAccepted: **{record.get('accepted', False)}**\n"
        )
        atomic_write(target, content)

    def _audit(self, event: str, record: dict[str, Any]) -> None:
        row = json.dumps({"at": now_iso(), "event": event, "result_id": record["result_id"], "status": record["status"]}, sort_keys=True)
        with self.audit.open("a") as stream:
            stream.write(row + "\n")
            stream.flush()
            os.fsync(stream.fileno())


def atomic_write(path: Path, content: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_name(f".{path.name}.{uuid.uuid4().hex}.tmp")
    temporary.write_text(content)
    os.replace(temporary, path)


def now_iso() -> str:
    return time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())


PRIVATE_TEST = r'''package cmds

import (
    "os"
    "testing"
)

func TestVerifierPrivateCorpus(t *testing.T) {
    root := os.Getenv("OPENLORE_PRIVATE_CORPUS")
    if root == "" { t.Fatal("private corpus missing") }
    fsys := benchmarkDiskFS{root: root}
    got, err := Ripgrep(fsys, []string{"/"}, `private-sentinel-[0-9]+`, RipgrepOptions{LineNumbers: true})
    if err != nil { t.Fatal(err) }
    if len(got) != 1 || got[0].LineNumber != 3 || got[0].Line != "private-sentinel-73129" { t.Fatalf("unexpected sentinel result: %+v", got) }
    insensitive, err := Ripgrep(fsys, []string{"/"}, "JAVASCRIPT", RipgrepOptions{CaseInsensitive: true, FilesWithMatches: true})
    if err != nil || len(insensitive) != 187 { t.Fatalf("private match count=%d err=%v", len(insensitive), err) }
    for i := 1; i < len(insensitive); i++ { if insensitive[i-1].Path >= insensitive[i].Path { t.Fatal("non-deterministic path ordering") } }
}
'''
