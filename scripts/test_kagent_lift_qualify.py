#!/usr/bin/env python3
"""Exercise the actual independent runtime smoke at external IO boundaries."""
import contextlib
import copy
import hashlib
import importlib.util
import io
import json
import os
import pathlib
import tempfile
import unittest
from unittest import mock

import yaml

SOURCE = pathlib.Path(__file__).resolve().parent / "ci/kagent-lift.py"

# Unmodified controller Service from the pinned kagent 0.10.3 Helm render.
# Only API-server identity fields are added by the hermetic replay below.
CONTROLLER_SERVICE = """\
apiVersion: v1
kind: Service
metadata:
  name: kagent-controller
  namespace: kagent
  labels:
    helm.sh/chart: kagent-0.10.3
    app.kubernetes.io/name: kagent
    app.kubernetes.io/instance: kagent
    app.kubernetes.io/version: "0.10.3"
    app.kubernetes.io/managed-by: Helm
    app.kubernetes.io/part-of: kagent
    app.kubernetes.io/component: controller
spec:
  type: ClusterIP
  ports:
    - port: 8083
      targetPort: 8083
      protocol: TCP
      name: controller
  selector:
    app.kubernetes.io/name: kagent
    app.kubernetes.io/instance: kagent
    app.kubernetes.io/component: controller
"""


class QualifyTests(unittest.TestCase):
    def exercise(self, defect=None):
        spec = importlib.util.spec_from_file_location("kagent_smoke", SOURCE)
        driver = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(driver)
        sentinel = "kmx-mcp-private-smoke-marker"
        answer = "KMX MCP proof: " + sentinel
        sha = lambda value: hashlib.sha256(value.encode()).hexdigest()
        evidence = {"model_calls": 2, "tool_calls": 1, "terminal_answers": 1, "judge_calls": 0,
                    "sentinel_sha256": sha(sentinel), "cases": [{"call_id": "call-one", "model_calls": 2,
                    "tool_calls": 1, "terminal_answers": 1, "user_turns": 1,
                    "input_sha256": sha("Use the allowed tool and return its result."), "answer_sha256": sha(answer)}]}
        empty = dict(evidence, model_calls=0, tool_calls=0, terminal_answers=0, cases=[])
        response = {"jsonrpc": "2.0", "id": "kmx-lift-once", "result": {"kind": "task", "id": "actual-task",
                    "contextId": "actual-context", "status": {"state": "completed"},
                    "artifacts": [{"parts": [{"kind": "text", "text": answer}]}]}}
        service = yaml.safe_load(CONTROLLER_SERVICE)
        service["metadata"].update(uid="controller-service", resourceVersion="1")
        labels = copy.deepcopy(service["spec"]["selector"])
        container = {"name": "controller", "image": "ghcr.io/kagent-dev/kagent/controller:0.10.3@sha256:" + driver.CONTROLLER,
                     "env": [{"name": "AUTH_MODE", "value": "unsecure"}]}
        controller = {"kind": "Deployment", "metadata": {"name": "kagent-controller", "namespace": "kagent",
                      "uid": "controller-uid", "generation": 1, "resourceVersion": "1"}, "spec": {"replicas": 1,
                      "selector": {"matchLabels": labels}, "template": {"metadata": {"labels": labels},
                      "spec": {"containers": [container]}}}, "status": {"observedGeneration": 1, "readyReplicas": 1,
                      "updatedReplicas": 1, "availableReplicas": 1}}
        if defect == "auth": container["env"][0]["value"] = "trusted-proxy"
        if defect == "auth-from": container["env"][0]["valueFrom"] = {"secretKeyRef": {"name": "override"}}
        if defect == "auth-duplicate": container["env"].append(dict(container["env"][0]))
        if defect == "selector": service["spec"]["selector"] = {"app": "unrelated"}
        if defect == "port": service["spec"]["ports"][0]["targetPort"] = 9000
        if defect == "port-name": service["spec"]["ports"][0]["name"] = "http"
        if defect == "service-port": service["spec"]["ports"][0]["port"] = 9000
        if defect == "protocol": service["spec"]["ports"][0]["protocol"] = "UDP"
        if defect == "duplicate-port": service["spec"]["ports"].append(dict(service["spec"]["ports"][0]))
        if defect == "rollout": controller["status"]["observedGeneration"] = 0
        if defect == "controller-image": container["image"] = "unqualified/image:latest"
        if defect == "counters": evidence["tool_calls"] = 2
        if defect == "case-counts": evidence["cases"][0]["user_turns"] = 2
        if defect == "case-input": evidence["cases"][0]["input_sha256"] = "f" * 64
        if defect == "case-answer": evidence["cases"][0]["answer_sha256"] = "f" * 64
        if defect == "judge": evidence["judge_calls"] = 1
        if defect == "rpc": response["error"] = {"message": "private failure"}
        if defect == "task-id": response["result"]["id"] = ""
        if defect == "warm": empty["tool_calls"] = 1
        objects, frames, creates, sends = {}, [], [], []
        objects[("deployment", "kagent-controller", "kagent")] = controller
        objects[("service", "kagent-controller", "kagent")] = service
        identity_reads = 0

        with tempfile.TemporaryDirectory() as directory:
            work = pathlib.Path(directory)
            (work / "bin").mkdir()
            (work / "fail-bin").mkdir()
            (work / "bin/kubectl").write_text("unused hermetic external boundary")
            (work / "fail-bin/kubectl").symlink_to(work / "bin/kubectl")
            # Any product invocation, including legacy bundle/create --live or
            # readers/evaluation/status, fails this runtime-only smoke test.
            binary = work / "kmx"
            binary.write_text("not executable: independent smoke must not invoke KMX")

            def kube(*args, phase, data=None):
                self.assertEqual(args, ("-n", "agents", "create", "-f", "-"))
                obj = yaml.safe_load(data)
                creates.append(copy.deepcopy(obj))
                self.assertEqual(obj["apiVersion"], "kagent.dev/v1alpha2")
                self.assertEqual(obj["metadata"], {"name": "lift-proof", "namespace": "agents"})
                self.assertNotIn("kaimahi.dev/bundle", json.dumps(obj))
                kind = obj["kind"]
                frames.append({"verb": "create", "dry_run": "none", "resources": [{"apiVersion": "kagent.dev/v1alpha2",
                              "kind": kind, "namespace": "agents", "name": "lift-proof"}]})
                obj["metadata"].update(uid=kind + "-actual-uid", generation=1, resourceVersion="1")
                obj["status"] = {"conditions": [{"type": cond, "status": "True", "observedGeneration": 1}
                                  for cond in (("Accepted", "Ready") if kind == "Agent" else ("Accepted",))]}
                objects[("agents.kagent.dev" if kind == "Agent" else "modelconfigs.kagent.dev", "lift-proof", "agents")] = obj
                return b"{}"

            def get(kind, name=None, namespace="agents", optional=False):
                nonlocal identity_reads
                if kind == "namespace": return {"metadata": {"uid": "actual-cluster"}}
                key = (kind, name, namespace)
                obj = copy.deepcopy(objects.get(key))
                if defect == "occupied" and optional and name == "lift-proof":
                    return {"metadata": {"uid": "preexisting"}}
                if obj and kind == "agents.kagent.dev":
                    identity_reads += 1
                    if defect == "identity-drift" and sends: obj["metadata"]["uid"] = "replaced"
                    if defect == "missing-uid": obj["metadata"].pop("uid")
                    if defect == "terminating": obj["metadata"]["deletionTimestamp"] = "now"
                return obj

            @contextlib.contextmanager
            def forward(service, port, namespace="agents"):
                self.assertIn((service, port, namespace), (("lift-fixture", 9000, "agents"), ("kagent-controller", 8083, "kagent")))
                yield "http://127.0.0.1:" + str(port)

            def http(url, data=None):
                if data is not None:
                    self.assertEqual(url, "http://127.0.0.1:8083/api/a2a/agents/lift-proof")
                    self.assertEqual(data["method"], "message/send")
                    self.assertTrue(data["params"]["configuration"]["blocking"])
                    sends.append(data)
                    return response
                self.assertEqual(url, "http://127.0.0.1:9000/evidence")
                return evidence if sends else empty

            def images(include_agent=False):
                self.assertTrue(include_agent)
                if defect == "images": raise driver.QualificationError("image mismatch")
                return {"controller": driver.CONTROLLER, "kagent": driver.GO_ADK,
                        "postgresql": driver.POSTGRES, "fixture": driver.FIXTURE}

            with mock.patch.dict(os.environ, KMX_HOME=str(work / "kmx-home")), \
                    mock.patch.object(driver, "kube", side_effect=kube), mock.patch.object(driver, "get", side_effect=get), \
                    mock.patch.object(driver, "audit_frames", side_effect=lambda _: copy.deepcopy(frames)), \
                    mock.patch.object(driver, "verify_images", side_effect=images), \
                    mock.patch.object(driver, "forward", side_effect=forward), mock.patch.object(driver, "http_json", side_effect=http), \
                    mock.patch.object(driver.subprocess, "run", side_effect=AssertionError("runtime smoke invoked product CLI")), \
                    contextlib.redirect_stdout(io.StringIO()) as out:
                if defect is None:
                    driver.qualify(binary, work)
                elif defect in ("port-name", "service-port", "protocol", "duplicate-port"):
                    with self.assertRaisesRegex(driver.QualificationError, "control Service HTTP endpoint differs"):
                        driver.qualify(binary, work)
                else:
                    with self.assertRaises(ValueError): driver.qualify(binary, work)
            artifact = work / "qualification.json"
            self.assertEqual(artifact.exists(), defect is None, "failure published success evidence")
            self.assertLessEqual(len(sends), 1, "ambiguous smoke was resent")
            self.assertFalse(any(path.name == "agent.yaml" or path.name == "receipts" for path in work.rglob("*")))
            if defect is not None:
                if defect in ("auth", "auth-from", "auth-duplicate", "selector", "port", "port-name", "service-port", "protocol", "duplicate-port", "rollout", "controller-image", "images", "occupied", "missing-uid", "terminating", "warm"):
                    self.assertEqual(sends, [], "failed fence admitted a native send")
                return
            self.assertEqual([obj["kind"] for obj in creates], ["ModelConfig", "Agent"])
            self.assertEqual(creates[0]["spec"], {"provider": "OpenAI", "model": "fixture", "apiKeySecret": "lift-model-key",
                             "apiKeySecretKey": "api-key", "openAI": {"baseUrl": "http://lift-fixture.agents.svc.cluster.local:9000/v1"}})
            declarative = creates[1]["spec"]["declarative"]
            self.assertEqual(declarative["runtime"], "go")
            self.assertEqual(declarative["modelConfig"], "lift-proof")
            self.assertEqual(declarative["tools"], [{"type": "McpServer", "mcpServer": {"apiGroup": "kagent.dev",
                             "kind": "RemoteMCPServer", "name": "lift-tools", "toolNames": ["read_sentinel"]}}])
            self.assertEqual(declarative["deployment"]["securityContext"]["capabilities"], {"drop": ["ALL"]})
            self.assertEqual(len(sends), 1)
            proof = json.loads(artifact.read_text())
            self.assertEqual(artifact.stat().st_mode & 0o777, 0o600)
            self.assertEqual(proof["kind"], "KagentRuntimeSmokeQualification")
            self.assertEqual(proof["execution"]["native_id"], "actual-task")
            self.assertEqual(proof["execution"]["native_context_id"], "actual-context")
            self.assertEqual(proof["fixture_counts"], {"tool_calls": 1, "model_calls": 2, "terminal_answers": 1, "judge_calls": 0})
            self.assertEqual(proof["identities"]["Agent/lift-proof"]["uid"], "Agent-actual-uid")
            for private in (sentinel, "Use the allowed tool", "systemMessage", "apiKeySecret", "evaluation", "receipt", "bundle"):
                self.assertNotIn(private, artifact.read_text() + out.getvalue())

    def test_pinned_chart_service_qualifies_raw_fixture_and_one_native_send(self):
        self.exercise()

    def test_runtime_fences_refuse_without_native_send_or_artifact(self):
        for defect in ("auth", "auth-from", "auth-duplicate", "selector", "port", "port-name", "service-port", "protocol", "duplicate-port", "rollout", "controller-image", "images", "occupied", "missing-uid", "terminating", "warm"):
            with self.subTest(defect=defect): self.exercise(defect)

    def test_ambiguous_or_uncorrelated_execution_never_resends_or_exports(self):
        for defect in ("rpc", "task-id", "counters", "case-counts", "case-input", "case-answer", "judge", "identity-drift"):
            with self.subTest(defect=defect): self.exercise(defect)


if __name__ == "__main__":
    unittest.main()
