#!/usr/bin/env python3
"""Native qualification in the existing exclusively owned Orka CI cluster."""
import base64
import contextlib
import copy
import hashlib
import importlib.util
import io
import json
import os
import pathlib
import re
import shutil
import signal
import stat
import subprocess
import sys
import tarfile
import time
import urllib.request
import urllib.parse

import yaml

JUDGE_BASE_URL = "http://127.0.0.1:18091/v1"
JUDGE_VERSION = "aikit-qwen3.5-2b@sha256:d838c5eebf533b5b73f67cc7f4984921f87d64d28e4a52d482740b667f46b8b4"
# Pin policy v2 independently; a new evaluator requires explicit qualification.
JUDGE_POLICY_DIGEST = "2866b5c14b0efa8dd13964a40030ffd79877bba4dc00405c1121000113adf793"

MARKERS = ("kaimahi.dev/suite", "kaimahi.dev/suite-digest", "kaimahi.dev/suite-member", "kaimahi.dev/suite-renderer",
           "kaimahi.dev/suite-binding-digest", "kaimahi.dev/suite-rendered-digest")


def canonical(value):
    # This finite fixture has string keys, integral counters and ASCII operands;
    # no floating-point or non-BMP key ordering is introduced into the JCS domains.
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode()


def sha(raw):
    return hashlib.sha256(raw).hexdigest()


def frame(name, raw):
    return name.encode() + b" " + str(len(raw)).encode() + b"\n" + raw + b"\n"


def native_digest(domain, value):
    return sha(frame("rendered/000.yaml", domain.encode()) + frame("rendered/001.yaml", canonical(value)))


def fingerprint(files):
    return "sha256:" + sha(canonical({"renderer": "kmx.native-orka/v2", "layout": "kmx.native-suite-evals/v2",
                                      "files": {p: "sha256:" + sha(b) for p, b in files.items()}}))


def require(condition, code):
    if not condition:
        raise ValueError(code)


def result_digest(stdout):
    # task result uses the same safeTerminal result reader as evaluation, then
    # Fprintln adds ONE newline. Preserve any newline belonging to the answer.
    require(stdout.endswith(b"\n") and stdout[:-1].strip(), "result-format")
    return sha(stdout[:-1])


def inspect_task(doc, row, member, input_text):
    meta, spec, status = doc["metadata"], dict(doc["spec"]), doc["status"]
    require(doc["kind"] == "Task" and doc["apiVersion"] == "core.orka.ai/v1alpha1" and
            meta["name"] == row["taskName"] and meta["namespace"] == "orka-system" and meta["uid"] == row["taskUID"] and
            meta["generation"] == 1 and not meta.get("deletionTimestamp"), "task-identity")
    for key, value in {"priority": 500, "concurrencyPolicy": "Forbid", "failedRunsHistoryLimit": 1,
                       "successfulRunsHistoryLimit": 3, "startingDeadlineSeconds": 100}.items():
        if key in spec:
            require(spec.pop(key) == value, "task-default")
    require(spec == {"type": "ai", "prompt": input_text, "agentRef": {"name": member, "namespace": "orka-system"}, "resources": {}}, "task-spec")
    require(status["phase"] == "Succeeded" and status["resultRef"]["available"] is True, "task-terminal")
    return {"uid": meta["uid"], "generation": meta["generation"], "specDigest": native_digest("kmx.native-orka.spec/v1", doc["spec"])}


def inspect_pair(doc, kind, owned, subject):
    meta, status = doc["metadata"], doc["status"]
    require(doc["kind"] == kind and meta["name"] == subject["Member"] and meta["namespace"] == "orka-system" and
            meta["uid"] == owned["UID"] and meta["generation"] == owned["Generation"] and not meta.get("deletionTimestamp"), "pair-identity")
    require(native_digest("kmx.native-orka.spec/v1", doc["spec"]) == owned["SpecDigest"], "pair-spec")
    values = [subject[k] for k in ("Suite", "SuiteDigest", "Member", "Renderer", "BindingDigest", "RenderedDigest")]
    require({k: meta.get("annotations", {}).get(k) for k in MARKERS} == dict(zip(MARKERS, values)), "pair-markers")
    require(status["ready"] is True and any(c["type"] == "Ready" and c["status"] == "True" and
            c["observedGeneration"] == meta["generation"] for c in status["conditions"]), "pair-ready")


def verify_candidate(receipt, frames, cases, specs):
    """Historical proof backed by actual CREATE/GET hashes, never a deleted-source read."""
    require(receipt.get("candidateCleanup") == "complete", "candidate-cleanup")
    subject, run, rows = receipt["subject"], receipt["runID"], receipt["cases"]
    pair = {kind: receipt[kind.lower()] for kind in ("Provider", "Agent")}
    require(re.fullmatch(r"[0-9a-f]{32}", run) and subject.get("EvaluationRun") == run and
            subject.get("PhysicalMember") == "kmx-eval-" + subject["SuiteDigest"][7:23] + "-" + run, "candidate-run")
    member = subject["PhysicalMember"]
    require(all(f["verb"] != "blocked" and f.get("returnCode") == 0 for f in frames if f["verb"] != "port-forward"), "candidate-audit")
    creates = [f for f in frames if f["persistent"] and f["verb"] != "delete"]
    wanted = [("Provider", member), ("Agent", member)] + [("Task", r["taskName"]) for r in rows]
    require([(f["resource"], f["name"]) for f in creates] == wanted and all(f["verb"] == "create" for f in creates), "candidate-creates")
    require(any(f["verb"] == "create" and f["resource"] == "token" for f in frames), "candidate-token")
    markers = dict(zip(MARKERS, [subject[k] for k in ("Suite", "SuiteDigest", "Member", "Renderer", "BindingDigest", "RenderedDigest")]))
    markers["kaimahi.dev/suite-evaluation-run"] = run
    observations = {}
    require(len(rows) == len(cases), "candidate-case-count")
    for created, (kind, name) in zip(creates, wanted):
        observed = created.get("response", {})
        meta = observed.get("metadata", {})
        owned = pair[kind] if kind != "Task" else {"UID": rows[wanted.index((kind, name)) - 2]["taskUID"], "Generation": 1}
        require(observed.get("apiVersion") == "core.orka.ai/v1alpha1" and observed.get("kind") == kind and
                meta.get("name") == name and meta.get("namespace") == "orka-system" and meta.get("uid") == owned["UID"] and
                meta.get("generation") == owned["Generation"] == 1 and not meta.get("terminating") and
                meta.get("annotations") == markers, "candidate-created-identity")
        seen = [f["response"] for f in frames if f["verb"] == "get" and f.get("response", {}).get("kind") == kind and
                f["response"].get("metadata", {}).get("name") == name]
        require(bool(seen), "candidate-independent-get")
        for current in seen:
            cm = current["metadata"]
            # Cleanup may observe a same-UID finalized object at generation+1;
            # that observation is not Ready/evaluation authority.
            require(cm["uid"] == owned["UID"] and cm["namespace"] == "orka-system" and cm["annotations"] == markers and
                    (cm["generation"] == 1 or cm["terminating"] and cm["generation"] == 2) and
                    current["specDigest"] == observed["specDigest"], "candidate-get-drift")
        if kind != "Task":
            digest = native_digest("kmx.native-orka.spec/v1", specs[kind])
            require(observed["specDigest"] == owned["SpecDigest"] == digest and
                    any(s.get("ready") and not s["metadata"]["terminating"] for s in seen), "candidate-pair-spec-ready")
        else:
            case = yaml.safe_load(list(cases.values())[wanted.index((kind, name)) - 2])
            expected = {"type": "ai", "prompt": case["input"], "agentRef": {"name": member, "namespace": "orka-system"}, "resources": {}}
            for current in [observed, *seen]:
                require(current.get("taskType") == "ai" and current.get("agentRef") == expected["agentRef"] and
                        current.get("inputDigest") == sha(case["input"].encode()) and
                        current.get("taskSpecDigest") == native_digest("kmx.native-orka.spec/v1", expected), "candidate-task-input")
            require(any(s.get("phase") == "Succeeded" and s.get("resultAvailable") and not s["metadata"]["terminating"] for s in seen), "candidate-task-terminal")
        attempts = [f for f in frames if f.get("admission") and (f["resource"], f["name"]) == (kind, name)]
        require(all(f["verb"] == "create" and f["stdinDigest"] == created["stdinDigest"] for f in attempts) and
                len(attempts) <= (2 if kind == "Task" else 3), "candidate-no-resend")
        observations[(kind, name)] = observed
    require(all(not f.get("admission") or f["resource"] == "token" or (f["resource"], f["name"]) in wanted for f in frames), "candidate-admissions")
    deletes = [(i, f) for i, f in enumerate(frames) if f["verb"] == "delete"]
    require([(f["resource"], f["name"]) for _, f in deletes] ==
            [("Task", r["taskName"]) for r in rows] + [("Agent", member), ("Provider", member)], "candidate-deletes")
    for i, deletion in deletes:
        ack = deletion.get("response", {})
        observed = observations[(deletion["resource"], deletion["name"])]
        require(ack == {"apiVersion": "v1", "kind": "Status", "status": "Success"} or
                ack.get("kind") == deletion["resource"] and ack.get("apiVersion") == "core.orka.ai/v1alpha1" and
                all(ack.get("metadata", {}).get(k) == observed["metadata"][k] for k in ("name", "namespace", "uid")), "candidate-delete-ack")
        latest = [f["response"] for f in frames[:i] if f["verb"] == "get" and f.get("response", {}).get("kind") == deletion["resource"] and
                  f["response"].get("metadata", {}).get("name") == deletion["name"]]
        require(bool(latest) and deletion["preconditions"] == {"uid": observed["metadata"]["uid"],
                "resourceVersion": latest[-1]["metadata"]["resourceVersion"]} and not deletion.get("taskAttempt") and
                not deletion.get("admission"), "candidate-delete-preconditions")
    return [{"uid": observations[("Task", r["taskName"])]["metadata"]["uid"], "generation": 1,
             "specDigest": observations[("Task", r["taskName"])]["specDigest"],
             "inputDigest": observations[("Task", r["taskName"])]["inputDigest"]} for r in rows]


def owned_children(observed, task_ids):
    owned = {kind: {} for kind in ("Job", "Pod", "ConfigMap")}
    for kind in ("Job", "ConfigMap", "Pod"):
        parents = task_ids if kind != "Pod" else owned["Job"]
        parent_kind, version = ("Task", "core.orka.ai/v1alpha1") if kind != "Pod" else ("Job", "batch/v1")
        for meta in observed[kind]:
            if any(o.get("apiVersion") == version and o.get("kind") == parent_kind and o.get("controller") is True and
                   parents.get(o.get("name")) == o.get("uid") for o in meta.get("ownerReferences", [])):
                require(meta.get("namespace") == "orka-system", "child-namespace")
                require(meta["name"] not in owned[kind] or owned[kind][meta["name"]] == meta["uid"], "child-cross-uid")
                owned[kind][meta["name"]] = meta["uid"]
    return owned


def verify_child_absence(observed, current, task_ids):
    owned = owned_children(observed, task_ids)
    combined = {kind: observed[kind] + current[kind] for kind in observed}
    closure = owned_children(combined, task_ids)
    for kind, rows in current.items():
        for meta in rows:
            require(meta["uid"] not in set(closure[kind].values()) and meta.get("taskLabel") not in task_ids, "retained-child")
    return owned



def native_cases(public):
    cases = {p.name: p.read_bytes() for p in sorted((public / "eval").glob("*.yaml"))}
    require(list(cases) == ["01-capital.yaml", "02-arithmetic.yaml"], "fixture-cases")
    case = {"id": "explanation", "input": "Briefly explain why hot air rises.", "assertions": [
        {"id": "science", "type": "llmJudge", "minScore": 70, "judgeModel": "qwen-3.5-2b", "judgeVersion": JUDGE_VERSION,
         "rubric": ("Score 100 for a scientifically correct explanation that warmer air is less dense than cooler surrounding air and rises through buoyancy. "
                    "Score 0 if the answer claims warmer air is denser or gravity pulls it upward. Equivalent wording is acceptable; do not require a particular sentence.")}]}
    cases["03-explanation.yaml"] = yaml.safe_dump(case).encode()
    return dict(sorted(cases.items()))


def case_definitions(case):
    return [{"id": "expectContains." + str(i), "type": "contains", "value": value}
            for i, value in enumerate(case.get("expectContains", []))] + case.get("assertions", [])


def packaged_case(raw):
    # Mirrors KMX case packaging.
    case = yaml.safe_load(raw)
    doc = {"schemaVersion": "1.0.0-draft", "mediaType": "application/vnd.agentsuite.eval.case.v1+json",
           "id": case["id"], "input": case["input"], "assertions": case_definitions(case)}
    doc.update({k: case[k] for k in ("runs", "minPasses") if k in case})
    return canonical(doc)


def packaged_case_path(member, raw):
    return "evals/" + member + "/cases/" + yaml.safe_load(raw)["id"] + ".json"


def verify_cases(rows, files, member):
    require(len(rows) == len(files) and bool(rows), "case-coverage")
    for row, (name, raw) in zip(rows, sorted(files.items())):
        case = yaml.safe_load(raw)
        definitions = case_definitions(case)
        expected = []
        require(len(row["assertions"]) == len(definitions), "case-assertions")
        for i, definition in enumerate(definitions):
            kind = definition["type"]
            require(kind in {"contains", "notContains", "regex", "llmJudge"}, "case-evaluator")
            operand = definition["rubric" if kind == "llmJudge" else "pattern" if kind == "regex" else "value"]
            digest = frame("type", kind.encode()) + frame("operand", operand.encode())
            assertion = {"id": definition["id"], "type": kind, "evaluatorVersion": "kmx.text-assertions/v1", "verdict": "pass"}
            if kind == "llmJudge":
                require(definition["judgeModel"] == "qwen-3.5-2b" and definition["judgeVersion"] == JUDGE_VERSION and
                        type(definition["minScore"]) is int and definition["minScore"] == 70, "judge-pin")
                evidence = row["assertions"][i].get("judge")
                require(isinstance(evidence, dict), "judge-evidence")
                scores = evidence.get("scores")
                require(isinstance(scores, list) and len(scores) == 2 and all(type(s) is int and 70 <= s <= 100 for s in scores), "judge-scores")
                matches = evidence.get("modelMatches")
                require(isinstance(matches, list) and len(matches) == 2 and
                        all(m in ("requested", "version-qualified") for m in matches), "judge-model-matches")
                configuration = sha(b"".join(frame(k, v.encode()) for k, v in (
                    ("endpoint", JUDGE_BASE_URL), ("model", definition["judgeModel"]),
                    ("modelVersion", definition["judgeVersion"]), ("requestStyle", "openai-chat-completions-v1"),
                    ("evaluatorDigest", JUDGE_POLICY_DIGEST))))
                expected_judge = {"requestStyle": "openai-chat-completions-v1", "model": definition["judgeModel"], "modelVersion": definition["judgeVersion"], "provenance": "user-declared",
                                  "evaluatorDigest": JUDGE_POLICY_DIGEST, "configurationDigest": configuration, "minScore": 70, "scores": scores, "modelMatches": matches}
                require(evidence == expected_judge, "judge-identity")
                digest += b"".join(frame(k, v.encode()) for k, v in (
                    ("minScore", "70"), ("judgeModel", definition["judgeModel"]),
                    ("judgeVersion", definition["judgeVersion"]), ("evaluatorDigest", JUDGE_POLICY_DIGEST)))
                assertion.update(evaluatorVersion="kmx.llm-judge/v2", judge=expected_judge)
            assertion["definitionDigest"] = sha(digest)
            expected.append(assertion)
        require(row["id"] == case["id"] and row["path"] == packaged_case_path(member, raw) and row["caseDigest"] == sha(packaged_case(raw)) and
                row["inputDigest"] == sha(case["input"].encode()) and row["verdict"] == "pass" and row["assertions"] == expected, "case-metadata")


def private_model_route(source, destination, internal_ip, port):
    networks = [node["NetworkSettings"]["Networks"]["kind"] for node in (source, destination)]
    require(networks[0]["NetworkID"] and networks[0]["NetworkID"] == networks[1]["NetworkID"] and
            networks[0]["IPAddress"] == internal_ip and internal_ip != networks[1]["IPAddress"], "model-private-network")
    require(30000 <= port <= 32767 and all(not node["NetworkSettings"].get("Ports", {}).get(str(port) + "/tcp")
            for node in (source, destination)), "model-host-mapping")
    return "http://" + internal_ip + ":" + str(port) + "/v1"



def release_record_name(suite, member):
    return "kmx-release-" + sha(("kmx.release-state/v1\x00" + suite + "\x00" + member + "\x00orka").encode())[:40]


def status_get_args(args, context, namespace, record=None):
    # Finite status-only boundary: no API proxy, Secret, POST, admission or setup.
    if record and args == ["--context", context, "--request-timeout=10s", "-n", namespace, "get", "configmap", record, "--ignore-not-found", "-o", "json"]:
        return
    require(args[:5] == ["--context", context, "--request-timeout=10s", "get", "--raw"] and len(args) == 6, "status-command")
    path = urllib.parse.urlsplit(args[5])
    require(not path.scheme and not path.netloc and not path.fragment, "status-path")
    discovery = {"/apis", "/apis/core.orka.ai/v1alpha1", "/apis/kagent.dev/v1alpha2",
                 "/api/v1/namespaces/kube-system", "/api/v1/namespaces/" + namespace}
    if path.path in discovery:
        require(not path.query, "status-discovery-query")
        return
    prefixes = {"/apis/core.orka.ai/v1alpha1": {"agents", "providers", "tasks"},
                "/apis/kagent.dev/v1alpha2": {"agents", "modelconfigs"},
                "/apis/apps/v1": {"deployments", "replicasets"}, "/api/v1": {"services", "pods"}}
    for prefix, kinds in prefixes.items():
        start = prefix + "/namespaces/" + namespace + "/"
        if not path.path.startswith(start):
            continue
        parts = path.path[len(start):].split("/")
        require(parts[0] in kinds and len(parts) in (1, 2), "status-resource")
        query = urllib.parse.parse_qs(path.query, keep_blank_values=True)
        if len(parts) == 2:
            require(parts[0] != "tasks" and re.fullmatch(r"[a-z0-9][-a-z0-9.]{0,252}", parts[1]) and not path.query, "status-item")
        else:
            require(query.get("limit") == ["128"] and set(query) <= {"limit", "continue", "labelSelector"} and
                    all(len(v) == 1 and 0 < len(v[0]) <= 4096 for v in query.values()), "status-list")
        return
    raise ValueError("status-path")


def status_kubectl(args):
    audit = pathlib.Path(os.environ["KMX_STATUS_AUDIT"])
    def record(frame):
        with audit.open("a") as stream:
            stream.write(json.dumps(frame, sort_keys=True) + "\n")
    try:
        status_get_args(args, os.environ["KMX_STATUS_CONTEXT"], os.environ["KMX_STATUS_NAMESPACE"], os.environ.get("KMX_STATUS_RECORD"))
    except Exception:
        record({"stage": "blocked"})
        return 1
    record({"stage": "intent", "args": args})
    result = subprocess.run([os.environ["KMX_STATUS_KUBECTL"], *args], stdin=subprocess.DEVNULL)
    record({"stage": "complete", "args": args, "returnCode": result.returncode})
    return result.returncode


def verify_status(report, table, placement, evidence, run_id):
    summary = placement["summary"]
    require(report.get("version") == 1 and report.get("destination") == "qualified" and
            report.get("complete") is True and report.get("errors") == [] and
            report.get("target") == summary["target"] and isinstance(report.get("candidates"), list), "status-report")
    rows = [r for r in report["agents"] if r.get("runtime") == summary["runtime"] and r.get("name") == summary["member"]]
    require(len(rows) == 1, "status-agent")
    row = rows[0]
    require(row["kind"] == "Agent" and all(row.get(k) == summary[k] for k in ("runtime", "suite", "member", "suiteDigest")) and
            row["readiness"]["state"] == "ready", "status-agent-identity-ready")
    expected_specs = {r["kind"]: r["specDigest"] for r in summary["resources"]}
    def provenance(value):
        require(value.get("state") == "recorded" and isinstance(value.get("record"), dict), "status-recorded")
        record = value["record"]
        expected = {"version": placement.get("provenanceVersion", 1), "suiteDigest": summary["suiteDigest"], "receiptDigest": "sha256:" + sha(evidence),
                    "receiptRunID": run_id, "evaluation": summary["sourceTarget"], "target": summary["target"],
                    "gate": "promotion", "evaluatedOnTarget": False, "resourceSpecs": expected_specs}
        require(all(record.get(k) == v for k, v in expected.items()) and re.fullmatch(r"[0-9a-f]{32}", record.get("liftID", "")),
                "status-evidence")
        require(re.fullmatch(r"\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d+)?Z", record.get("attemptedAt", "")), "status-attempt")
        time.strptime(record["attemptedAt"].split(".")[0].removesuffix("Z"), "%Y-%m-%dT%H:%M:%S")
        require("actor" in record and (record["actor"] is None or isinstance(record["actor"], str)), "status-actor")
        return record
    record = provenance(row["provenance"])
    if placement.get("provenanceVersion") == 2:
        require(row.get("releaseRecord") == placement["releaseRecord"] and row["releaseRecord"]["state"] == "completed" and
                record["liftID"] == placement["liftID"], "status-update-completion")
        require("RELEASE STATE" in table and "LIFT ATTEMPT" in table and "completed" in table and
                "v2/" + record["liftID"] in table, "status-update-table")
    require(len(row["resources"]) == len(placement["resources"]), "status-pair")
    for expected in placement["resources"]:
        resources = [r for r in row["resources"] if r["kind"] == expected["kind"] and r["name"] == expected["name"]]
        require(len(resources) == 1, "status-resource")
        resource = resources[0]
        require(resource["uid"] == expected["uid"] and resource["generation"] == expected["generation"] and
                resource["readiness"]["state"] == "ready" and resource["specDrift"] == "matched", "status-resource-ready")
        require(provenance(resource["provenance"]) == record, "status-per-object-record")
        require(expected["kind"] + "/" + expected["name"] + ": provenance=recorded" in table, "status-table-resource")
    for text in ("Destination: qualified", "AGENT/MEMBER", "complete: true", "not evaluated on this target",
                 summary["member"] + "/" + summary["member"], summary["runtime"], summary["suiteDigest"][:19],
                 record["receiptDigest"][:19], record["attemptedAt"]):
        require(text in table, "status-table")
    for text in ("unsigned", "uid continuity", "not answered or evaluated"):
        require(text in table.lower() and text in report.get("observation", "").lower(), "status-observation-caveat")


def qualify_status(work, placement, evidence, run_id, run, env, kubectl, name="destination-status"):
    root = private_root(work / name)
    workspace, wrappers, home = root / "workspace", root / "bin", root / "kmx-home"
    workspace.mkdir(mode=0o700)
    wrappers.mkdir(mode=0o700)
    # Descriptor-only resolution must not load member source or build profiles.
    descriptor = workspace / "workspace.yaml"
    descriptor.write_text("schemaVersion: 1\nname: status-proof\nmember: agents/status-proof/member.yaml\nbuildProfile: build-profiles/default.json\n")
    descriptor.chmod(0o600)
    summary = placement["summary"]
    target = {k: summary["target"][k] for k in ("context", "namespace")}
    target["runtime"] = summary["runtime"]
    config = workspace / "kmx.yaml"
    config.write_text(json.dumps({"version": 1, "targets": {"qualified": target}}) + "\n")
    config.chmod(0o600)
    original = snapshot(workspace)
    wrapper = wrappers / "kubectl"
    wrapper.write_text("#!/usr/bin/env python3\nimport os,sys\nos.execv(sys.executable, [sys.executable, " +
                       repr(str(pathlib.Path(__file__).resolve())) + ", '--status-kubectl', *sys.argv[1:]])\n")
    wrapper.chmod(0o700)
    audit = root / "kubectl.jsonl"
    env = dict(env, PATH=str(wrappers) + ":" + env["PATH"], KMX_HOME=str(home),
               KMX_STATUS_CONTEXT=target["context"], KMX_STATUS_NAMESPACE=target["namespace"],
               KMX_STATUS_KUBECTL=str(kubectl), KMX_STATUS_AUDIT=str(audit),
               KMX_STATUS_RECORD=release_record_name(summary["suite"], summary["member"]), PYTHONDONTWRITEBYTECODE="1")
    outputs = []
    for format in ("json", "table"):
        require(not home.exists(), "status-created-home")
        start = len(audit.read_text().splitlines()) if audit.exists() else 0
        result = run(["package", "status", "--to", "qualified", "--workspace", str(workspace), "-o", format], env)
        require(result.returncode == 0 and not result.stderr and not home.exists() and snapshot(workspace) == original, "status-command-or-state")
        frames = [json.loads(line) for line in audit.read_text().splitlines()][start:] if audit.exists() else []
        require(len(frames) >= 2 and len(frames) % 2 == 0, "status-missing-trace")
        for intent, complete in zip(frames[::2], frames[1::2]):
            require(intent.get("stage") == "intent" and complete.get("stage") == "complete" and
                    complete.get("returnCode") == 0 and intent.get("args") == complete.get("args"), "status-audit")
            status_get_args(intent["args"], target["context"], target["namespace"], env["KMX_STATUS_RECORD"])
        outputs.append(result.stdout)
    verify_status(json.loads(outputs[0]), outputs[1].decode(), placement, evidence, run_id)
    for format, output in zip(("json", "table"), outputs):
        (root / ("status." + format)).write_bytes(output)
    return {"formats": ["json", "table"], "readOnly": True, "freshHomeAbsent": True, "recordedPair": True}


def verify_promote_effects(frames, member):
    require(all(f["verb"] != "blocked" and f.get("returnCode", 0) == 0 and not f.get("taskAttempt") for f in frames), "promote-audit")
    effects = [f for f in frames if f.get("admission") or f["persistent"]]
    records = [f for f in frames if f.get("releaseRecord")]
    writes = [f for f in records if f["persistent"]]
    require(len(writes) == 4 and re.fullmatch(r"kmx-release-[0-9a-f]{40}", writes[0]["name"]), "promote-record-count")
    name = writes[0]["name"]
    expected = [("create", kind, member, True, False) for kind in ("Provider", "Agent")]
    expected += [("create", "ConfigMap", name, False, True), ("create", "Provider", member, True, True),
                 ("patch", "ConfigMap", name, False, True), ("create", "Agent", member, True, True),
                 ("patch", "ConfigMap", name, False, True), ("patch", "ConfigMap", name, False, True)]
    require([(f["verb"], f["resource"], f["name"], bool(f["admission"]), f["persistent"]) for f in effects] == expected, "promote-effects")
    native = [f for f in effects if not f.get("releaseRecord")]
    require(native[0]["stdinDigest"] == native[2]["stdinDigest"] and
            native[1]["stdinDigest"] == native[3]["stdinDigest"], "promote-admitted-input")
    preflight = records[:4]
    require([(f["verb"], f.get("permission"), f["name"]) for f in preflight] ==
            [("get", None, name), ("auth", "get", name), ("auth", "create", ""), ("auth", "patch", name)], "promote-record-preflight")
    require(all(f["verb"] == "get" and f["name"] == name for f in records[4:] if not f["persistent"]), "promote-record-reads")
    prior = None
    for i, f in enumerate(writes):
        response = f.get("response", {})
        meta = response.get("metadata", {})
        require(f.get("releasePhase") == ("completed" if i == 3 else "pending") and
                f.get("releaseAcknowledged") == min(i, 2) and response.get("releaseDigest") == f.get("releaseDigest") and
                response.get("releasePhase") == f.get("releasePhase") and
                response.get("releaseAcknowledged") == f.get("releaseAcknowledged") and
                meta.get("name") == name and meta.get("uid") and meta.get("resourceVersion"), "promote-record-response")
        if prior is not None:
            require(f.get("recordUID") == prior["metadata"]["uid"] == meta["uid"] and
                    f.get("recordRV") == prior["metadata"]["resourceVersion"] and
                    f.get("priorDigest") == prior["releaseDigest"], "promote-record-cas")
        prior = response


def verify_promote_completion_order(frames):
    effects = [f for f in frames if f.get("admission") or f["persistent"] or f.get("releaseRecord")]
    pending = None
    for f in effects:
        if f["stage"] == "intent":
            require(pending is None, "promote-completion-order")
            pending = f["requestID"]
        else:
            require(pending == f["requestID"] and f.get("returnCode") == 0, "promote-completion-order")
            pending = None
    require(pending is None, "promote-completion-order")
    persistent = [f for f in effects if f["persistent"] and f["stage"] == "intent"]
    require(len(persistent) == 6, "promote-completion-order")
    first = effects.index(persistent[0])
    require(sum(f["stage"] == "complete" and f.get("admission") and not f["persistent"] for f in effects[:first]) == 2,
            "promote-completion-order")


def verify_update_effects(raw, member, record):
    frames = load_audit_completed(raw)
    require(all(f.get("returnCode", 0) == 0 and not f.get("taskAttempt") and f["verb"] != "blocked" for f in frames), "update-audit")
    relevant = [f for f in raw if f.get("admission") or f["persistent"] or f.get("releaseRecord") or f.get("nativePermission")]
    pending = None
    for f in relevant:
        if f["stage"] == "intent":
            require(pending is None, "update-effect-overlap")
            pending = f["requestID"]
        else:
            require(pending == f["requestID"] and f["returnCode"] == 0, "update-effect-completion")
            pending = None
    require(pending is None, "update-effect-interrupted")
    effects = [f for f in frames if f.get("admission") or f["persistent"]]
    dry = lambda kind: ("patch", kind, member, True, False)
    live = lambda kind: ("patch", kind, member, True, True)
    checkpoint = ("patch", "ConfigMap", record, False, True)
    require([(f["verb"], f["resource"], f["name"], f["admission"], f["persistent"]) for f in effects] ==
            [dry("Provider"), dry("Agent"), checkpoint, dry("Provider"), live("Provider"), checkpoint,
             dry("Agent"), live("Agent"), checkpoint, checkpoint], "update-effects")
    writes = [f for f in effects if f.get("releaseRecord")]
    prior = None
    for index, f in enumerate(writes):
        response, meta = f.get("response", {}), f.get("response", {}).get("metadata", {})
        require(f["releasePhase"] == ("completed" if index == 3 else "pending") and
                f["releaseAcknowledged"] == min(index, 2) and
                all(response.get(k) == f[k] for k in ("releaseDigest", "releasePhase", "releaseAcknowledged")) and
                meta.get("name") == record and meta.get("uid") and meta.get("resourceVersion"), "update-record-response")
        if prior is not None:
            require(f["recordUID"] == meta["uid"] == prior["metadata"]["uid"] and
                    f["recordRV"] == prior["metadata"]["resourceVersion"] and
                    f["priorDigest"] == prior["releaseDigest"], "update-record-chain")
        prior = response
    for kind in ("Provider", "Agent"):
        patches = [f for f in effects if f["resource"] == kind]
        require(len(patches) == 3 and len({(f["desiredSpecDigest"], f["desiredAnnotationsDigest"], f["updateUID"]) for f in patches}) == 1,
                "update-admitted-intent")
    for f in effects:
        if f.get("releaseRecord"):
            continue
        response, meta = f.get("response", {}), f.get("response", {}).get("metadata", {})
        require(f.get("nativeUpdate") and meta.get("uid") == f["updateUID"] and meta.get("name") == member and
                meta.get("generation", 0) >= f["updateGeneration"] and response.get("specDigest") == f["desiredSpecDigest"],
                "update-native-response")


def load_audit_completed(raw):
    spec = importlib.util.spec_from_file_location("update_audit", pathlib.Path(__file__).with_name("suite-native-audit.py"))
    audit = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(audit)
    return audit.completed(raw)


def verify_update_summary(summary, receipt, evidence, destination, specs, previous):
    create_summary = copy.deepcopy(summary)
    create_summary["resources"] = [{"kind": r["kind"], "name": r["name"], "action": "create", "specDigest": r["specDigest"]}
                                   for r in summary["resources"]]
    verify_promote_summary(create_summary, receipt, evidence, destination, specs)
    require(len(summary["resources"]) == len(previous["resources"]) == 2, "update-plan-pair")
    for row, old in zip(summary["resources"], previous["resources"]):
        require(row["kind"] == old["kind"] and row["name"] == old["name"] and row["action"] == "update" and
                row["uid"] == old["uid"] and row["generation"] == old["generation"] and
                re.fullmatch(r"[a-zA-Z0-9._:-]{1,128}", row.get("resourceVersion", "")), "update-plan-baseline")


def verify_update_result(result, receipt, evidence, destination, specs, previous, record, seen):
    verify_update_summary(result["summary"], receipt, evidence, destination, specs, previous)
    require(result["state"] == "ready" and result.get("provenanceState") == "recorded" and
            re.fullmatch(r"[0-9a-f]{32}", result.get("liftID", "")) and result["liftID"] not in seen and
            result.get("releaseRecord", {}).get("state") == "completed" and
            result["releaseRecord"].get("name") == record and result["releaseRecord"].get("uid"), "update-completed")
    require(len(result["resources"]) == 2, "update-result-pair")
    for row, planned, old in zip(result["resources"], result["summary"]["resources"], previous["resources"]):
        require(all(row.get(k) == planned[k] for k in ("kind", "name", "action", "specDigest", "uid")) and
                row["uid"] == old["uid"] and row["outcome"] == "updated" and row["ready"] is True and
                row["generation"] >= old["generation"] and (row["kind"] != "Agent" or row["generation"] > old["generation"]),
                "update-current-ready")


def verify_model_only_change(before, after, model):
    require(before["spec"]["defaultModel"] == model and after["spec"]["defaultModel"] == "unevaluated-model" and
            after["spec"] == {**before["spec"], "defaultModel": "unevaluated-model"} and
            after["metadata"]["uid"] == before["metadata"]["uid"] and
            after["metadata"]["generation"] > before["metadata"]["generation"], "promote-model-only-change")


def verify_promote_summary(summary, receipt, evidence, destination, specs, inference="native-suite-inference"):
    source = receipt["subject"]
    target = {k[0].lower() + k[1:]: v for k, v in destination["Target"].items()}
    expected = {"suite": source["Suite"], "suiteDigest": source["SuiteDigest"], "member": source["Member"],
                "model": source["Model"], "runtime": "orka", "target": target,
                "sourceTarget": {k[0].lower() + k[1:]: v for k, v in source["Target"].items()},
                "promote": True, "gate": "promotion", "servingModel": source["Model"],
                "inference": "provider:" + inference, "bindingDigest": destination["BindingDigest"],
                "renderedDigest": destination["RenderedDigest"], "evidenceDigest": "sha256:" + sha(evidence),
                "evidenceRunID": receipt["runID"]}
    require(all(summary.get(k) == v for k, v in expected.items()), "promote-summary")
    require(re.fullmatch(r"sha256:[0-9a-f]{64}", summary.get("planDigest", "")), "promote-plan-digest")
    require(summary["resources"] == [{"kind": kind, "name": source["Member"], "action": "create",
            "specDigest": native_digest("kmx.native-orka.spec/v1", specs[kind])} for kind in ("Provider", "Agent")], "promote-create-plan")


def verify_promote_result(result, receipt, evidence, destination, specs):
    verify_promote_summary(result["summary"], receipt, evidence, destination, specs)
    require(result["state"] == "ready" and len(result["resources"]) == 2, "promote-ready")
    for row, planned, kind in zip(result["resources"], result["summary"]["resources"], ("Provider", "Agent")):
        require(all(row.get(k) == v for k, v in planned.items()) and row["outcome"] == "created" and row["ready"] is True and
                row["uid"] and row["uid"] != receipt[kind.lower()]["UID"] and row["generation"] == 1, "promote-created-result")
    require(result["resources"][0]["uid"] != result["resources"][1]["uid"], "promote-created-uids")


def verify_fresh_answer(stdout, case):
    result_digest(stdout)
    answer = stdout[:-1].decode("utf-8")
    assertions = [{"type": "contains", "value": v} for v in case.get("expectContains", [])] + case.get("assertions", [])
    require(bool(assertions), "answer-assertions")
    for assertion in assertions:
        kind = assertion["type"]
        require(kind in {"contains", "notContains", "regex"}, "answer-evaluator")
        passed = (assertion["value"] in answer if kind == "contains" else assertion["value"] not in answer
                  if kind == "notContains" else re.search(assertion["pattern"], answer) is not None)
        require(passed, "fresh-answer")


def verify_refusal(code, stderr, frames, reason):
    require(code == 1 and reason.encode() in stderr and not any(f["persistent"] or f.get("admission") or f.get("taskAttempt") or f.get("verb") == "blocked" or
            f.get("returnCode", 0) != 0 for f in frames), "refusal-proof")


def read_blob(layout, descriptor):
    digest = descriptor["digest"]
    require(digest.startswith("sha256:") and len(digest) == 71, "blob-descriptor")
    path = pathlib.Path(layout) / "blobs/sha256" / digest[7:]
    require(not path.is_symlink(), "blob-symlink")
    raw = path.read_bytes()
    require(len(raw) == descriptor["size"] and "sha256:" + sha(raw) == digest, "blob-digest")
    return raw


def private_root(root):
    root = pathlib.Path(root).absolute()
    require(not any(p.is_symlink() for p in [root, *root.parents]), "work-symlink")
    root.mkdir(mode=0o700)  # exclusive: never adopt a previous run
    return root


def snapshot(root):
    return {p.relative_to(root).as_posix(): p.read_bytes() for p in root.rglob("*") if p.is_file()}


def verify_transfer(layout, extracted, subject, cases):
    descriptors = json.loads((layout / "index.json").read_bytes())["manifests"]
    require(len(descriptors) == 1 and descriptors[0]["digest"] == subject["SuiteDigest"], "transfer-S")
    manifest = json.loads(read_blob(layout, descriptors[0]))
    read_blob(layout, manifest["config"])
    require(len(manifest["layers"]) == 1, "transfer-layers")
    content = read_blob(layout, manifest["layers"][0])
    files = snapshot(extracted)
    with tarfile.open(fileobj=io.BytesIO(content), mode="r:*") as archive:
        packed = {p.name.removeprefix("./"): archive.extractfile(p).read() for p in archive if p.isfile()}
    require(packed == files, "transfer-content")
    graph = json.loads(files["agentsuite.json"])
    require("sha256:" + sha(canonical(graph)) == subject["GraphDigest"], "transfer-G")
    member = subject["Member"]
    index = json.loads(files["evals/" + member + "/index.json"])
    require([ref["path"] for ref in index["cases"]] == [packaged_case_path(member, raw) for raw in cases.values()], "transfer-case-order")
    for raw in cases.values():
        require(files[packaged_case_path(member, raw)] == packaged_case(raw), "transfer-cases")
    require(not any(name.startswith("metadata/kmx/eval/") for name in files), "transfer-private-cases")
    return graph


def observed_runtime(controller, worker):
    meta, spec, status = controller["metadata"], controller["spec"], controller["status"]
    pod = spec["template"]["spec"]
    containers = [c for c in pod["containers"] if c["name"] == "controller"]
    require(len(containers) == 1, "runtime-controller")
    container = containers[0]
    flags = dict(arg[2:].split("=", 1) for arg in container["args"])
    helm = {k: meta["labels"][v] for k, v in {"Chart": "helm.sh/chart", "Instance": "app.kubernetes.io/instance",
            "App": "app.kubernetes.io/name", "ManagedBy": "app.kubernetes.io/managed-by"}.items()}
    actual = {"ControllerName": meta["name"], "ControllerUID": meta["uid"], "ControllerGeneration": meta["generation"],
              "ControllerReplicas": spec["replicas"], "ControllerServiceAccount": pod["serviceAccountName"],
              "ControllerImage": container["image"], "Mode": flags["controller-mode"], "WatchNamespace": flags["watch-namespace"],
              "AIWorkerImage": flags["ai-worker-image"], "WorkerAccount": flags["ai-worker-service-account-name"],
              "WorkerAccountUID": worker["metadata"]["uid"], "Helm": helm}
    require(not container.get("command") and not meta.get("deletionTimestamp"), "runtime-identity")
    require(actual["Mode"] == "harness-v2" and actual["WatchNamespace"] == "orka-system" and helm["Chart"] == "orka-0.2.0" and
            container["image"] == "ghcr.io/orka-agents/orka@sha256:7c1727f92d5c0cf05d464c6eb9e70b8342cb5a7a87c2e35338f051d7b85aa370" and
            actual["AIWorkerImage"] == "ghcr.io/orka-agents/orka/ai-worker@sha256:814d7149308e62730df59759c5137667cad04520c94cc50ff56e5028fdc8bcd4", "runtime-pin")
    require(status["observedGeneration"] == meta["generation"] and all(status[k] == spec["replicas"] for k in
            ("replicas", "updatedReplicas", "readyReplicas", "availableReplicas")) and not status.get("unavailableReplicas", 0), "runtime-ready")
    wm = worker["metadata"]
    require(wm["name"] == actual["WorkerAccount"] and wm["namespace"] == "orka-system" and not wm.get("deletionTimestamp") and
            wm["labels"]["orka.ai/worker"] == "true" and wm["labels"]["orka.ai/worker-trust"] == "ai", "runtime-worker")
    return actual


def verify_runtime(expected, controller, worker):
    require(observed_runtime(controller, worker) == expected, "runtime-identity")


def verify_lift_commit(home, result, shared):
    """Independent local completion observation; never synthesize ownership."""
    lift_id = result.get("liftID", "")
    require(re.fullmatch(r"[0-9a-f]{32}", lift_id), "commit-lift-id")
    attempt = home / "release-lifts" / lift_id
    snapshots = []
    limit = (1 << 20) + (48 << 10)
    for name in ("4.json", "committed.json"):
        path = attempt / name
        require(path.exists(), "commit-file-missing")
        info = path.lstat()
        require(stat.S_ISREG(info.st_mode) and info.st_mode & 0o077 == 0 and info.st_size <= limit, "commit-file-boundary")
        with path.open("rb") as stream:
            raw = stream.read(limit + 1)
        require(len(raw) <= limit, "commit-file-size")
        snapshots.append(raw)
    require(snapshots[0] == snapshots[1], "commit-candidate-mismatch")
    proof = json.loads(snapshots[0])
    require(proof.get("id") == lift_id and proof.get("updateProof", {}).get("recordUID") ==
            shared["metadata"]["uid"] == result["releaseRecord"]["uid"] and
            proof.get("releaseDataDigest") == "sha256:" + sha(shared["data"]["release.json"].encode()), "commit-shared-binding")


class NativeTimings:
    """Closed-phase durations only; no command data or absolute clock readings."""
    PHASES = frozenset({
        "preflight", "model", "model-warmup", "model-ready", "author", "evaluate", "evaluate-v2",
        "historical-proof", "candidate-cleanup", "transfer", "registry", "lift-denials",
        "lift-local-plan", "lift-registry-plan", "lift-local-create", "lift-local-answer",
        "lift-registry-answer", "lift-promotion-answer", "destination-status", "promote-setup",
        "promote-plan", "promote-model-mismatch", "promote-create", "promote-answer",
        "public-update-v2", "public-update-original-v1", "public-update-v2-registry",
        "public-update-rv-noop", "refusals", "artifacts",
    })
    STATUSES = frozenset({"success", "failure", "timeout"})

    def __init__(self, path=None):
        self.path = path
        self.phase, self.started = "preflight", time.monotonic()
        self.rows, self.status = [], "running"
        self.write()

    def write(self):
        if self.path is not None:
            # Replace snapshots atomically so interruption retains closed rows.
            pending = self.path.with_suffix(".tmp")
            pending.write_bytes(canonical({"schemaVersion": 1, "status": self.status, "phases": self.rows}))
            pending.replace(self.path)

    def close(self, now, status):
        self.rows.append({"phase": self.phase, "elapsedSeconds": float(now - self.started), "status": status})

    def enter(self, phase):
        require(type(phase) is str and phase in self.PHASES, "timing-phase")
        if self.status != "running" or phase == self.phase:
            return
        # Reserve one row for failure finalization; overflow must not look green.
        require(len(self.rows) < 127, "timing-limit")
        now = time.monotonic()
        self.close(now, "success")
        self.phase, self.started = phase, now
        self.write()

    def finish(self, status):
        require(type(status) is str and status in self.STATUSES, "timing-status")
        if self.status == "running":
            self.close(time.monotonic(), status)
            self.status = status
            self.write()


class Qualification:
    @property
    def phase(self):
        return self._phase

    @phase.setter
    def phase(self, phase):
        require(type(phase) is str and phase in NativeTimings.PHASES, "timing-phase")
        # __new__-constructed hermetic fixtures and destination helpers need no writer.
        timings = getattr(self, "_timings", None)
        if timings is not None:
            timings.enter(phase)
        self._phase = phase

    def __init__(self, context, work, promotion_context=None, timings=None):
        self._timings = timings
        self.phase = "preflight"
        # The owned CI fixture uses one kubeconfig file. Capture its selection
        # before private HOME/cwd would redirect kubectl and client-go defaults.
        kubeconfig = pathlib.Path(os.environ.get("KUBECONFIG") or pathlib.Path.home() / ".kube/config").absolute()
        require(kubeconfig.is_file(), "kubeconfig")
        self.context, self.work = context, private_root(work)
        self.promotion_context = promotion_context
        self.repo = pathlib.Path(__file__).resolve().parents[2]
        self.kmx, self.kubectl = str(self.repo / "bin/kmx"), shutil.which("kubectl")
        require(self.kubectl and pathlib.Path(self.kmx).is_file() and
                (not promotion_context or (self.repo / "bin/orka-native-reference").is_file()), "tools")
        self.env = {k: os.environ[k] for k in ("PATH", "TMPDIR", "RUNNER_TEMP") if k in os.environ}
        self.env.update(KUBECONFIG=str(kubeconfig), HOME=str(self.work / "home"), KMX_HOME=str(self.work / "kmx-home"),
                        KUBECACHEDIR=str(self.work / "kube-cache"), KMX_TOOLCHAIN="off", NO_COLOR="1", KAIMAHI_CONFIRM=context,
                        PYTHONDONTWRITEBYTECODE="1")
        for name in ("home", "kmx-home", "kube-cache", "receipt", "wrappers"):
            (self.work / name).mkdir(mode=0o700)
        spec = importlib.util.spec_from_file_location("native_audit", self.repo / "scripts/ci/suite-native-audit.py")
        self.audit = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(self.audit)
        wrapper = self.repo / "scripts/ci/suite-native-audit.py"
        for name in ("kubectl", "docker", "podman", "buildctl", "agentkit", "supervisord"):
            (self.work / "wrappers" / name).symlink_to(wrapper)
        self.product_env = dict(self.env, PATH=str(self.work / "wrappers") + ":" + self.env["PATH"],
                                KMX_NATIVE_KUBECTL=self.kubectl, KMX_NATIVE_CONTEXT=context,
                                KMX_NATIVE_AUDIT=str(self.work / "audit.jsonl"))

    def command(self, args, raw=None, timeout=60, product=False, success=True, env=None):
        with subprocess.Popen([str(a) for a in args], cwd=self.work, env=env if env is not None else self.product_env if product else self.env,
                              stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=True) as process:
            try:
                stdout, stderr = process.communicate(raw, timeout=timeout)
            except BaseException:
                os.killpg(process.pid, signal.SIGKILL)
                process.wait()
                raise
        (self.work / "command.stdout").write_bytes(stdout)
        (self.work / "command.stderr").write_bytes(stderr)
        if success:
            require(process.returncode == 0, "command-failed")
        return subprocess.CompletedProcess(args, process.returncode, stdout, stderr)

    def kube(self, *args, **kwargs):
        return self.command([self.kubectl, "--context", self.context, *args], **kwargs).stdout

    def get(self, resource, name):
        return json.loads(self.kube("-n", "orka-system", "get", resource, name, "-o", "json"))

    def cli(self, *args, **kwargs):
        return self.command([self.kmx, *args], product=True, **kwargs)

    def raw_frames(self):
        path = self.work / "audit.jsonl"
        return [json.loads(line) for line in path.read_bytes().splitlines()] if path.exists() else []

    def frames(self):
        return self.audit.completed(self.raw_frames())

    def provision(self):
        self.phase = "model"
        for resource, name in (("deployment", "native-suite-aikit"), ("service", "native-suite-aikit"),
                               ("configmap", "native-suite-aikit"), ("providers.core.orka.ai", "native-suite-inference")):
            require(not self.kube("-n", "orka-system", "get", resource, name, "--ignore-not-found", "-o", "name").strip(), "fixture-exists")
        for name in ("ci-native-suite",):
            for resource in ("providers.core.orka.ai", "agents.core.orka.ai"):
                require(not self.kube("-n", "orka-system", "get", resource, name, "--ignore-not-found", "-o", "name").strip(), "member-exists")
        local = self.get("providers.core.orka.ai", "local")["spec"]
        self.secret_ref = local["secretRef"]
        require(self.secret_ref["name"] == "local-provider-key", "fixture-secret")
        self.kube("-n", "orka-system", "create", "configmap", "native-suite-aikit",
                  "--from-file=config.yaml=" + str(self.repo / "scripts/ci/eval-loop-model.yaml"))
        self.kube("create", "-f", self.repo / "scripts/ci/native-suite-model.yaml")
        self.kube("-n", "orka-system", "rollout", "status", "deployment/native-suite-aikit", "--timeout=480s", timeout=480)
        self.phase = "model-warmup"
        with self.model_forward():
            warmup = (self.repo / "scripts/ci/eval-loop-warmup.json").read_bytes()
            request = urllib.request.Request(JUDGE_BASE_URL + "/chat/completions", data=warmup, headers={"Content-Type": "application/json"})
            with urllib.request.urlopen(request, timeout=180) as response:
                require(response.status == 200 and json.load(response)["choices"], "model-warmup")
        self.phase = "model-ready"
        provider = {"apiVersion": "core.orka.ai/v1alpha1", "kind": "Provider",
                    "metadata": {"name": "native-suite-inference", "namespace": "orka-system"},
                    "spec": {"type": "openai", "defaultModel": "qwen-3.5-2b", "secretRef": self.secret_ref,
                             "baseURL": "http://native-suite-aikit.orka-system.svc.cluster.local:8080/v1"}}
        self.kube("create", "-f", "-", raw=canonical(provider))
        for _ in range(60):
            selected = self.get("providers.core.orka.ai", "native-suite-inference")
            if selected.get("status", {}).get("ready") is True:
                break
            time.sleep(5)
        require(selected.get("status", {}).get("ready") is True, "provider-ready")

    @contextlib.contextmanager
    def registry(self):
        self.phase = "registry"
        name = "native-suite-registry"
        for resource in ("deployment", "service"):
            require(not self.kube("-n", "orka-system", "get", resource, name, "--ignore-not-found", "-o", "name").strip(), "registry-exists")
        labels = {"app": name}
        service = {"apiVersion": "v1", "kind": "Service", "metadata": {"name": name, "namespace": "orka-system"},
                   "spec": {"selector": labels, "ports": [{"port": 5000, "targetPort": 5000}]}}
        container = {"name": "registry", "image": "docker.io/library/registry@sha256:5b12b22f21522fe69443079df04c0f3f42cb9977857f3c20404386652a6c6d8e",
                     "ports": [{"containerPort": 5000}], "env": [{"name": "REGISTRY_HTTP_SECRET", "value": os.urandom(32).hex()}],
                     "resources": {"requests": {"cpu": "100m", "memory": "64Mi"}, "limits": {"cpu": "1", "memory": "128Mi", "ephemeral-storage": "256Mi"}},
                     "volumeMounts": [{"name": "storage", "mountPath": "/var/lib/registry"}],
                     "readinessProbe": {"httpGet": {"path": "/v2/", "port": 5000}}}
        deployment = {"apiVersion": "apps/v1", "kind": "Deployment", "metadata": {"name": name, "namespace": "orka-system"},
                      "spec": {"replicas": 1, "selector": {"matchLabels": labels}, "template": {"metadata": {"labels": labels},
                               "spec": {"containers": [container], "volumes": [{"name": "storage", "emptyDir": {"sizeLimit": "128Mi"}}]}}}}
        self.kube("create", "-f", "-", raw=canonical(service))
        self.kube("create", "-f", "-", raw=canonical(deployment))
        self.kube("-n", "orka-system", "rollout", "status", "deploy/" + name, "--timeout=120s", timeout=125)
        log = self.work / "registry-forward.log"
        with log.open("wb") as stream, subprocess.Popen([self.kubectl, "--context", self.context, "-n", "orka-system", "port-forward",
                "--address", "127.0.0.1", "svc/" + name, "18092:5000"], env=self.env, stdout=stream, stderr=stream) as forward:
            try:
                deadline, ready = time.monotonic() + 20, False
                while time.monotonic() < deadline and forward.poll() is None:
                    if b"Forwarding from 127.0.0.1:18092 -> 5000" in log.read_bytes():
                        try:
                            with urllib.request.urlopen("http://127.0.0.1:18092/v2/", timeout=2) as response:
                                ready = response.status == 200 and json.load(response) == {}
                        except OSError:
                            pass
                    if ready:
                        break
                    time.sleep(0.2)
                require(ready, "registry-forward")
                yield "127.0.0.1:18092/native-suite:v1"
            finally:
                forward.terminate()
                forward.wait(timeout=10)
        # Storage belongs only to this run's owned cluster; normal shard teardown
        # deletes the fixture. Never issue fixture repair or adopt an old registry.

    def pair_proof(self, receipt):
        for kind in ("Provider", "Agent"):
            inspect_pair(self.get(kind.lower() + "s.core.orka.ai", receipt["subject"]["Member"]), kind, receipt[kind.lower()], receipt["subject"])

    def member_tasks(self, member):
        docs = json.loads(self.kube("-n", "orka-system", "get", "tasks.core.orka.ai", "-o", "json"))["items"]
        return {d["metadata"]["name"]: d for d in docs if d.get("spec", {}).get("agentRef", {}).get("name") == member}

    def lift(self, reference, receipt_path, layout=None, plan=False, evidence=True, success=True, inference="native-suite-inference", promote=False,
             update=False, member=None, record=None):
        args = ["lift", reference, "--to-context", self.context, "--to-namespace", "orka-system", "--inference", "provider:" + inference, "-o", "json"]
        args += ["--from-layout", layout] if layout else ["--plain-http"]
        if evidence:
            args += ["--evidence", receipt_path]
        if plan:
            args += ["--plan"]
        if promote:
            args += ["--promote"]
        if update:
            require(member and record and re.fullmatch(r"kmx-release-[0-9a-f]{40}", record), "update-audit-scope")
            args += ["--update"]
            env = dict(self.product_env, KMX_NATIVE_UPDATE_MEMBER=member, KMX_NATIVE_UPDATE_RECORD=record)
            return self.cli(*args, success=success, timeout=180, env=env)
        return self.cli(*args, success=success, timeout=180)

    def fresh_run(self, transport, receipt, case):
        self.phase = "lift-" + transport + "-answer"
        member = receipt["subject"]["Member"]
        before = self.member_tasks(member)
        self.pair_proof(receipt)
        prompt = self.work / (transport + "-prompt.txt")
        prompt.write_text(case["input"])
        start = len(self.frames())
        output = self.cli("--context", self.context, "run", "--agent", member, "--namespace", "orka-system",
                          "--prompt-file", prompt, "--wait", "5m", timeout=310).stdout
        frames = self.frames()[start:]
        require(not any(f.get("returnCode", 0) != 0 for f in frames), "run-audit-failed")
        after = self.member_tasks(member)
        require(set(before).issubset(after) and len(after) == len(before) + 1, "fresh-task-count")
        name = next(iter(set(after) - set(before)))
        doc = after[name]
        require(doc["metadata"]["uid"] not in {d["metadata"]["uid"] for d in before.values()}, "fresh-task-uid")
        row = {"taskName": name, "taskUID": doc["metadata"]["uid"]}
        task = inspect_task(doc, row, member, case["input"])
        require([(f["resource"], f["name"]) for f in frames if f["persistent"]] == [("Task", name)], "run-native-writes")
        require(sum(bool(f.get("taskAttempt")) for f in frames) == 1, "run-task-attempts")
        require(all(not f.get("admission") or f["resource"] in {"token", "Task"} for f in frames), "run-admissions")
        require(any(f["verb"] == "get" and f["resource"] == "tasks.core.orka.ai" and f["name"] == name for f in frames), "run-task-read")
        verify_fresh_answer(output, case)
        self.pair_proof(receipt)
        require(inspect_task(self.get("tasks.core.orka.ai", name), row, member, case["input"]) == task, "fresh-task-drift")
        return {**task, "inputDigest": sha(case["input"].encode()), "answerDigest": result_digest(output)}

    def status(self, placement, receipt, evidence, name="destination-status"):
        previous = getattr(self, "_phase", "preflight")
        self.phase = "destination-status"
        result = qualify_status(self.work, placement, evidence, receipt["runID"],
                                lambda args, env: self.cli(*args, env=env), self.product_env, self.kubectl, name=name)
        # Keep a failed nested probe active; only successful probes resume their caller.
        self.phase = previous
        return result

    def lift_qualification(self, workspace, layout, changed_layout, changed_s, receipt_path, receipt, cases, revision=None):
        evidence, original = receipt_path.read_bytes(), snapshot(workspace)
        layout_before = snapshot(layout)
        source = receipt["subject"]
        local = "native-suite@" + source["SuiteDigest"]
        results = {}
        with self.registry() as registry:
            self.cli("package", "push", "--workspace", workspace, "--evaluation", receipt_path, "--plain-http", "--provenance=false", registry)
            if revision:
                revision["registry"] = registry.rsplit(":", 1)[0] + ":v2"
                self.cli("package", "push", "--workspace", revision["workspace"], "--evaluation", revision["path"],
                         "--plain-http", "--provenance=false", revision["registry"])
                revision["registry"] = revision["registry"].rsplit(":", 1)[0] + "@" + revision["receipt"]["subject"]["SuiteDigest"]
                pulled = self.work / "v2-registry-pulled"
                self.cli("package", "pull", "--plain-http", "--output", pulled, revision["registry"])
                verify_transfer(revision["layout"], pulled, revision["receipt"]["subject"], cases)
            registry = registry.rsplit(":", 1)[0] + "@" + source["SuiteDigest"]
            hidden = self.work / "source-withheld"
            workspace.rename(hidden)
            hidden.chmod(0)
            hidden2 = None
            if revision:
                hidden2 = self.work / "v2-source-withheld"
                revision["workspace"].rename(hidden2)
                hidden2.chmod(0)
            try:
                self.empty_member(source["Member"])
                subject, specs = self.promotion_subject(receipt)
                results["denials"] = self.lift_denials(layout, changed_layout, changed_s, receipt_path, receipt)
                for transport, reference, selected_layout in (("local", local, layout), ("registry", registry, None)):
                    self.phase = "lift-" + transport + "-plan"
                    start = len(self.frames())
                    rejected = self.lift(reference, receipt_path, selected_layout, success=False)
                    frames = self.frames()[start:]
                    verify_refusal(rejected.returncode, rejected.stderr, frames, "release lift planning refused")
                    self.audit.verify_read_only(frames)
                    self.empty_member(source["Member"])
                    start = len(self.frames())
                    result = self.lift(reference, receipt_path, selected_layout, plan=True, promote=True)
                    self.audit.verify_read_only(self.frames()[start:])
                    verify_promote_summary(json.loads(result.stdout), receipt, evidence, subject, specs)
                    results[transport] = {"plans": 1, "planCountScope": "primary-transport-only-controls-excluded",
                                          "defaultGateRefused": True, "reuseApplies": 0}
                self.phase = "lift-local-create"
                start = len(self.raw_frames())
                result = json.loads(self.lift(local, receipt_path, layout, promote=True).stdout)
                raw_frames = self.raw_frames()[start:]
                verify_promote_completion_order(raw_frames)
                verify_promote_effects(self.audit.completed(raw_frames), source["Member"])
                verify_promote_result(result, receipt, evidence, subject, specs)
                results["status"] = self.status(result, receipt, evidence)
                placement = {"subject": subject, **{row["kind"].lower(): {"UID": row["uid"], "Generation": row["generation"],
                             "SpecDigest": row["specDigest"]} for row in result["resources"]}}
                self.pair_proof(placement)
                results["local"].update(gate="promotion", sourceOnlyEvidence=True, admissions=4, nativeWrites=2, recordCreates=1, recordPatches=3,
                                        freshAnswer=self.fresh_run("local", placement, yaml.safe_load(cases[sorted(cases)[0]])))
                for transport, reference, selected_layout in (("local", local, layout), ("registry", registry, None)):
                    before = self.task_inventory()
                    start = len(self.frames())
                    rejected = self.lift(reference, receipt_path, selected_layout, promote=True, success=False)
                    frames = self.frames()[start:]
                    verify_refusal(rejected.returncode, rejected.stderr, frames, "release lift planning refused")
                    self.audit.verify_read_only(frames)
                    self.pair_proof(placement)
                    require(self.task_inventory() == before, "occupied-repeat-effects")
                    results[transport]["occupiedRepeatRefused"] = True
                if self.promotion_context:
                    results["promotion"] = self.promote_qualification(registry, receipt_path, receipt, cases, placement)
                self.pair_proof(placement)
                if revision:
                    results["updates"] = self.update_qualification(result, receipt_path, receipt, layout, revision)
            finally:
                if hidden2:
                    hidden2.chmod(0o700)
                    hidden2.rename(revision["workspace"])
                hidden.chmod(0o700)
                hidden.rename(workspace)
        require(snapshot(workspace) == original and snapshot(layout) == layout_before and receipt_path.read_bytes() == evidence, "lift-input-changed")
        return results


    def passing_revision(self, receipt, cases):
        self.phase = "evaluate-v2"
        instructions = self.work / "v2-instructions.txt"
        instructions.write_text(self.fixture_instructions + "\nKeep each response concise.\n")
        workspace, path, layout = self.work / "v2-workspace", self.work / "receipt/v2-passing.json", self.work / "v2-layout"
        self.author(receipt["subject"]["Member"], workspace, instructions, cases)
        original = snapshot(workspace)
        start = len(self.frames())
        old = self.fixture_instructions
        self.fixture_instructions = instructions.read_text()
        try:
            with self.model_forward():
                self.evaluation(workspace, path)
            second = json.loads(path.read_bytes())
            require(second["result"] == "pass" and second["runID"] != receipt["runID"] and
                    second["subject"]["SuiteDigest"] != receipt["subject"]["SuiteDigest"] and
                    second["sourceFingerprint"] == fingerprint(original) and snapshot(workspace) == original and
                    path.stat().st_mode & 0o777 == 0o600, "v2-independent-pass")
            verify_cases(second["cases"], cases, second["subject"]["Member"])
            specs, payload = self.candidate_specs(second["subject"])
            require(second["subject"]["PayloadDigest"] == payload, "v2-candidate-payload")
            frames = self.frames()[start:]
            verify_candidate(second, frames, cases, specs)
            self.phase = "candidate-cleanup"
            self.audit.verify_cleanup_order(self.raw_frames())
            self.cleanup_proof(second["subject"], frames)
            self.phase = "transfer"
            self.cli("package", "push", "--workspace", workspace, "--evaluation", path, "--to-layout", layout, "native-suite:v2")
            extracted = self.work / "v2-pulled"
            self.cli("package", "pull", "--from-layout", layout, "--output", extracted, "native-suite:v2")
            verify_transfer(layout, extracted, second["subject"], cases)
            return {"workspace": workspace, "path": path, "layout": layout, "receipt": second,
                    "instructions": instructions.read_text(), "source": original, "layoutSnapshot": snapshot(layout)}
        finally:
            self.fixture_instructions = old

    def update_qualification(self, initial, path, receipt, layout, revision):
        member = receipt["subject"]["Member"]
        record = release_record_name(receipt["subject"]["Suite"], member)
        require(initial.get("releaseRecord", {}).get("name") == record and initial.get("liftID"), "update-owned-baseline")
        verify_lift_commit(self.work / "kmx-home", initial, self.get("configmap", record))
        before = self.task_inventory()
        source_bytes, second_bytes = path.read_bytes(), revision["path"].read_bytes()
        old_instructions = self.fixture_instructions
        seen, previous, observations = {initial["liftID"]}, initial, []
        transitions = [("v2", revision["receipt"], revision["path"], revision["layout"], None, revision["instructions"]),
                       ("original-v1", receipt, path, layout, None, old_instructions),
                       ("v2-registry", revision["receipt"], revision["path"], None, revision["registry"], revision["instructions"])]
        try:
            for label, desired, evidence_path, selected_layout, reference, instructions in transitions:
                self.phase = "public-update-" + label
                self.fixture_instructions = instructions
                subject, specs = self.promotion_subject(desired)
                reference = reference or "native-suite@" + desired["subject"]["SuiteDigest"]
                options = dict(promote=True, update=True, member=member, record=record)
                home = snapshot(self.work / "kmx-home")
                start = len(self.frames())
                planned = json.loads(self.lift(reference, evidence_path, selected_layout, plan=True, **options).stdout)
                self.audit.verify_read_only(self.frames()[start:])
                require(snapshot(self.work / "kmx-home") == home, "update-plan-private-write")
                verify_update_summary(planned, desired, evidence_path.read_bytes(), subject, specs, previous)
                start = len(self.raw_frames())
                result = json.loads(self.lift(reference, evidence_path, selected_layout, **options).stdout)
                verify_update_effects(self.raw_frames()[start:], member, record)
                verify_update_result(result, desired, evidence_path.read_bytes(), subject, specs, previous, record, seen)
                require(result["releaseRecord"]["uid"] == initial["releaseRecord"]["uid"], "update-record-replaced")
                verify_lift_commit(self.work / "kmx-home", result, self.get("configmap", record))
                result["provenanceVersion"] = 2
                status = self.status(result, desired, evidence_path.read_bytes(), name="status-update-" + label)
                placement = {"subject": subject, **{r["kind"].lower(): {"UID": r["uid"], "Generation": r["generation"], "SpecDigest": r["specDigest"]}
                                                    for r in result["resources"]}}
                self.pair_proof(placement)
                seen.add(result["liftID"])
                observations.append({"version": label, "liftID": result["liftID"], "suiteDigest": desired["subject"]["SuiteDigest"],
                                     "receiptDigest": "sha256:" + sha(evidence_path.read_bytes()), "status": status,
                                     "resources": [{k: r[k] for k in ("kind", "uid", "generation", "specDigest")} for r in result["resources"]]})
                previous = result
            self.phase = "public-update-rv-noop"
            provider = self.get("providers.core.orka.ai", "native-suite-inference")
            meta = provider["metadata"]
            ops = [{"op": "test", "path": "/metadata/" + k, "value": meta[k]} for k in ("uid", "resourceVersion", "generation")]
            ops += [{"op": "test", "path": "/spec", "value": provider["spec"]},
                    {"op": "add", "path": "/status/message", "value": "Owned public update no-op RV qualification"}]
            changed = json.loads(self.kube("-n", "orka-system", "patch", "providers.core.orka.ai", "native-suite-inference",
                                         "--subresource=status", "--type=json", "--patch-file=/dev/stdin", "-o", "json", raw=canonical(ops)))
            require(changed["metadata"]["uid"] == meta["uid"] and changed["metadata"]["generation"] == meta["generation"] and
                    changed["metadata"]["resourceVersion"] != meta["resourceVersion"] and changed["spec"] == provider["spec"] and
                    changed["status"]["ready"] is True, "update-status-only-rv")
            home = snapshot(self.work / "kmx-home")
            start = len(self.frames())
            noop = json.loads(self.lift(revision["registry"], revision["path"], promote=True, update=True, member=member, record=record).stdout)
            self.audit.verify_read_only(self.frames()[start:])
            require(noop["state"] == "ready" and not noop.get("liftID") and snapshot(self.work / "kmx-home") == home and
                    len(noop["resources"]) == 2, "update-noop-writes")
            for row, old in zip(noop["resources"], previous["resources"]):
                require(row["outcome"] == "reused" and row["ready"] is True and
                        all(row.get(k) == old[k] for k in ("kind", "name", "uid", "generation", "specDigest")), "update-noop-baseline")
            status = self.status(previous, revision["receipt"], second_bytes, name="status-update-noop")
            require(self.task_inventory() == before and path.read_bytes() == source_bytes and revision["path"].read_bytes() == second_bytes,
                    "digest-update-task-or-evidence-change")
            require(snapshot(revision["layout"]) == revision["layoutSnapshot"], "v2-layout-mutated")
            return {"publicCLI": True, "committedOwnershipIndependent": True,
                    "transitions": observations, "sameDigestNoop": status, "forcedStatusOnlyRV": True,
                    "digestUpdateTaskAttempts": 0, "digestUpdateEvaluations": 0, "digestUpdatePublications": 0,
                    "workspaceStaleCompletionLiveQualified": False, "interleavingLiveQualified": False}
        finally:
            self.fixture_instructions = old_instructions

    def promote_qualification(self, reference, receipt_path, receipt, cases, source_placement):
        self.phase = "promote-setup"
        destination = Qualification(self.promotion_context, self.work / "promotion")
        member, evidence = receipt["subject"]["Member"], receipt_path.read_bytes()
        self.live_subject(receipt["subject"])
        self.pair_proof(source_placement)
        source_tasks = self.member_tasks(member)
        destination.empty_member(member)
        nodes = []
        for qualifier in (self, destination):
            inventory = json.loads(qualifier.kube("get", "nodes", "-o", "json"))["items"]
            require(len(inventory) == 1, "promote-node-count")
            node = inventory[0]
            require(node["metadata"]["name"] == qualifier.context.removeprefix("kind-") + "-control-plane", "promote-node-owner")
            addresses = [a["address"] for a in node["status"]["addresses"] if a["type"] == "InternalIP"]
            require(len(addresses) == 1, "promote-node-address")
            inspected = json.loads(self.command(["docker", "inspect", node["metadata"]["name"]]).stdout)
            require(len(inspected) == 1, "promote-node-container")
            nodes.append((addresses[0], inspected[0]))
        endpoint = private_model_route(nodes[0][1], nodes[1][1], nodes[0][0], 30891)
        name = "native-suite-promote-model"
        require(not self.kube("-n", "orka-system", "get", "service", name, "--ignore-not-found", "-o", "name").strip(), "promote-service-exists")
        self.kube("create", "-f", "-", raw=canonical({"apiVersion": "v1", "kind": "Service",
                  "metadata": {"name": name, "namespace": "orka-system"}, "spec": {"type": "NodePort",
                  "selector": {"app": "native-suite-aikit"}, "ports": [{"name": "http", "port": 8080, "targetPort": "http", "nodePort": 30891}]}}))
        observed_service = self.get("service", name)
        require(observed_service["spec"]["type"] == "NodePort" and observed_service["spec"]["ports"][0]["nodePort"] == 30891 and
                observed_service["spec"]["selector"] == {"app": "native-suite-aikit"}, "promote-service-route")
        # Keyless fixture prerequisite, independently created in the owned target;
        # never copy the source Secret or read its data into the qualifier.
        secret_name = "native-suite-provider-key"
        require(not destination.kube("-n", "orka-system", "get", "secret", secret_name, "--ignore-not-found", "-o", "name").strip(), "promote-secret-exists")
        destination.kube("create", "-f", "-", raw=canonical({"apiVersion": "v1", "kind": "Secret",
                         "metadata": {"name": secret_name, "namespace": "orka-system"}, "type": "Opaque", "stringData": {"api-key": "keyless"}}))
        for provider_name in ("native-suite-inference", "native-suite-model-mismatch"):
            model = receipt["subject"]["Model"]
            require(not destination.kube("-n", "orka-system", "get", "providers.core.orka.ai", provider_name,
                    "--ignore-not-found", "-o", "name").strip(), "promote-inference-exists")
            destination.kube("create", "-f", "-", raw=canonical({"apiVersion": "core.orka.ai/v1alpha1", "kind": "Provider",
                             "metadata": {"name": provider_name, "namespace": "orka-system"}, "spec": {"type": "openai", "defaultModel": model,
                             "baseURL": endpoint, "secretRef": {"name": secret_name, "key": "api-key"}}}))
            for _ in range(30):
                selected = destination.get("providers.core.orka.ai", provider_name)
                if selected.get("status", {}).get("ready") is True:
                    break
                time.sleep(1)
            require(selected.get("status", {}).get("ready") is True and selected["spec"]["defaultModel"] == model, "promote-inference-ready")
        destination.fixture_instructions = self.fixture_instructions
        subject, specs = destination.promotion_subject(receipt)
        require(subject["Binding"]["Provider"]["UID"] != receipt["subject"]["Binding"]["Provider"]["UID"] and
                subject["Binding"]["Secret"]["UID"] != receipt["subject"]["Binding"]["Secret"]["UID"], "promote-own-binding")
        def control(expected_subject=subject, expected_specs=specs, inference="native-suite-inference"):
            start = len(destination.frames())
            result = destination.lift(reference, receipt_path, plan=True, promote=True, inference=inference)
            destination.audit.verify_read_only(destination.frames()[start:])
            verify_promote_summary(json.loads(result.stdout), receipt, evidence, expected_subject, expected_specs, inference)
            destination.empty_member(member)

        self.phase = "promote-plan"
        control()
        alternate = "native-suite-model-mismatch"
        alternate_subject, alternate_specs = destination.promotion_subject(receipt, inference=alternate)
        control(alternate_subject, alternate_specs, alternate)
        self.phase = "promote-model-mismatch"
        before_model = destination.get("providers.core.orka.ai", alternate)
        destination.kube("-n", "orka-system", "patch", "providers.core.orka.ai", alternate, "--type=merge",
                         "-p", json.dumps({"spec": {"defaultModel": "unevaluated-model"}}))
        for _ in range(30):
            after_model = destination.get("providers.core.orka.ai", alternate)
            if after_model.get("status", {}).get("ready") is True and any(c["type"] == "Ready" and c["status"] == "True" and
                    c.get("observedGeneration") == after_model["metadata"]["generation"] for c in after_model.get("status", {}).get("conditions", [])):
                break
            time.sleep(1)
        verify_model_only_change(before_model, after_model, receipt["subject"]["Model"])
        require(after_model.get("status", {}).get("ready") is True and any(c["type"] == "Ready" and c["status"] == "True" and
                c.get("observedGeneration") == after_model["metadata"]["generation"] for c in after_model.get("status", {}).get("conditions", [])), "promote-mismatch-ready")
        tasks_before = destination.task_inventory()
        start = len(destination.frames())
        rejected = destination.lift(reference, receipt_path, inference="native-suite-model-mismatch", promote=True, success=False)
        frames = destination.frames()[start:]
        verify_refusal(rejected.returncode, rejected.stderr, frames, "release lift planning refused")
        destination.audit.verify_read_only(frames)
        require(any(f["verb"] == "get" and f["resource"] == "providers.core.orka.ai" and
                f["name"] == "native-suite-model-mismatch" for f in frames), "promote-mismatch-inspected")
        destination.empty_member(member)
        require(destination.task_inventory() == tasks_before, "promote-denial-task-drift")
        control()
        self.phase = "promote-create"
        start = len(destination.raw_frames())
        result = json.loads(destination.lift(reference, receipt_path, promote=True).stdout)
        raw_frames = destination.raw_frames()[start:]
        verify_promote_completion_order(raw_frames)
        verify_promote_effects(destination.audit.completed(raw_frames), member)
        verify_promote_result(result, receipt, evidence, subject, specs)
        status = destination.status(result, receipt, evidence)
        require(destination.task_inventory() == tasks_before, "promote-create-task")
        placement = {"subject": subject}
        for row in result["resources"]:
            placement[row["kind"].lower()] = {"UID": row["uid"], "Generation": row["generation"], "SpecDigest": row["specDigest"]}
        destination.live_subject(subject)
        destination.pair_proof(placement)
        self.phase = "promote-answer"
        answer = destination.fresh_run("promotion", placement, yaml.safe_load(cases[sorted(cases)[0]]))
        require(answer["uid"] not in {d["metadata"]["uid"] for d in source_tasks.values()}, "promote-task-source-uid")
        destination.live_subject(subject)
        self.live_subject(receipt["subject"])
        self.pair_proof(source_placement)
        require(self.member_tasks(member) == source_tasks and receipt_path.read_bytes() == evidence, "promote-source-drift")
        return {"transport": "registry", "gate": "promotion", "sourceOnlyEvidence": True, "destinationEvaluated": False,
                "sourceTarget": result["summary"]["sourceTarget"], "target": result["summary"]["target"],
                "bindingDigest": subject["BindingDigest"], "renderedDigest": subject["RenderedDigest"],
                "resources": [{k: row[k] for k in ("kind", "uid", "generation", "specDigest")} for row in result["resources"]],
                "plans": 3, "admissions": 4, "nativeWrites": 2, "recordCreates": 1, "recordPatches": 3, "taskAttempts": 0, "freshAnswer": answer, "status": status,
                "refusal": {"name": "model-mismatch", "admissions": 0, "nativeWrites": 0, "taskAttempts": 0}}

    def empty_member(self, member):
        for resource in ("providers.core.orka.ai", "agents.core.orka.ai"):
            require(not self.kube("-n", "orka-system", "get", resource, member, "--ignore-not-found", "-o", "name").strip(), "promote-member-exists")

    def task_inventory(self):
        return json.loads(self.kube("-n", "orka-system", "get", "tasks.core.orka.ai", "-o", "json"))["items"]

    def promotion_subject(self, receipt, inference="native-suite-inference"):
        # Independently render the fixture's expected payload through the existing
        # offline renderer. Digest lift still runs source-free in the private cwd;
        # this reference render grants no placement/evaluation authority.
        subject = copy.deepcopy(receipt["subject"])
        subject.pop("PhysicalMember", None)
        subject.pop("EvaluationRun", None)
        subject["Target"] = {"Context": self.context, "Namespace": "orka-system",
                             "ClusterUID": self.get_namespace_uid("kube-system"), "NamespaceUID": self.get_namespace_uid("orka-system")}
        provider = self.get("providers.core.orka.ai", inference)
        meta, spec = provider["metadata"], provider["spec"]
        bound = {"Name": meta["name"], "Namespace": meta["namespace"], "UID": meta["uid"], "Generation": meta["generation"],
                 "ResourceVersion": meta["resourceVersion"], "Type": spec["type"], "BaseURL": spec["baseURL"],
                 "SecretName": spec["secretRef"]["name"], "SecretKey": spec["secretRef"]["key"]}
        secret = json.loads(self.kube("-n", "orka-system", "get", "secret", bound["SecretName"], "-o", self.audit.SECRET_METADATA))
        require(not secret["Terminating"], "promote-secret-terminating")
        subject["Binding"] = {"Provider": bound, "Secret": {**secret["Metadata"], "Key": bound["SecretKey"], "KeyPresent": True}}
        controller = self.get("deployment", "orka-api-controller")
        subject["Runtime"] = observed_runtime(controller, self.get("serviceaccount", "orka-api-ai-worker"))
        subject["BindingDigest"] = native_digest("kmx.native-orka.binding/v1", {k: subject[k] for k in ("Model", "RequiredImages", "Binding", "Runtime")})
        # The same pure native scaffold renders the reference without a retired
        # CLI route, cluster I/O, persistent bundle or competing placement engine.
        reference = {"Name": subject["Member"], "Namespace": "orka-system", "Model": subject["Model"],
                     "ProviderType": "openai", "BaseURL": bound["BaseURL"], "SecretName": bound["SecretName"],
                     "SecretKey": bound["SecretKey"], "Instructions": self.fixture_instructions, "Description": ""}
        start = len(self.frames())
        result = self.command([self.repo / "bin/orka-native-reference"],
                              raw=canonical(reference), timeout=60)
        require(result.returncode == 0, "promote-reference-render")
        require(len(self.frames()) == start, "promote-reference-cluster-io")
        encoded = json.loads(result.stdout)
        require(isinstance(encoded, list) and len(encoded) == 3, "promote-reference-documents")
        documents = [base64.b64decode(raw, validate=True) for raw in encoded]
        parsed = [yaml.safe_load(raw) for raw in documents]
        require([doc["kind"] for doc in parsed] == ["Secret", "Provider", "Agent"], "promote-reference-documents")
        specs = {doc["kind"]: doc["spec"] for doc in parsed[1:]}
        require(subject["Model"] == "qwen-3.5-2b" and subject["RequiredImages"] == [], "reference-packaged-model")
        subject["PayloadDigest"] = sha(b"".join(frame("rendered/%03d.yaml" % i, raw) for i, raw in enumerate(documents)))
        subject["RenderedDigest"] = native_digest("kmx.native-orka.revision/v1", {**subject, "RenderedDigest": ""})
        self.live_subject(subject, inference=inference)
        return subject, specs

    def candidate_specs(self, subject):
        bound = subject["Binding"]["Provider"]
        reference = {"Name": subject["PhysicalMember"], "Namespace": "orka-system", "Model": "qwen-3.5-2b",
                     "ProviderType": "openai", "BaseURL": bound["BaseURL"], "SecretName": bound["SecretName"],
                     "SecretKey": bound["SecretKey"], "Instructions": self.fixture_instructions, "Description": ""}
        start = len(self.frames())
        result = self.command([self.repo / "bin/orka-native-reference"], raw=canonical(reference))
        require(len(self.frames()) == start, "reference-cluster-io")
        documents = [base64.b64decode(raw, validate=True) for raw in json.loads(result.stdout)]
        parsed = [yaml.safe_load(raw) for raw in documents]
        require([doc["kind"] for doc in parsed] == ["Secret", "Provider", "Agent"], "candidate-reference-documents")
        payload = sha(b"".join(frame("rendered/%03d.yaml" % i, raw) for i, raw in enumerate(documents)))
        return {doc["kind"]: doc["spec"] for doc in parsed[1:]}, payload

    def child_inventory(self, kind):
        path = {"Job": "/apis/batch/v1/namespaces/orka-system/jobs", "Pod": "/api/v1/namespaces/orka-system/pods",
                "ConfigMap": "/api/v1/namespaces/orka-system/configmaps"}[kind]
        rows, token, rv, tokens = [], "", None, set()
        for _ in range(4):
            raw = self.kube("get", "--raw", path + "?limit=128" + ("&continue=" + urllib.parse.quote_plus(token) if token else ""))
            require(len(raw) <= 4 << 20, "readback-page-bytes")
            page = self.audit.response_snapshot(raw)
            require(page["kind"] == kind + "List" and page["apiVersion"] == ("batch/v1" if kind == "Job" else "v1") and
                    bool(page["resourceVersion"]) and (rv is None or page["resourceVersion"] == rv), "readback-page")
            rv = page["resourceVersion"]
            for row in page["items"]:
                require(row.get("namespace") == "orka-system" and row.get("uid") and row.get("resourceVersion") and
                        not any(old["name"] == row["name"] or old["uid"] == row["uid"] for old in rows), "readback-child-identity")
                rows.append(row)
            token = json.loads(raw)["metadata"].get("continue", "")
            if not token:
                return rows
            require(len(token) <= 4096 and token not in tokens, "readback-page-token")
            tokens.add(token)
        raise ValueError("readback-page-bound")

    def cleanup_proof(self, subject, frames):
        member = subject["PhysicalMember"]
        for kind in ("Provider", "Agent", "Task"):
            names = [member] if kind != "Task" else [f["name"] for f in frames if f["verb"] == "create" and f["persistent"] and f["resource"] == "Task"]
            for name in names:
                require(not self.kube("-n", "orka-system", "get", kind.lower() + "s.core.orka.ai", name,
                        "--ignore-not-found=true", "-o", "json").strip(), "retained-candidate-root")
        observed = {kind: [meta for f in frames if f.get("response", {}).get("kind") == kind + "List"
                          for meta in f["response"]["items"]] for kind in ("Job", "Pod", "ConfigMap")}
        task_ids = {f["name"]: f["response"]["metadata"]["uid"] for f in frames if f["verb"] == "create" and f["persistent"] and f["resource"] == "Task"}
        owned = owned_children(observed, task_ids)
        first_delete = next((i for i, f in enumerate(frames) if f["verb"] == "delete"), len(frames))
        before_delete_jobs = [meta for f in frames[:first_delete] if f.get("response", {}).get("kind") == "JobList"
                              for meta in f["response"]["items"]]
        anchored_tasks = {o.get("uid") for meta in before_delete_jobs for o in meta.get("ownerReferences", [])
                          if o.get("apiVersion") == "core.orka.ai/v1alpha1" and o.get("kind") == "Task" and o.get("controller") is True and
                          task_ids.get(o.get("name")) == o.get("uid")}
        require(set(task_ids.values()) <= anchored_tasks, "pre-delete-job-uid-capture")
        for _ in range(2):
            current = {kind: self.child_inventory(kind) for kind in observed}
            verify_child_absence(observed, current, task_ids)
        self.live_subject(subject)  # shared namespace/inference/Secret/runtime survive unchanged
        return {kind: len(ids) for kind, ids in owned.items()}


    def get_namespace_uid(self, name):
        return json.loads(self.kube("get", "namespace", name, "-o", "json"))["metadata"]["uid"]

    def lift_denials(self, layout, changed_layout, changed_s, receipt_path, receipt):
        self.phase = "lift-denials"
        reference = "native-suite@" + receipt["subject"]["SuiteDigest"]
        subject, specs = self.promotion_subject(receipt)
        outcomes = []
        def control():
            self.empty_member(subject["Member"])
            start = len(self.frames())
            result = self.lift(reference, receipt_path, layout, plan=True, promote=True)
            self.audit.verify_read_only(self.frames()[start:])
            verify_promote_summary(json.loads(result.stdout), receipt, receipt_path.read_bytes(), subject, specs)
        for name, selected_reference, selected_layout, supplied, evidence in (
                ("missing-evidence-flag", reference, layout, False, receipt_path),
                ("missing-evidence-file", reference, layout, True, self.work / "receipt/missing.json"),
                ("different-S-same-G", "native-suite@" + changed_s, changed_layout, True, receipt_path)):
            control()  # an actually valid absent-pair baseline precedes every negative
            before = self.task_inventory()
            start = len(self.frames())
            rejected = self.lift(selected_reference, evidence, selected_layout, evidence=supplied, promote=True, success=False)
            frames = self.frames()[start:]
            verify_refusal(rejected.returncode, rejected.stderr, frames, "release lift planning refused")
            self.audit.verify_read_only(frames)
            self.empty_member(subject["Member"])
            require(self.task_inventory() == before, "refusal-live-drift")
            outcomes.append({"name": name, "admissions": 0, "nativeWrites": 0, "taskAttempts": 0})
        control()
        return outcomes


    def author(self, name, directory, instructions, cases):
        self.cli("create", name, "--workspace", directory, "--model", "qwen-3.5-2b", "--instructions", instructions)
        (directory / "eval/example.yaml").unlink()
        for filename, raw in cases.items():
            (directory / "eval" / filename).write_bytes(raw)

    @contextlib.contextmanager
    def model_forward(self):
        deadline = time.monotonic() + 30
        log = self.work / "model-forward.log"
        with log.open("wb") as stream, subprocess.Popen([self.kubectl, "--context", self.context, "-n", "orka-system", "port-forward",
                "--address", "127.0.0.1", "svc/native-suite-aikit", "18091:8080"], env=self.env, stdout=stream, stderr=stream) as forward:
            try:
                ready = False
                while time.monotonic() < deadline and forward.poll() is None:
                    if b"Forwarding from 127.0.0.1:18091 -> 8080" in log.read_bytes():
                        try:
                            with urllib.request.urlopen("http://127.0.0.1:18091/readyz", timeout=2) as response:
                                ready = response.status == 200
                        except OSError:
                            pass
                    if ready:
                        break
                    time.sleep(1)
                require(ready, "model-ready")
                yield
            finally:
                forward.terminate()
                forward.wait(timeout=10)

    def evaluation(self, directory, output, success=True):
        return self.cli("eval", "--workspace", directory, "--to-context", self.context, "--to-namespace", "orka-system",
                        "--inference", "provider:native-suite-inference", "--case-timeout", "5m", "--out", output,
                        "--judge-base-url", JUDGE_BASE_URL, timeout=660, success=success)

    def live_subject(self, subject, inference="native-suite-inference"):
        target = {"Context": self.context, "Namespace": "orka-system",
                  "ClusterUID": json.loads(self.kube("get", "namespace", "kube-system", "-o", "json"))["metadata"]["uid"],
                  "NamespaceUID": json.loads(self.kube("get", "namespace", "orka-system", "-o", "json"))["metadata"]["uid"]}
        require(subject["Target"] == target, "target-identity")
        selected = self.get("providers.core.orka.ai", inference)
        meta, spec = selected["metadata"], selected["spec"]
        provider = {"Name": meta["name"], "Namespace": meta["namespace"], "UID": meta["uid"], "Generation": meta["generation"],
                    "ResourceVersion": meta["resourceVersion"], "Type": spec["type"], "BaseURL": spec["baseURL"],
                    "SecretName": spec["secretRef"]["name"], "SecretKey": spec["secretRef"]["key"]}
        require(provider == subject["Binding"]["Provider"] and selected["status"]["ready"] is True, "binding-provider")
        secret = json.loads(self.kube("-n", "orka-system", "get", "secret", provider["SecretName"], "-o", self.audit.SECRET_METADATA))
        sm = secret["Metadata"]
        require(subject["Binding"]["Secret"] == {**sm, "Key": provider["SecretKey"], "KeyPresent": True} and not secret["Terminating"], "binding-secret")
        presence = 'go-template=secret\n{{range $key, $_ := .data}}{{if eq $key ' + json.dumps(provider["SecretKey"]) + '}}present{{end}}{{end}}'
        require(self.kube("-n", "orka-system", "get", "secret", provider["SecretName"], "-o", presence) == b"secret\npresent", "binding-key")
        runtime = subject["Runtime"]
        verify_runtime(runtime, self.get("deployment", runtime["ControllerName"]), self.get("serviceaccount", runtime["WorkerAccount"]))
        require(subject["BindingDigest"] == native_digest("kmx.native-orka.binding/v1", {k: subject[k] for k in
                ("Model", "RequiredImages", "Binding", "Runtime")}), "binding-digest")
        require(subject["RenderedDigest"] == native_digest("kmx.native-orka.revision/v1", {**subject, "RenderedDigest": ""}), "revision-digest")

    def qualify(self):
        self.provision()
        self.phase = "author"
        public = self.repo / "internal/kmx/app/testdata/live-eval-loop"
        instructions = self.work / "instructions.txt"
        instructions.write_bytes((public / "instructions.txt").read_bytes())
        cases = native_cases(public)
        workspace, receipt_path = self.work / "workspace", self.work / "receipt/passing.json"
        member = "ci-native-suite"
        self.author(member, workspace, instructions, cases)
        original = snapshot(workspace)
        require(not self.frames(), "offline-audit")
        self.fixture_instructions = instructions.read_text()
        self.phase = "evaluate"
        # The original full evaluation and the independent v2 full evaluation
        # share this one pinned CPU workload, with no additional warmup.
        with self.model_forward():
            self.evaluation(workspace, receipt_path)
        receipt = json.loads(receipt_path.read_bytes())
        require(receipt_path.stat().st_mode & 0o777 == 0o600 and receipt["result"] == "pass" and
                receipt["sourceFingerprint"] == fingerprint(original) and snapshot(workspace) == original, "receipt-source")
        subject = receipt["subject"]
        require(subject["Member"] == member and subject["Model"] == "qwen-3.5-2b" and subject["RequiredImages"] == [], "receipt-member")
        verify_cases(receipt["cases"], cases, member)
        packaged = sorted((packaged_case_path(member, raw).removeprefix("evals/"), packaged_case(raw)) for raw in cases.values())
        require(receipt["casesDigest"] == sha(b"".join(frame("eval/" + n, b) for n, b in packaged)), "case-set")
        self.phase = "historical-proof"
        self.audit.verify_cleanup_order(self.raw_frames())
        frames = self.frames()
        specs, payload = self.candidate_specs(subject)
        require(subject["PayloadDigest"] == payload, "candidate-payload")
        tasks = verify_candidate(receipt, frames, cases, specs)
        # Deletion is inside evaluation; this phase measures independent absence checks.
        self.phase = "candidate-cleanup"
        children = self.cleanup_proof(subject, frames)
        self.phase = "transfer"
        layout, extracted, reference = self.work / "layout", self.work / "pulled", "native-suite:v1"
        self.cli("package", "push", "--workspace", workspace, "--evaluation", receipt_path, "--to-layout", layout, reference)
        self.cli("package", "pull", "--from-layout", layout, "--output", extracted, reference)
        verify_transfer(layout, extracted, subject, cases)
        before = snapshot(layout)
        case_path = workspace / "eval" / sorted(cases)[0]
        changed_case = yaml.safe_load(cases[case_path.name])
        changed_case["input"] += " Answer in one word."
        case_path.write_text(yaml.safe_dump(changed_case))
        rejected = self.cli("package", "push", "--workspace", workspace, "--evaluation", receipt_path, "--to-layout", layout, reference, success=False)
        verify_refusal(rejected.returncode, rejected.stderr, [], "expected publication identity")
        require(snapshot(layout) == before, "destination-changed")
        changed_layout, changed = self.work / "changed-layout", self.work / "changed-pulled"
        self.cli("package", "push", "--unevaluated", "--workspace", workspace, "--to-layout", changed_layout, reference)
        self.cli("package", "pull", "--from-layout", changed_layout, "--output", changed, reference)
        changed_s = json.loads((changed_layout / "index.json").read_bytes())["manifests"][0]["digest"]
        # Cases are in the graph, so a case edit changes S and G.
        require(changed_s != subject["SuiteDigest"] and sha(canonical(json.loads((changed / "agentsuite.json").read_bytes()))) != subject["GraphDigest"][7:], "mutation-S-and-G")
        case_path.write_bytes(cases[case_path.name])
        revision = self.passing_revision(receipt, cases)
        lift_results = self.lift_qualification(workspace, layout, changed_layout, changed_s, receipt_path, receipt, cases, revision=revision)
        require(snapshot(revision["workspace"]) == revision["source"], "v2-source-changed")
        lift_results["candidateChildrenAbsent"] = children
        self.refusals(workspace, instructions, cases, subject)
        self.artifacts(receipt_path, receipt, tasks, lift_results)

    def refusals(self, workspace, instructions, cases, subject):
        self.phase = "refusals"
        case_path = workspace / "eval" / sorted(cases)[0]
        unsupported = yaml.safe_load(cases[sorted(cases)[0]])
        unsupported["assertions"] = [{"id": "unsupported", "type": "toolCalled", "tool": "k8s-get-resources"}]
        case_path.write_text(yaml.safe_dump(unsupported))
        start = len(self.frames())
        try:
            rejected = self.evaluation(workspace, self.work / "receipt/unsupported.json", success=False)
            verify_refusal(rejected.returncode, rejected.stderr, self.frames()[start:], "unsupported native suite evaluator")
            require(len(self.frames()) == start and not (self.work / "receipt/unsupported.json").exists(), "unsupported-kube-call")
        finally:
            case_path.write_bytes(cases[case_path.name])


    def artifacts(self, receipt_path, receipt, tasks, lift_results):
        self.phase = "artifacts"
        subject = receipt["subject"]
        artifacts = self.work / "artifacts"
        artifacts.mkdir(mode=0o700)
        summary = {"schemaVersion": 1, "suiteDigest": subject["SuiteDigest"], "graphDigest": subject["GraphDigest"],
                   "sourceFingerprint": receipt["sourceFingerprint"], "receiptDigest": sha(receipt_path.read_bytes()),
                   "caseCount": len(tasks), "requiredImageCount": 0, "destinationEvaluated": False,
                   "judgeAssertionCount": 1, "judgeSampleCount": 2, "judgeIdentityBound": True,
                   "occupiedPromotionRefusal": True, "unsupportedRefusal": True, "caseMutationRefusal": True,
                   "sourceAnswerDigestIndependent": False, "sourceAssertionsProductObserved": True,
                   "sourceJudgeScoresProductObserved": True, "sourceTaskSpecInputIndependent": True,
                   "candidateCleanupIndependent": True, "lift": lift_results}
        for i, task in enumerate(tasks):
            summary.update({"task" + str(i + 1) + k.capitalize(): v for k, v in task.items()})
        (artifacts / "qualification.json").write_bytes(canonical(summary))
        shutil.copyfile(receipt_path, artifacts / "passing.json")
        print("native qualification: historical candidate identity/input and cleanup; product-observed source assertions/judge; exact transfer; explicit promotion and fresh destination answers verified")


def main():
    qualification, timings, status, result = None, None, "failure", 1
    try:
        os.umask(0o077)
        path = pathlib.Path(os.environ["RUNNER_TEMP"]) / "native-suite-timings.json" if os.environ.get("RUNNER_TEMP") else None
        timings = NativeTimings(path)
        require(len(sys.argv) == 4 and re.fullmatch(r"kind-[a-z0-9-]+", sys.argv[1]) and
                sys.argv[1] == "kind-" + os.environ.get("KIND_CLUSTER", "") and os.environ.get("KMX_ORKA_RUNTIME_CREATED") == "1" and
                sys.argv[2] == "kind-kmx-orka-promote" and sys.argv[1] != sys.argv[2] and
                os.environ.get("KMX_ORKA_PROMOTION_CREATED") == "1", "owned-context")
        qualification = Qualification(sys.argv[1], sys.argv[3], promotion_context=sys.argv[2], timings=timings)
        qualification.qualify()
        status = "success"
        result = 0
    except BaseException as error:
        status = "timeout" if isinstance(error, (subprocess.TimeoutExpired, TimeoutError)) else "failure"
        phase = qualification.phase if qualification else "preflight"
        print("native qualification: phase=" + phase + " reason=failed", file=sys.stderr)
    finally:
        if timings is not None:
            try:
                timings.finish(status)
            except OSError:
                # An earlier running snapshot can exist, so file upload alone
                # cannot establish successful timing finalization.
                print("native qualification: timing-finalization=failed", file=sys.stderr)
                result = 1
    return result


if __name__ == "__main__":
    sys.exit(status_kubectl(sys.argv[2:]) if sys.argv[1:2] == ["--status-kubectl"] else main())
