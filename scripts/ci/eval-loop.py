#!/usr/bin/env python3
"""Run the public toy bundle quietly; publish only receipt/report evidence."""
from __future__ import annotations

import argparse
import json
from pathlib import Path
import shutil
import subprocess
import sys

CASE_IDS = {"capital-france", "arithmetic"}


class GateError(Exception):
    """Fixed CI diagnostic, without captured provider or CLI payloads."""


def evaluation_path(bundle: Path) -> Path:
    paths = list((bundle / "receipts").glob("eval-*.json"))
    if len(paths) != 1:
        raise GateError("expected exactly one evaluation receipt")
    return paths[0]


def check_cases(document: dict) -> list:
    cases = document.get("cases", [])
    if len(cases) != len(CASE_IDS) or {c.get("id") for c in cases} != CASE_IDS:
        raise GateError("evidence does not cover the complete toy case set")
    return cases


def check_evaluation(receipt: dict) -> None:
    cases = check_cases(receipt)
    if (receipt.get("result") != "pass" or receipt.get("fullCaseSet") is not True
            or any(c.get("verdict") != "pass" for c in cases)):
        raise GateError("evaluation gate did not pass")


def check_verification(report: dict) -> None:
    cases = check_cases(report)
    if (report.get("result") != "equivalent"
            or any(c.get("status") != "equivalent" or c.get("modelCalls") != 0
                   for c in cases)):
        raise GateError("verification requires equivalent cases and zero model calls")


def report_failed_cases(receipt: dict, output: str) -> None:
    # Non-TTY kmx output has a heading and verdict around each answer. Refuse
    # ambiguous/missing boundaries instead of dumping unrelated passing answers.
    lines = output.splitlines()
    for case in receipt.get("cases", []):
        case_id = case.get("id")
        if case_id not in CASE_IDS or case.get("verdict") == "pass":
            continue
        headings = [i for i, line in enumerate(lines) if line == "case " + case_id]
        answer = None
        if len(headings) == 1 and case.get("verdict") == "fail":
            start = headings[0] + 1
            end = next((i for i in range(start, len(lines))
                        if lines[i].startswith("case ") or lines[i].startswith("evaluation ")),
                       len(lines))
            verdicts = [i for i in range(start, end) if lines[i] == case_id + ": fail"]
            if len(verdicts) == 1:
                answer = "\n".join(lines[start:verdicts[0]])
        if answer is None:
            print(f"{case_id}: {case.get('verdict')} (answer unavailable)", file=sys.stderr)
        else:
            # JSON escaping keeps terminal escapes and GitHub workflow commands
            # inert, including answers with newlines. Only this public toy opts in.
            print(f"{case_id} answer: {json.dumps(answer, ensure_ascii=True)}", file=sys.stderr)


def run(args: argparse.Namespace) -> None:
    if args.mode == "evaluate":
        command = [args.kmx, "agent", "evaluate", str(args.bundle),
                   "--sessions", args.sessions, "--case-timeout", "2m"]
    else:
        receipt_path = evaluation_path(args.bundle)
        command = [args.kmx, "agent", "verify", str(receipt_path),
                   "--sessions", args.sessions, "--timeout", "1m"]
    try:
        result = subprocess.run(command, capture_output=True, text=True,
                                encoding="utf-8", errors="replace", timeout=300)
    except subprocess.TimeoutExpired as exc:
        raise GateError("kmx exceeded the CI deadline") from exc
    receipt_path = evaluation_path(args.bundle)
    receipt = json.loads(receipt_path.read_text())
    args.artifacts.mkdir(parents=True, exist_ok=True)
    shutil.copy2(receipt_path, args.artifacts / receipt_path.name)
    if args.mode == "evaluate":
        if result.returncode != 0 or receipt.get("result") != "pass":
            report_failed_cases(receipt, result.stdout)
        if result.returncode != 0:
            raise GateError("kmx evaluation failed; see case diagnostics above")
        check_evaluation(receipt)
        print("evaluation: 2/2 cases passed")
    else:
        report_path = receipt_path.with_name("verify-" + receipt_path.name + ".json")
        report = json.loads(report_path.read_text())
        shutil.copy2(report_path, args.artifacts / report_path.name)
        if result.returncode != 0:
            raise GateError("kmx verification failed")
        check_evaluation(receipt)
        check_verification(report)
        print("verification: 2/2 cases equivalent; model calls: 0 (provider stopped)")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("mode", choices=("evaluate", "verify"))
    parser.add_argument("--kmx", required=True)
    parser.add_argument("--bundle", required=True, type=Path)
    parser.add_argument("--sessions", required=True)
    parser.add_argument("--artifacts", required=True, type=Path)
    args = parser.parse_args()
    try:
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
