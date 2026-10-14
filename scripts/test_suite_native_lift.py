#!/usr/bin/env python3
"""Boundary tests for live lift qualification; not a live execution claim."""
import copy
import importlib.util
import json
import os
import pathlib
import signal
import subprocess
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

    def test_summary_proof_rejects_identity_gate_or_create_changes(self):
        Q.verify_lift_summary(self.summary, self.receipt, b"receipt")
        for key, value in (("suiteDigest", "sha256:" + "7" * 64), ("gate", "promotion"), ("promote", True),
                           ("runtime", "kagent"), ("evidenceRunID", "other")):
            bad = {**self.summary, key: value}
            with self.subTest(key=key), self.assertRaises(ValueError):
                Q.verify_lift_summary(bad, self.receipt, b"receipt")
        bad = copy.deepcopy(self.summary)
        bad["resources"][0]["action"] = "create"
        with self.assertRaises(ValueError):
            Q.verify_lift_summary(bad, self.receipt, b"receipt")

    def test_result_needs_both_exact_reused_ready_resources(self):
        result = {"summary": self.summary, "state": "ready", "resources": [{**r, "outcome": "reused", "ready": True} for r in self.summary["resources"]]}
        Q.verify_lift_result(result, self.receipt, b"receipt")
        for key, value in (("outcome", "created"), ("ready", False), ("uid", "recreated"), ("generation", 2)):
            bad = copy.deepcopy(result)
            bad["resources"][0][key] = value
            with self.subTest(key=key), self.assertRaises(ValueError):
                Q.verify_lift_result(bad, self.receipt, b"receipt")

    def test_terminal_spec_patch_preserves_object_model_and_changes_name(self):
        for spec in ({"providerRef": {"name": "member"}}, {"model": {"name": "evaluated", "temperature": 0}}):
            doc = {"spec": copy.deepcopy(spec)}
            patch = Q.spec_drift_patch(doc)
            self.assertIsInstance(patch["spec"]["model"], dict)
            self.assertEqual(patch["spec"]["model"]["name"], "unevaluated-model")
            if "model" in spec:
                self.assertEqual(patch["spec"]["model"]["temperature"], 0)
            self.assertEqual(doc["spec"], spec)
        with self.assertRaises(ValueError):
            Q.spec_drift_patch({"spec": {"model": "invalid-shape"}})

    def test_counterfactual_control_uses_same_reconstruction_and_preserves_original(self):
        subject = self.receipt["subject"]
        subject["RenderedDigest"] = Q.native_digest("kmx.native-orka.revision/v1", {**subject, "RenderedDigest": ""})
        original = copy.deepcopy(self.receipt)
        control = Q.namespace_evidence(self.receipt, "namespace")
        self.assertEqual(control, self.receipt)
        negative = Q.namespace_evidence(self.receipt, "counterfactual-namespace")
        self.assertEqual(self.receipt, original)
        negative["subject"]["Target"]["NamespaceUID"] = "namespace"
        negative["subject"]["RenderedDigest"] = control["subject"]["RenderedDigest"]
        self.assertEqual(negative, control)

    def test_fresh_answer_runs_authored_text_assertions_not_nonempty(self):
        case = {"expectContains": ["approved"], "assertions": [{"type": "notContains", "value": "denied"}, {"type": "regex", "pattern": "^approved"}]}
        Q.verify_fresh_answer(b"approved result\n", case)
        for answer in (b"error result\n", b"approved denied\n", b"prefix approved\n", b"approved result", b"\n"):
            with self.subTest(answer=answer), self.assertRaises(ValueError):
                Q.verify_fresh_answer(answer, case)


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


if __name__ == "__main__":
    unittest.main()
