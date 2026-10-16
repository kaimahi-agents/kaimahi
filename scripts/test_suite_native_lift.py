#!/usr/bin/env python3
"""Boundary tests for live lift qualification; not a live execution claim."""
import copy
import importlib.util
import json
import os
import pathlib
import signal
import subprocess
import sys
import tempfile
import time
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[1]


def load(name):
    spec = importlib.util.spec_from_file_location(name.replace("-", "_"), ROOT / "scripts/ci" / (name + ".py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


A = load("suite-native-audit")
Q = load("suite-native")


def task_body():
    return json.dumps({"apiVersion": "core.orka.ai/v1alpha1", "kind": "Task", "metadata": {"name": "fresh-task", "namespace": "orka-system"}, "spec": {"prompt": "PRIVATE_CANARY"}}).encode()


def pair(frame, identity="request-one", code=0):
    return [{**frame, "requestID": identity, "stage": "intent"}, {**frame, "requestID": identity, "stage": "complete", "returnCode": code}]


class AuditEffects(unittest.TestCase):
    def test_interrupted_real_wrapper_keeps_attempt_intent_without_payload(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            wrapper, real, audit = root / "kubectl", root / "real-kubectl", root / "audit.jsonl"
            wrapper.symlink_to(ROOT / "scripts/ci/suite-native-audit.py")
            real.write_text("#!/usr/bin/env python3\nimport time\ntime.sleep(30)\n")
            real.chmod(0o700)
            env = dict(os.environ, KMX_NATIVE_KUBECTL=str(real), KMX_NATIVE_CONTEXT="kind-owned", KMX_NATIVE_AUDIT=str(audit))
            process = subprocess.Popen([str(wrapper), "--context", "kind-owned", "create", "-f", "-"],
                                       stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, env=env, start_new_session=True)
            try:
                process.stdin.write(task_body())
                process.stdin.close()
                deadline = time.monotonic() + 5
                while not audit.exists() and time.monotonic() < deadline:
                    time.sleep(0.01)
                self.assertTrue(audit.exists(), "attempt must be logged before child completion")
            finally:
                os.killpg(process.pid, signal.SIGKILL)
                process.wait(timeout=5)
                process.stdout.close()
                process.stderr.close()
            raw = audit.read_text()
            self.assertNotIn("PRIVATE_CANARY", raw)
            frames = [json.loads(line) for line in raw.splitlines()]
            self.assertEqual(len(frames), 1)
            self.assertTrue(frames[0]["persistent"] and frames[0]["admission"] and frames[0]["taskAttempt"])
            with self.assertRaises(ValueError):
                A.completed(frames)

    def test_server_dryrun_is_admission_and_task_attempt(self):
        frame = A.classify(["create", "--dry-run=server", "-f", "-"], task_body())
        self.assertFalse(frame["persistent"])
        self.assertTrue(frame["admission"])
        self.assertTrue(frame["taskAttempt"])
        self.assertNotIn("PRIVATE_CANARY", json.dumps(frame))
        with self.assertRaises(ValueError):
            A.verify_read_only([frame])

    def test_token_request_is_an_admission_not_a_persistent_native_write(self):
        frame = A.classify(["create", "token", "orka-result-reader"])
        self.assertTrue(frame["admission"])
        self.assertFalse(frame["persistent"])
        self.assertFalse(frame["taskAttempt"])
        with self.assertRaises(ValueError):
            A.verify_read_only([frame])

    def test_interleaved_requests_and_owned_forward_normalize_once(self):
        one = A.classify(["get", "pods"])
        two = A.classify(["config", "view", "-o", "json"])
        forward = A.classify(["port-forward", "--address", "127.0.0.1", "svc/orka-api", "19180:8080"])
        p1, p2 = pair(one, "one"), pair(two, "two")
        frames = [p1[0], p2[0], {**forward, "requestID": "forward", "stage": "intent"}, p2[1], p1[1]]
        normalized = A.completed(frames)
        self.assertEqual(len(normalized), 3)
        self.assertEqual([f["requestID"] for f in normalized], ["one", "two", "forward"])
        A.verify_read_only([f for f in normalized if f["verb"] != "port-forward"])

    def test_missing_duplicate_or_changed_completion_cannot_qualify(self):
        frames = pair(A.classify(["get", "pods"]))
        changed = copy.deepcopy(frames)
        changed[1]["resource"] = "secret"
        for bad in (frames[:1], frames[1:], frames + [frames[1]], frames + [frames[0]], changed):
            with self.subTest(frames=bad), self.assertRaises(ValueError):
                A.completed(bad)

    def test_failed_request_and_mutation_refuse_readonly_proof(self):
        frame = A.classify(["get", "pods"])
        A.verify_read_only(A.completed(pair(frame)))
        with self.assertRaises(ValueError):
            A.verify_read_only(A.completed(pair(frame, code=1)))
        write = A.classify(["create", "-f", "-"], task_body())
        with self.assertRaises(ValueError):
            A.verify_read_only(A.completed(pair(write)))

    def test_generic_refusal_does_not_hide_successful_admission(self):
        dry = A.classify(["create", "--dry-run=server", "-f", "-"], task_body())
        with self.assertRaises(ValueError):
            Q.verify_refusal(1, b"release lift planning refused", A.completed(pair(dry)), "release lift planning refused")


class LiftProofs(unittest.TestCase):
    def setUp(self):
        self.receipt = {"runID": "run-one", "subject": {"Suite": "suite", "SuiteDigest": "sha256:" + "1" * 64,
            "Member": "member", "Model": "model", "BindingDigest": "2" * 64, "RenderedDigest": "3" * 64,
            "Target": {"Context": "kind-owned", "Namespace": "orka-system", "ClusterUID": "cluster", "NamespaceUID": "namespace"}},
            "provider": {"UID": "provider-uid", "Generation": 1, "SpecDigest": "4" * 64},
            "agent": {"UID": "agent-uid", "Generation": 1, "SpecDigest": "5" * 64}}
        s = self.receipt["subject"]
        target = {k[0].lower() + k[1:]: v for k, v in s["Target"].items()}
        self.summary = {"suite": s["Suite"], "suiteDigest": s["SuiteDigest"], "member": s["Member"], "model": s["Model"],
            "runtime": "orka", "target": target, "sourceTarget": target, "promote": False, "gate": "same-subject",
            "inference": "provider:native-suite-inference", "bindingDigest": s["BindingDigest"], "renderedDigest": s["RenderedDigest"],
            "evidenceDigest": "sha256:" + Q.sha(b"receipt"), "evidenceRunID": "run-one", "planDigest": "sha256:" + "6" * 64,
            "resources": [{"kind": kind, "name": "member", "action": "reuse", "uid": self.receipt[kind.lower()]["UID"],
                           "generation": 1, "specDigest": self.receipt[kind.lower()]["SpecDigest"]} for kind in ("Provider", "Agent")]}

    def test_same_target_requires_explicit_promotion_and_absent_canonical_creation(self):
        destination = copy.deepcopy(self.receipt["subject"])
        specs = {kind: {"type": "native"} for kind in ("Provider", "Agent")}
        summary = copy.deepcopy(self.summary)
        summary.update(promote=True, gate="promotion", servingModel="model")
        summary["resources"] = [{"kind": kind, "name": "member", "action": "create",
                                "specDigest": Q.native_digest("kmx.native-orka.spec/v1", specs[kind])} for kind in specs]
        Q.verify_promote_summary(summary, self.receipt, b"receipt", destination, specs)
        for edit in (lambda s: s.update(promote=False), lambda s: s.update(gate="same-subject"),
                     lambda s: s["resources"][0].update(action="reuse", uid="source-uid")):
            changed = copy.deepcopy(summary); edit(changed)
            with self.assertRaises(ValueError):
                Q.verify_promote_summary(changed, self.receipt, b"receipt", destination, specs)

    def test_fresh_answer_runs_authored_text_assertions_not_nonempty(self):
        case = {"expectContains": ["approved"], "assertions": [{"type": "notContains", "value": "denied"}, {"type": "regex", "pattern": "^approved"}]}
        Q.verify_fresh_answer(b"approved result\n", case)
        for answer in (b"error result\n", b"approved denied\n", b"prefix approved\n", b"approved result", b"\n"):
            with self.subTest(answer=answer), self.assertRaises(ValueError):
                Q.verify_fresh_answer(answer, case)



    def test_source_withheld_journey_plans_two_transports_but_creates_once_then_refuses_occupied(self):
        import contextlib
        fixture = LiftProofs(); fixture.setUp()
        receipt = fixture.receipt
        # Candidate fields survive only in source evidence, never the canonical destination.
        receipt["subject"].update(PhysicalMember="deleted-candidate", EvaluationRun="a" * 32)
        destination = copy.deepcopy(receipt["subject"])
        destination.pop("PhysicalMember"); destination.pop("EvaluationRun")
        specs = {kind: {"type": "native"} for kind in ("Provider", "Agent")}
        summary = copy.deepcopy(fixture.summary)
        summary.update(promote=True, gate="promotion", servingModel="model")
        summary["resources"] = [{"kind": kind, "name": "member", "action": "create",
                                "specDigest": Q.native_digest("kmx.native-orka.spec/v1", specs[kind])} for kind in specs]
        result = {"summary": summary, "state": "ready", "resources": [{**row, "outcome": "created", "ready": True,
                   "uid": "canonical-" + row["kind"], "generation": 1} for row in summary["resources"]]}
        def effect_frames():
            frames = []
            for i, (kind, dry) in enumerate((("Provider", True), ("Agent", True), ("Provider", False), ("Agent", False))):
                doc = {"apiVersion": "core.orka.ai/v1alpha1", "kind": kind, "metadata": {"name": "member", "namespace": "orka-system"}}
                f = A.classify(["create"] + (["--dry-run=server"] if dry else []) + ["-f", "-"], json.dumps(doc).encode())
                frames.extend(pair(f, str(i)))
            return frames
        with tempfile.TemporaryDirectory() as directory:
            q = Q.Qualification.__new__(Q.Qualification)
            q.work = pathlib.Path(directory)
            workspace, layout, path = q.work / "workspace", q.work / "layout", q.work / "passing.json"
            workspace.mkdir(); layout.mkdir(); path.write_bytes(b"receipt")
            q.context, q.promotion_context, q.audit = "kind-owned", None, A
            q.registry = lambda: contextlib.nullcontext("fixture:v1")
            q.cli = lambda *a, **k: None
            q.empty_member = lambda member: None
            q.promotion_subject = lambda r: (destination, specs)
            q.lift_denials = lambda *a: []
            q.pair_proof = lambda r: None
            q.task_inventory = lambda: []
            q.fresh_run = lambda *a: {"answerDigest": "fresh"}
            q.status = lambda *a: {"recordedPair": True}
            frames, calls, effects = [], [], []
            q.frames = lambda: A.completed(frames)
            q.raw_frames = lambda: frames[:]
            def lift(reference, evidence, selected_layout=None, **kwargs):
                self.assertFalse(workspace.exists(), "Lift must run with source withheld")
                calls.append((reference, kwargs))
                if kwargs.get("success") is False:
                    return subprocess.CompletedProcess([], 1, b"", b"release lift planning refused")
                if kwargs.get("plan"):
                    return subprocess.CompletedProcess([], 0, json.dumps(summary).encode(), b"")
                frames.extend(effect_frames())
                effects.append(reference)
                return subprocess.CompletedProcess([], 0, json.dumps(result).encode(), b"")
            q.lift = lift
            cases = {"one.yaml": b'id: one\ninput: question\nexpectContains: ["answer"]\n'}
            output = q.lift_qualification(workspace, layout, layout, "changed-S", path, receipt, cases)
            self.assertEqual(effects, ["native-suite@" + receipt["subject"]["SuiteDigest"]])
            self.assertEqual(len(calls), 7)
            self.assertEqual([c[1].get("promote", False) for c in calls], [False, True, False, True, True, True, True])
            self.assertEqual(output["local"]["freshAnswer"], {"answerDigest": "fresh"})
            self.assertTrue(output["registry"]["occupiedRepeatRefused"])
            self.assertTrue(output["status"]["recordedPair"])
            self.assertTrue(workspace.exists())


class PromotionProofs(unittest.TestCase):
    def test_private_route_binds_internal_ip_network_and_unmapped_nodeport(self):
        def node(ip, network="private-kind"):
            return {"NetworkSettings": {"Networks": {"kind": {"NetworkID": network, "IPAddress": ip}},
                                        "Ports": {"6443/tcp": [{"HostIp": "127.0.0.1", "HostPort": "1234"}]}}}
        source, target = node("172.18.0.2"), node("172.18.0.3")
        self.assertEqual(Q.private_model_route(source, target, "172.18.0.2", 30891), "http://172.18.0.2:30891/v1")
        for changed, ip in ((node("172.18.0.2", "foreign"), "172.18.0.2"), (source, "127.0.0.1")):
            with self.assertRaises(ValueError):
                Q.private_model_route(changed, target, ip, 30891)
        mapped = copy.deepcopy(source)
        mapped["NetworkSettings"]["Ports"]["30891/tcp"] = [{"HostPort": "30891"}]
        with self.assertRaises(ValueError):
            Q.private_model_route(mapped, target, "172.18.0.2", 30891)

    def test_promotion_requires_ordered_preflights_then_exact_creates(self):
        def request(kind, dry=False):
            raw = json.dumps({"apiVersion": "core.orka.ai/v1alpha1", "kind": kind,
                              "metadata": {"name": "member", "namespace": "orka-system"}}).encode()
            args = ["create"] + (["--dry-run=server"] if dry else []) + ["-f", "-"]
            return {**A.classify(args, raw), "returnCode": 0}
        frames = [request(k, dry) for dry in (True, False) for k in ("Provider", "Agent")]
        Q.verify_promote_effects(frames, "member")
        for bad in (frames[1:], frames + [frames[-1]], [frames[0], frames[2], frames[1], frames[3]],
                    frames + [request("Task")], [{**frames[0], "returnCode": 1}, *frames[1:]],
                    [{**frames[0], "verb": "replace"}, *frames[1:]]):
            with self.subTest(frames=bad), self.assertRaises(ValueError):
                Q.verify_promote_effects(bad, "member")

    def test_preflight_completion_precedes_first_persistent_intent(self):
        def request(kind, dry, identity):
            raw = json.dumps({"apiVersion": "core.orka.ai/v1alpha1", "kind": kind,
                              "metadata": {"name": "member", "namespace": "orka-system"}}).encode()
            args = ["create"] + (["--dry-run=server"] if dry else []) + ["-f", "-"]
            return pair(A.classify(args, raw), identity)
        p, a, cp, ca = [request(k, dry, str(i)) for i, (k, dry) in enumerate(
            (("Provider", True), ("Agent", True), ("Provider", False), ("Agent", False)))]
        raw = [*p, *a, *cp, *ca]
        Q.verify_promote_completion_order(raw)
        # Normalization still accepts this ordering: raw completions must catch it.
        premature = [p[0], a[0], *cp, *ca, p[1], a[1]]
        Q.verify_promote_effects(A.completed(premature), "member")
        for bad in (premature, [*p, *cp, *a, *ca], raw[:-1],
                    [{**p[0]}, {**p[1], "returnCode": 1}, *a, *cp, *ca]):
            with self.subTest(frames=bad), self.assertRaises(ValueError):
                Q.verify_promote_completion_order(bad)

    def test_mismatch_changes_only_previously_passing_provider_model(self):
        before = {"metadata": {"uid": "same-provider", "generation": 1},
                  "spec": {"defaultModel": "evaluated", "baseURL": "http://private:30891/v1", "secretRef": {"name": "owned"}}}
        after = copy.deepcopy(before)
        after["metadata"]["generation"] = 2
        after["spec"]["defaultModel"] = "unevaluated-model"
        Q.verify_model_only_change(before, after, "evaluated")
        for bad in (before, {**after, "metadata": {"uid": "recreated", "generation": 2}},
                    {**after, "spec": {**after["spec"], "baseURL": "http://foreign"}},
                    {**after, "spec": {**after["spec"], "defaultModel": "evaluated"}}):
            with self.subTest(provider=bad), self.assertRaises(ValueError):
                Q.verify_model_only_change(before, bad, "evaluated")

    def test_promotion_summary_and_created_result_bind_independent_destination(self):
        fixture = LiftProofs()
        fixture.setUp()
        receipt, summary = fixture.receipt, copy.deepcopy(fixture.summary)
        destination = copy.deepcopy(receipt["subject"])
        destination.update(BindingDigest="a" * 64, RenderedDigest="b" * 64)
        destination["Target"].update(Context="kind-destination", ClusterUID="new-cluster", NamespaceUID="new-namespace")
        summary.update(target={k[0].lower() + k[1:]: v for k, v in destination["Target"].items()},
                       gate="promotion", promote=True, servingModel="model", bindingDigest="a" * 64,
                       renderedDigest="b" * 64)
        specs = {kind: {"model": "model"} for kind in ("Provider", "Agent")}
        summary["resources"] = [{"kind": k, "name": "member", "action": "create",
                                 "specDigest": Q.native_digest("kmx.native-orka.spec/v1", specs[k])} for k in specs]
        Q.verify_promote_summary(summary, receipt, b"receipt", destination, specs)
        result = {"summary": summary, "state": "ready", "resources": [{**r, "outcome": "created", "ready": True,
                  "uid": "new-" + r["kind"], "generation": 1} for r in summary["resources"]]}
        Q.verify_promote_result(result, receipt, b"receipt", destination, specs)
        for key, value in (("sourceTarget", summary["target"]), ("gate", "same-subject"), ("servingModel", "other"),
                           ("target", summary["sourceTarget"]), ("bindingDigest", "2" * 64), ("promote", False)):
            with self.subTest(key=key), self.assertRaises(ValueError):
                Q.verify_promote_summary({**summary, key: value}, receipt, b"receipt", destination, specs)
        for key, value in (("uid", "provider-uid"), ("generation", 0), ("outcome", "reused"), ("ready", False)):
            bad = copy.deepcopy(result)
            bad["resources"][0][key] = value
            with self.subTest(key=key), self.assertRaises(ValueError):
                Q.verify_promote_result(bad, receipt, b"receipt", destination, specs)
        bad = copy.deepcopy(summary)
        bad["resources"][0]["uid"] = "occupied"
        with self.assertRaises(ValueError):
            Q.verify_promote_summary(bad, receipt, b"receipt", destination, specs)



def status_fixture(runtime="orka"):
    target = {"context": "kind-owned", "namespace": "orka-system", "clusterUID": "cluster", "namespaceUID": "namespace"}
    source = dict(target, namespace="source", namespaceUID="source-namespace")
    kinds = ("Provider", "Agent") if runtime == "orka" else ("ModelConfig", "Agent")
    summary = {"runtime": runtime, "suite": "proof-suite", "member": "proof", "suiteDigest": "sha256:" + "a" * 64,
               "target": target, "sourceTarget": source, "gate": "promotion",
               "resources": [{"kind": k, "name": "proof", "specDigest": "sha256:" + str(i + 1) * 64} for i, k in enumerate(kinds)]}
    placement = {"summary": summary, "resources": [dict(r, uid=r["kind"] + "-uid", generation=1) for r in summary["resources"]]}
    record = {"version": 1, "liftID": "c" * 32, "suiteDigest": summary["suiteDigest"],
              "receiptDigest": "sha256:" + Q.sha(b"receipt"), "receiptRunID": "d" * 32, "evaluation": source,
              "target": target, "gate": "promotion", "evaluatedOnTarget": False, "attemptedAt": "2026-10-16T12:00:00Z",
              "actor": None, "resourceSpecs": {r["kind"]: r["specDigest"] for r in summary["resources"]}}
    provenance = {"state": "recorded", "reason": "unsigned create-time observation; no UID continuity proof", "record": record}
    ready = {"state": "ready", "reason": "current-generation Ready placement"}
    resources = [dict(r, readiness=dict(ready), provenance=copy.deepcopy(provenance), specDrift="matched")
                 for r in placement["resources"]]
    row = {"kind": "Agent", "name": "proof", "runtime": runtime, "suite": "proof-suite", "member": "proof",
           "suiteDigest": summary["suiteDigest"], "provenance": copy.deepcopy(provenance), "readiness": ready,
           "resources": resources, "workloads": []}
    report = {"version": 1, "destination": "qualified", "target": target, "complete": True, "agents": [row],
              "candidates": [], "errors": [], "observation": "Unsigned cluster observations; original resource UID continuity is unverified. Ready is placement, not answered or evaluated."}
    table = ("Destination: qualified\n" + report["observation"] + "\nAGENT/MEMBER RUNTIME SUITE DIGEST RECEIPT EVIDENCE ATTEMPT/ACTOR AGENT PROVENANCE READY\n"
             + "proof/proof " + runtime + " " + summary["suiteDigest"][:19] + " " + record["receiptDigest"][:19]
             + " passed on source; promoted; not evaluated on this target 2026-10-16T12:00:00Z/not recorded recorded ready\n"
             + "".join(r["kind"] + "/proof: provenance=recorded; spec=matched; ready: current-generation\n" for r in resources)
             + "Agents: 1; evaluation candidate roots (separate): 0; complete: true\n")
    return placement, report, table


class StatusProofs(unittest.TestCase):
    def test_status_requires_recorded_pair_receipt_target_and_current_readiness(self):
        self.assertTrue(callable(getattr(Q, "verify_status", None)), "missing status qualification validator")
        placement, report, table = status_fixture()
        # Shared namespaces may contain unrelated, readable foreign rows.
        report["agents"].append({"kind": "Agent", "name": "foreign", "runtime": "orka"})
        Q.verify_status(report, table, placement, b"receipt", "d" * 32)
        mutations = [
            lambda r: r.update(complete=False), lambda r: r.update(errors=["partial"]),
            lambda r: r["target"].update(namespaceUID="wrong"),
            lambda r: r["agents"][0].update(runtime="kagent"),
            lambda r: r["agents"][0].update(suiteDigest="sha256:" + "f" * 64),
            lambda r: r["agents"][0]["readiness"].update(state="pending"),
            lambda r: r["agents"][0]["provenance"].update(state="not recorded", record=None),
            lambda r: r["agents"][0]["provenance"]["record"].update(receiptDigest="sha256:" + "f" * 64),
            lambda r: r["agents"][0]["provenance"]["record"].update(gate="same-subject", evaluatedOnTarget=True),
            lambda r: r["agents"][0]["provenance"]["record"].update(receiptRunID="e" * 32),
            lambda r: r["agents"][0]["provenance"]["record"].update(attemptedAt="not-time"),
            lambda r: r["agents"][0]["resources"][0]["provenance"].update(state="not recorded", record=None),
            lambda r: r["agents"][0]["resources"][0].update(uid="replacement"),
            lambda r: r["agents"][0]["resources"][0].update(specDrift="drifted"),
        ]
        for i, mutate in enumerate(mutations):
            changed = copy.deepcopy(report); mutate(changed)
            with self.subTest(defect=i), self.assertRaises(ValueError):
                Q.verify_status(changed, table, placement, b"receipt", "d" * 32)
        for bad in ("", table.replace("not evaluated on this target", "evaluated on target"),
                    table.replace("Unsigned", "Trusted"), table.replace("Provider/proof: provenance=recorded", "Provider/proof: provenance=unknown")):
            with self.subTest(table=bad[:20]), self.assertRaises(ValueError):
                Q.verify_status(report, bad, placement, b"receipt", "d" * 32)

    def test_status_driver_executes_json_and_table_get_only_without_creating_home(self):
        self.assertTrue(callable(getattr(Q, "qualify_status", None)), "missing status qualification driver")
        placement, report, table = status_fixture()
        with tempfile.TemporaryDirectory() as directory:
            work = pathlib.Path(directory)
            real = work / "real-kubectl"
            real.write_text("#!/usr/bin/env python3\nprint('{}')\n"); real.chmod(0o700)
            calls = []
            def run(args, env):
                calls.append(args)
                root = pathlib.Path(args[args.index("--workspace") + 1])
                self.assertEqual(sorted(p.name for p in root.iterdir()), ["kmx.yaml", "workspace.yaml"])
                self.assertEqual(Q.yaml.safe_load((root / "workspace.yaml").read_text()), {"schemaVersion": 1, "name": "status-proof", "member": "agents/status-proof/member.yaml", "buildProfile": "build-profiles/default.json"})
                config = Q.yaml.safe_load((root / "kmx.yaml").read_text())
                self.assertEqual(config, {"version": 1, "targets": {"qualified": {"context": "kind-owned", "namespace": "orka-system", "runtime": "orka"}}})
                self.assertFalse(pathlib.Path(env["KMX_HOME"]).exists())
                result = subprocess.run(["kubectl", "--context", "kind-owned", "--request-timeout=10s", "get", "--raw", "/apis"],
                                        env=env, capture_output=True, check=True)
                self.assertEqual(result.stdout, b"{}\n")
                return subprocess.CompletedProcess([], 0, json.dumps(report).encode() if args[-1] == "json" else table.encode(), b"")
            Q.qualify_status(work, placement, b"receipt", "d" * 32, run, dict(os.environ), str(real))
            self.assertEqual([a[-1] for a in calls], ["json", "table"])
            self.assertFalse((work / "destination-status/kmx-home").exists())

    def test_status_wrapper_rejects_posts_tasks_admission_and_scope_escape_before_forwarding(self):
        with tempfile.TemporaryDirectory() as directory:
            work = pathlib.Path(directory)
            marker = work / "called"
            real = work / "real-kubectl"
            real.write_text("#!/usr/bin/env python3\nimport pathlib\npathlib.Path(" + repr(str(marker)) + ").touch()\nprint('{}')\n")
            real.chmod(0o700)
            wrapper = ROOT / "scripts/ci/suite-native.py"
            env = dict(os.environ, KMX_STATUS_CONTEXT="kind-owned", KMX_STATUS_NAMESPACE="orka-system",
                       KMX_STATUS_KUBECTL=str(real), KMX_STATUS_AUDIT=str(work / "audit.jsonl"))
            prefix = ["--context", "kind-owned", "--request-timeout=10s"]
            for tail in (["create", "--raw", "/api/v1/namespaces/orka-system/services/fixture/proxy", "-f", "-"],
                         ["create", "--dry-run=server", "-f", "-"], ["get", "secret", "key", "-o", "json"],
                         ["get", "--raw", "/api/v1/namespaces/other/pods?limit=128"],
                         ["get", "--raw", "/api/v1/namespaces/orka-system/secrets"],
                         ["get", "--raw", "/api/v1/namespaces/orka-system/services/fixture/proxy"],
                         ["get", "--raw", "/apis/core.orka.ai/v1alpha1/namespaces/orka-system/tasks/task"],
                         ["get", "--raw", "https://foreign/api"]):
                with self.subTest(tail=tail):
                    result = subprocess.run([sys.executable, str(wrapper), "--status-kubectl", *prefix, *tail], env=env, input=b"PRIVATE_CANARY", capture_output=True)
                    self.assertNotEqual(result.returncode, 0)
                    self.assertFalse(marker.exists())
            self.assertTrue((work / "audit.jsonl").exists(), "every rejected attempt needs a trace")
            self.assertNotIn("PRIVATE_CANARY", (work / "audit.jsonl").read_text())


if __name__ == "__main__":
    unittest.main()
