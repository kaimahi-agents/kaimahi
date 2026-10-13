#!/usr/bin/env python3
"""Qualify the actual packaged kagent eval CLI in the existing pinned kind shard."""
import hashlib
import importlib.util
import json
import pathlib
import subprocess
import sys

spec = importlib.util.spec_from_file_location("kagent_lift_driver", pathlib.Path(__file__).with_name("kagent-lift.py"))
driver = importlib.util.module_from_spec(spec)
spec.loader.exec_module(driver)


def require(condition, reason):
    if not condition:
        raise driver.QualificationError(reason)


def cli(binary, args, refusal=None):
    # Two separately bounded cases plus create-only placement and proof reads.
    result = subprocess.run([str(binary), *args], stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=1440)
    expected = 0 if refusal is None else 1
    if result.returncode != expected or refusal is not None and refusal not in result.stderr:
        # The enclosing shard redirects all subprocess diagnostics privately.
        sys.stderr.buffer.write(result.stderr)
        raise driver.QualificationError("suite CLI exit did not match " + args[0] + " qualification expectation")
    return result


def qualify(binary, work):
    root = work / "suite-evaluation"
    instructions = work / "suite-instructions.md"
    instructions.write_text("Return the deterministic suite fixture answer. No tools.\n")
    cli(binary, ["create", "suite-eval-proof", "--workspace", str(root), "--runtime", "kagent",
                 "--model", "fixture-suite", "--instructions", str(instructions)])
    example = root / "eval/example.yaml"
    example.unlink()
    for case_id, suffix in (("one", "yaml"), ("two", "yml")):
        (root / ("eval/" + case_id + "." + suffix)).write_text(
            "id: " + case_id + "\ninput: suite case " + case_id + "\nexpectContains: ['KMX suite proof:']\n")
    bindings = work / "suite-bindings.yaml"
    bindings.write_text("apiVersion: kmx.kaimahi.dev/v1alpha1\nkind: KagentBindings\nnamespace: agents\n"
                        "modelConfig:\n  provider: openai\n  baseURL: http://lift-fixture.agents.svc.cluster.local:9000/v1\n"
                        "  secretRef: {name: lift-model-key, key: api-key}\n")
    receipt = work / "suite-passing.json"
    args = ["eval", "--workspace", str(root), "--to-context", driver.CONTEXT,
            "--bindings", str(bindings), "--out", str(receipt), "--case-timeout", "9m"]
    with driver.forward("lift-fixture", 9000) as fixture:
        before = driver.http_json(fixture + "/evidence")
        result = cli(binary, args)
        after = driver.http_json(fixture + "/evidence")
        require(after["model_calls"] - before["model_calls"] == 2
                and after["terminal_answers"] - before["terminal_answers"] == 2
                and after["tool_calls"] == before["tool_calls"]
                and after["judge_calls"] == before["judge_calls"], "suite evaluation native counts differ")
        rows = after["cases"][len(before["cases"]):]
        require(len(rows) == 2 and all((r["model_calls"], r["tool_calls"], r["terminal_answers"], r["user_turns"]) == (1, 0, 1, 1) for r in rows),
                "suite cases are not single fresh tool-free turns")
        evidence = json.loads(receipt.read_bytes())
        require(evidence.get("domain") == "kmx.native-suite.passing/v1" and evidence.get("executionMode") == "native-kagent"
                and evidence.get("result") == "pass" and [c["id"] for c in evidence.get("cases", [])] == ["one", "two"],
                "suite passing receipt coverage/runtime differs")
        require("subject" not in evidence and "provider" not in evidence and "agent" not in evidence,
                "Orka proof placeholders appeared in kagent evidence")
        for case, row in zip(evidence["cases"], rows):
            require(case["inputDigest"].removeprefix("sha256:") == row["input_sha256"]
                    and case["answerDigest"].removeprefix("sha256:") == row["answer_sha256"], "suite case attribution/hash differs")
        require(b"KMX suite proof:" not in receipt.read_bytes() and b"suite case one" not in result.stdout + result.stderr,
                "suite payload escaped identity-only output")
        cli(binary, ["eval", "diff", str(receipt), str(receipt)])
        layout = work / "suite-layout"
        cli(binary, ["push", "--workspace", str(root), "--evaluation", str(receipt), "--to-layout", str(layout), "suite:v1"])
        # A new attempt cannot adopt the already occupied target or resend Tasks.
        denied = work / "suite-denied.json"
        cli(binary, args[:args.index("--out") + 1] + [str(denied)] + args[args.index("--out") + 2:], refusal=b"suite Kagent target occupied without complete caller-retained proof; no adoption or updates")
        require(not denied.exists() and driver.http_json(fixture + "/evidence") == after, "occupied eval exported evidence or resent native traffic")
        # Supplementary case bytes change S even when public graph G is unchanged.
        (root / "eval/two.yml").write_text("id: two\ninput: changed case\nexpectContains: ['KMX suite proof:']\n")
        stale_layout = work / "stale-layout"
        cli(binary, ["push", "--workspace", str(root), "--evaluation", str(receipt), "--to-layout", str(stale_layout), "suite:v1"], refusal=b"prepared suite does not match the expected publication identity")
        require(not stale_layout.exists(), "stale suite evidence authorized publication")
    driver.require(receipt.stat().st_mode & 0o777 == 0o600, "suite passing receipt is not private")
    (work / "suite-qualification.json").write_text(json.dumps({"runtime": "kagent", "version": "v0.10.3", "engine": "go",
        "executionMode": "native-kagent", "cases": 2, "modelCalls": 2, "toolCalls": 0, "judgeCalls": 0,
        "receiptSHA256": hashlib.sha256(receipt.read_bytes()).hexdigest()}, sort_keys=True) + "\n")


if __name__ == "__main__":
    try:
        qualify(pathlib.Path(sys.argv[1]), pathlib.Path(sys.argv[2]))
    except Exception as error:
        detail = str(error) if isinstance(error, driver.QualificationError) else type(error).__name__
        print("packaged kagent suite evaluation qualification failed: " + detail, file=sys.stderr)
        raise SystemExit(1) from None
