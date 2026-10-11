#!/usr/bin/env python3
"""Run checkout-bound evals quietly and retain only receipt/report evidence."""
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys


class GateError(Exception):
    """Fixed CI diagnostic, without captured provider or CLI payloads."""


def evaluation_path(bundle: Path, sessions: str) -> Path:
    # Same domain as writeSessionsEvaluationReceipt; never select another target.
    key = hashlib.sha256(("agentsessions\0" + sessions + "\0chat").encode()).hexdigest()
    return bundle / "receipts" / ("eval-" + key + ".json")


def git(bundle: Path, *args: str) -> bytes:
    try:
        return subprocess.run(["git", "-C", str(bundle), *args], capture_output=True,
                              check=True, timeout=10).stdout
    except (OSError, subprocess.SubprocessError) as exc:
        raise GateError("bundle source revision could not be verified") from exc


def source_revision(bundle: Path) -> str:
    root = Path(os.fsdecode(git(bundle, "rev-parse", "--show-toplevel")).strip())
    try:
        relative = bundle.resolve().relative_to(root.resolve())
    except ValueError as exc:
        raise GateError("bundle must be inside the tested Git checkout") from exc
    prefix = relative.as_posix()
    if prefix == ".":
        prefix = ""
    else:
        prefix += "/"
    committed = {os.fsdecode(p) for p in git(root, "ls-tree", "-r", "--name-only", "-z", "HEAD").split(b"\0") if p}
    eval_dir = prefix + "eval/"
    expected = {p for p in committed if p.startswith(eval_dir)
                and "/" not in p[len(eval_dir):] and p.endswith(".yaml")}
    actual = {prefix + "eval/" + p.name for p in (bundle / "eval").glob("*.yaml")}
    if not expected or actual != expected or prefix + "agent.yaml" not in committed:
        raise GateError("agent and complete eval case set must be tracked in HEAD")
    sources = sorted(expected | {prefix + "agent.yaml"})
    for name in sources:
        path = root / name
        if path.is_symlink() or not path.is_file() or path.read_bytes() != git(bundle, "show", "HEAD:" + name):
            raise GateError("agent and eval cases must match the tested commit")
    # Check the index too; byte comparison above defeats skip-worktree hints.
    git(bundle, "diff", "--cached", "--quiet", "HEAD", "--", "agent.yaml", "eval")
    return git(bundle, "rev-parse", "--verify", "HEAD").decode().strip()


def check_cases(document: dict, expected_ids: set[str] | None = None) -> list:
    cases = document.get("cases")
    if not isinstance(cases, list) or not cases or any(not isinstance(c, dict) for c in cases):
        raise GateError("evidence has no complete case set")
    ids = [c.get("id") for c in cases]
    if any(not isinstance(i, str) or not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]{0,62}", i) for i in ids):
        raise GateError("evidence has invalid case identities")
    if len(set(ids)) != len(ids) or expected_ids is not None and set(ids) != expected_ids:
        raise GateError("evidence does not cover the same complete case set")
    return cases


def check_evaluation(receipt: dict) -> None:
    cases = check_cases(receipt)
    if (receipt.get("result") != "pass" or receipt.get("fullCaseSet") is not True
            or any(c.get("verdict") != "pass" for c in cases)):
        raise GateError("evaluation gate did not pass")


def check_verification(report: dict, expected_ids: set[str]) -> None:
    cases = check_cases(report, expected_ids)
    if (report.get("result") != "equivalent"
            or any(c.get("status") != "equivalent" or type(c.get("modelCalls")) is not int
                   or c["modelCalls"] != 0 for c in cases)):
        raise GateError("verification requires equivalent cases and zero model calls")


def report_failed_cases(receipt: dict, output: bytes) -> None:
    shapes_path = Path(__file__).resolve().parents[2] / "internal/kmx/secretshapes/shapes.json"
    shapes = json.loads(shapes_path.read_text())["shapes"]
    for case in check_cases(receipt):
        if case.get("verdict") == "pass":
            continue
        case_id = case["id"]
        answer = None
        heading = ("\ncase " + case_id + "\n").encode()
        verdict = ("\n" + case_id + ": fail\n").encode()
        # Preserve exact bytes including CRLF/trailing newlines. Ambiguous output
        # never falls back to raw CLI logs or another case's answer.
        if case.get("verdict") == "fail" and output.count(heading) == 1:
            start = output.index(heading) + len(heading)
            tail = output[start:]
            if tail.count(verdict) == 1:
                candidate = tail[:tail.index(verdict)]
                if hashlib.sha256(candidate).hexdigest() == case.get("answerSHA256"):
                    answer = candidate.decode("utf-8", errors="strict")
        if answer is not None:
            for name in {os.environ.get("MODEL_KEY_ENV", ""), "MODEL_API_KEY", "OPENAI_API_KEY"}:
                key = os.environ.get(name, "")
                if key:
                    answer = answer.replace(key, "[REDACTED]")
            if any(re.search(shape["regexp"], answer) for shape in shapes):
                answer = None
        if answer is None:
            print(f"{case_id}: answer unavailable", file=sys.stderr)
        else:
            # JSON escaping prevents ANSI escapes and workflow commands. Bound
            # logs independently of the runtime's larger committed-answer limit.
            if len(answer) > 4096:
                answer = answer[:4096] + " [truncated]"
            print(f"{case_id} answer: {json.dumps(answer, ensure_ascii=True)}", file=sys.stderr)


def read_evidence(path: Path) -> tuple[dict, bytes]:
    if path.is_symlink() or not path.is_file() or path.stat().st_size > 1024 * 1024:
        raise GateError("live eval evidence unavailable or malformed")
    raw = path.read_bytes()
    document = json.loads(raw)
    if not isinstance(document, dict):
        raise GateError("live eval evidence unavailable or malformed")
    return document, raw


def keep_evidence(path: Path, artifacts: Path) -> dict:
    document, raw = read_evidence(path)
    destination = artifacts / path.name
    # Output directory is fresh, private and owned by this invocation.
    with destination.open("wb") as stream:
        stream.write(raw)
    destination.chmod(0o600)
    return document


def invoke_kmx(command: list[str], timeout: int) -> subprocess.CompletedProcess:
    child_env = dict(os.environ, NO_COLOR="1", TERM="dumb")
    for name in {os.environ.get("MODEL_KEY_ENV", ""), "MODEL_API_KEY", "OPENAI_API_KEY"}:
        child_env.pop(name, None)
    return subprocess.run(command, capture_output=True, timeout=timeout, env=child_env)


def run(args: argparse.Namespace) -> None:
    revision = source_revision(args.bundle)
    receipt_path = evaluation_path(args.bundle, args.sessions)
    report_path = receipt_path.with_name("verify-" + receipt_path.name + ".json")
    if receipt_path.parent.is_symlink() or receipt_path.parent.exists() and not receipt_path.parent.is_dir():
        raise GateError("receipt parent must be a directory, not a link")
    if args.artifacts.is_symlink():
        raise GateError("artifact directory must not be a link")
    if args.mode == "prepare":
        args.artifacts.mkdir(mode=0o700, parents=True, exist_ok=True)
        if any(args.artifacts.iterdir()):
            raise GateError("artifact directory must be fresh for this run")
        # Clear only this endpoint's two KMX files; preserve other runtimes/targets.
        receipt_path.unlink(missing_ok=True)
        report_path.unlink(missing_ok=True)
        return
    if args.mode == "evaluate":
        command = [args.kmx, "agent", "evaluate", str(args.bundle), "--sessions", args.sessions,
                   "--case-timeout", args.case_timeout]
    else:
        command = [args.kmx, "agent", "verify", str(receipt_path), "--sessions", args.sessions,
                   "--timeout", args.verify_timeout]
    try:
        result = invoke_kmx(command, args.command_timeout)
    except subprocess.TimeoutExpired:
        # Preserve evidence already written, but a timed-out command never passes.
        if receipt_path.is_file():
            keep_evidence(receipt_path, args.artifacts)
        if args.mode == "verify" and report_path.is_file():
            keep_evidence(report_path, args.artifacts)
        raise GateError("kmx exceeded the CI deadline") from None
    receipt = keep_evidence(receipt_path, args.artifacts)
    report = keep_evidence(report_path, args.artifacts) if args.mode == "verify" else None
    identity = receipt.get("target", {}).get("identity", {})
    if (receipt.get("gitCommit") != revision or source_revision(args.bundle) != revision
            or receipt.get("target", {}).get("runtime") != "agentsessions"
            or identity.get("address") != args.sessions
            or len(check_cases(receipt)) != len(list((args.bundle / "eval").glob("*.yaml")))):
        raise GateError("receipt does not describe this checkout and complete case set")
    if args.mode == "evaluate":
        if result.returncode != 0 or receipt.get("result") != "pass":
            report_failed_cases(receipt, result.stdout)
        if result.returncode != 0:
            raise GateError("kmx evaluation failed; see case diagnostics above")
        check_evaluation(receipt)
        count = len(receipt["cases"])
        print(f"evaluation: {count}/{count} cases passed")
    else:
        if result.returncode != 0:
            raise GateError("kmx verification failed")
        check_evaluation(receipt)
        check_verification(report, {c["id"] for c in receipt["cases"]})
        count = len(receipt["cases"])
        print(f"verification: {count}/{count} cases equivalent; model calls: 0")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("mode", choices=("prepare", "evaluate", "verify"))
    parser.add_argument("--kmx", required=True)
    parser.add_argument("--bundle", required=True, type=Path)
    parser.add_argument("--sessions", required=True)
    parser.add_argument("--artifacts", required=True, type=Path)
    parser.add_argument("--case-timeout", default="2m")
    parser.add_argument("--verify-timeout", default="5m")
    parser.add_argument("--command-timeout", type=int, default=600)
    args = parser.parse_args()
    try:
        if args.command_timeout <= 0:
            raise GateError("command timeout must be positive")
        run(args)
    except GateError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1
    except (OSError, ValueError, TypeError, KeyError, AttributeError):
        print("error: live eval evidence unavailable or malformed", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
