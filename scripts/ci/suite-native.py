#!/usr/bin/env python3
"""Native qualification in the existing exclusively owned Orka CI cluster."""
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
import subprocess
import sys
import tarfile
import time
import urllib.request

import yaml

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
    return "sha256:" + sha(canonical({"renderer": "kmx.native-orka/v1", "files": {p: "sha256:" + sha(b) for p, b in files.items()}}))


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


def verify_cases(rows, files):
    require(len(rows) == len(files) and bool(rows), "case-coverage")
    for row, (name, raw) in zip(rows, sorted(files.items())):
        case = yaml.safe_load(raw)
        definitions = [{"id": "expectContains." + str(i), "type": "contains", "value": value}
                       for i, value in enumerate(case.get("expectContains", []))] + case.get("assertions", [])
        expected = []
        for definition in definitions:
            kind = definition["type"]
            require(kind in {"contains", "notContains", "regex"}, "case-evaluator")
            operand = definition["pattern" if kind == "regex" else "value"]
            expected.append({"id": definition["id"], "type": kind,
                             "definitionDigest": sha(frame("type", kind.encode()) + frame("operand", operand.encode())),
                             "evaluatorVersion": "kmx.text-assertions/v1", "verdict": "pass"})
        require(row["id"] == case["id"] and row["path"] == "metadata/kmx/eval/" + name and row["caseDigest"] == sha(raw) and
                row["inputDigest"] == sha(case["input"].encode()) and row["verdict"] == "pass" and row["assertions"] == expected, "case-metadata")


def verify_lift_summary(summary, receipt, evidence):
    subject = receipt["subject"]
    target = {k[0].lower() + k[1:]: v for k, v in subject["Target"].items()}
    expected = {"suite": subject["Suite"], "suiteDigest": subject["SuiteDigest"], "member": subject["Member"],
                "model": subject["Model"], "runtime": "orka", "target": target, "sourceTarget": target,
                "promote": False, "gate": "same-subject", "inference": "provider:native-suite-inference",
                "bindingDigest": subject["BindingDigest"], "renderedDigest": subject["RenderedDigest"],
                "evidenceDigest": "sha256:" + sha(evidence), "evidenceRunID": receipt["runID"]}
    require(all(summary.get(k) == v for k, v in expected.items()), "lift-summary")
    require(re.fullmatch(r"sha256:[0-9a-f]{64}", summary.get("planDigest", "")), "lift-plan-digest")
    require(len(summary["resources"]) == 2, "lift-resources")
    for resource, kind in zip(summary["resources"], ("Provider", "Agent")):
        owned = receipt[kind.lower()]
        require(all(resource.get(k) == v for k, v in {"kind": kind, "name": subject["Member"], "action": "reuse",
                "uid": owned["UID"], "generation": owned["Generation"], "specDigest": owned["SpecDigest"]}.items()), "lift-reuse")


def verify_lift_result(result, receipt, evidence):
    verify_lift_summary(result["summary"], receipt, evidence)
    require(result["state"] == "ready" and len(result["resources"]) == 2, "lift-ready")
    for resource, kind in zip(result["resources"], ("Provider", "Agent")):
        owned = receipt[kind.lower()]
        require(all(resource.get(k) == v for k, v in {"kind": kind, "name": receipt["subject"]["Member"],
                "outcome": "reused", "ready": True, "uid": owned["UID"], "generation": owned["Generation"],
                "specDigest": owned["SpecDigest"]}.items()), "lift-result")


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


def spec_drift_patch(doc):
    model = copy.deepcopy(doc["spec"].get("model", {}))
    require(isinstance(model, dict) and model.get("name") != "unevaluated-model", "spec-drift-shape")
    model["name"] = "unevaluated-model"
    return {"spec": {"model": model}}


def namespace_evidence(receipt, namespace_uid):
    # Counterfactual only: never replace the live runtime namespace. A positive
    # original-UID control follows this identical reconstruction before denial.
    out = copy.deepcopy(receipt)
    subject = out["subject"]
    subject["Target"]["NamespaceUID"] = namespace_uid
    subject["RenderedDigest"] = native_digest("kmx.native-orka.revision/v1", {**subject, "RenderedDigest": ""})
    return out


def verify_refusal(code, stderr, frames, reason):
    require(code == 1 and reason.encode() in stderr and not any(f["persistent"] or f.get("admission") or f.get("taskAttempt") or f.get("verb") == "blocked" or
            f.get("returnCode", 0) != 0 for f in frames), "refusal-proof")


def verify_collision_refusal(code, stderr, frames, member):
    # Entering placement is classified ambiguous even when its ownership
    # preflight refused. Independent audit/readback establishes no writes.
    verify_refusal(code, stderr, frames, "native evaluation outcome unknown; resources may remain; no retry or cleanup")
    require(any(f["verb"] == "get" and f["resource"] == "providers.core.orka.ai" and f["name"] == member for f in frames), "collision-inspection")


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
    for name, raw in cases.items():
        require(files["metadata/kmx/eval/" + name] == raw, "transfer-cases")
    return graph


def verify_runtime(expected, controller, worker):
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
    require(actual == expected and not container.get("command") and not meta.get("deletionTimestamp"), "runtime-identity")
    require(actual["Mode"] == "harness-v2" and actual["WatchNamespace"] == "orka-system" and helm["Chart"] == "orka-0.2.0" and
            container["image"] == "ghcr.io/orka-agents/orka@sha256:7c1727f92d5c0cf05d464c6eb9e70b8342cb5a7a87c2e35338f051d7b85aa370" and
            actual["AIWorkerImage"] == "ghcr.io/orka-agents/orka/ai-worker@sha256:814d7149308e62730df59759c5137667cad04520c94cc50ff56e5028fdc8bcd4", "runtime-pin")
    require(status["observedGeneration"] == meta["generation"] and all(status[k] == spec["replicas"] for k in
            ("replicas", "updatedReplicas", "readyReplicas", "availableReplicas")) and not status.get("unavailableReplicas", 0), "runtime-ready")
    wm = worker["metadata"]
    require(wm["name"] == actual["WorkerAccount"] and wm["namespace"] == "orka-system" and not wm.get("deletionTimestamp") and
            wm["labels"]["orka.ai/worker"] == "true" and wm["labels"]["orka.ai/worker-trust"] == "ai", "runtime-worker")


class Qualification:
    def __init__(self, context, work):
        # The owned CI fixture uses one kubeconfig file. Capture its selection
        # before private HOME/cwd would redirect kubectl and client-go defaults.
        kubeconfig = pathlib.Path(os.environ.get("KUBECONFIG") or pathlib.Path.home() / ".kube/config").absolute()
        require(kubeconfig.is_file(), "kubeconfig")
        self.context, self.work = context, private_root(work)
        self.repo = pathlib.Path(__file__).resolve().parents[2]
        self.kmx, self.kubectl = str(self.repo / "bin/kmx"), shutil.which("kubectl")
        require(self.kubectl and pathlib.Path(self.kmx).is_file(), "tools")
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
        self.phase = "preflight"

    def command(self, args, raw=None, timeout=60, product=False, success=True):
        with subprocess.Popen([str(a) for a in args], cwd=self.work, env=self.product_env if product else self.env,
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

    def frames(self):
        path = self.work / "audit.jsonl"
        return self.audit.completed([json.loads(line) for line in path.read_bytes().splitlines()]) if path.exists() else []

    def provision(self):
        self.phase = "model"
        for resource, name in (("deployment", "native-suite-aikit"), ("service", "native-suite-aikit"),
                               ("configmap", "native-suite-aikit"), ("providers.core.orka.ai", "native-suite-inference")):
            require(not self.kube("-n", "orka-system", "get", resource, name, "--ignore-not-found", "-o", "name").strip(), "fixture-exists")
        for name in ("ci-native-suite", "ci-native-collision"):
            for resource in ("providers.core.orka.ai", "agents.core.orka.ai"):
                require(not self.kube("-n", "orka-system", "get", resource, name, "--ignore-not-found", "-o", "name").strip(), "member-exists")
        local = self.get("providers.core.orka.ai", "local")["spec"]
        self.secret_ref = local["secretRef"]
        require(self.secret_ref["name"] == "local-provider-key", "fixture-secret")
        self.kube("-n", "orka-system", "create", "configmap", "native-suite-aikit",
                  "--from-file=config.yaml=" + str(self.repo / "scripts/ci/eval-loop-model.yaml"))
        self.kube("create", "-f", self.repo / "scripts/ci/native-suite-model.yaml")
        self.kube("-n", "orka-system", "rollout", "status", "deployment/native-suite-aikit", "--timeout=480s", timeout=480)
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
                warmup = (self.repo / "scripts/ci/eval-loop-warmup.json").read_bytes()
                request = urllib.request.Request("http://127.0.0.1:18091/v1/chat/completions", data=warmup, headers={"Content-Type": "application/json"})
                with urllib.request.urlopen(request, timeout=180) as response:
                    require(response.status == 200 and json.load(response)["choices"], "model-warmup")
            finally:
                forward.terminate()
                forward.wait(timeout=10)
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

    def lift(self, reference, receipt_path, layout=None, plan=False, evidence=True, success=True, inference="native-suite-inference"):
        args = ["lift", reference, "--to-context", self.context, "--to-namespace", "orka-system", "--inference", "provider:" + inference, "-o", "json"]
        args += ["--from-layout", layout] if layout else ["--plain-http"]
        if evidence:
            args += ["--evidence", receipt_path]
        if plan:
            args += ["--plan"]
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

    def lift_qualification(self, workspace, layout, receipt_path, receipt, cases):
        evidence, original = receipt_path.read_bytes(), snapshot(workspace)
        layout_before = snapshot(layout)
        subject = receipt["subject"]
        local = "native-suite@" + subject["SuiteDigest"]
        results = {}
        with self.registry() as registry:
            self.cli("push", "--workspace", workspace, "--evaluation", receipt_path, "--plain-http", "--provenance=false", registry)
            registry = registry.rsplit(":", 1)[0] + "@" + subject["SuiteDigest"]
            hidden = self.work / "source-withheld"
            workspace.rename(hidden)
            hidden.chmod(0)
            try:
                for transport, reference, selected_layout in (("local", local, layout), ("registry", registry, None)):
                    self.phase = "lift-" + transport
                    self.live_subject(subject)
                    self.pair_proof(receipt)
                    for step, plan in enumerate((True, False, False)):
                        self.phase = "lift-" + transport + "-" + str(step)
                        start = len(self.frames())
                        result = self.lift(reference, receipt_path, selected_layout, plan=plan)
                        self.audit.verify_read_only(self.frames()[start:])
                        parsed = json.loads(result.stdout)
                        (verify_lift_summary if plan else verify_lift_result)(parsed, receipt, evidence)
                        self.pair_proof(receipt)
                    results[transport] = self.fresh_run(transport, receipt, yaml.safe_load(cases[sorted(cases)[0]]))
                    results[transport].update({"plans": 1, "reuseApplies": 2, "admissions": 0, "nativeWrites": 0})
            finally:
                hidden.chmod(0o700)
                hidden.rename(workspace)
        require(snapshot(workspace) == original and snapshot(layout) == layout_before and receipt_path.read_bytes() == evidence, "lift-input-changed")
        return results

    def lift_denials(self, layout, changed_layout, changed_s, receipt_path, receipt):
        self.phase = "lift-denials"
        member = receipt["subject"]["Member"]
        reference = "native-suite@" + receipt["subject"]["SuiteDigest"]
        outcomes = []

        def pair_state():
            return [{k: doc[k] for k in ("kind", "spec")} | {"identity": {k: doc["metadata"].get(k) for k in
                    ("name", "uid", "generation", "annotations")}} for doc in
                    (self.get("providers.core.orka.ai", member), self.get("agents.core.orka.ai", member))]

        def refusal(name, selected_reference=reference, evidence=receipt_path, selected_layout=layout, supplied=True, reason="release lift planning refused", inference="native-suite-inference"):
            pair_before, tasks_before = pair_state(), self.member_tasks(member)
            start = len(self.frames())
            rejected = self.lift(selected_reference, evidence, selected_layout, evidence=supplied, success=False, inference=inference)
            frames = self.frames()[start:]
            verify_refusal(rejected.returncode, rejected.stderr, frames, reason)
            self.audit.verify_read_only(frames)
            require(pair_state() == pair_before and self.member_tasks(member) == tasks_before, "refusal-live-drift")
            outcomes.append({"name": name, "admissions": 0, "nativeWrites": 0, "taskAttempts": 0})

        def control(path, expected_receipt):
            start = len(self.frames())
            result = self.lift(reference, path, layout, plan=True)
            self.audit.verify_read_only(self.frames()[start:])
            verify_lift_summary(json.loads(result.stdout), expected_receipt, path.read_bytes())
            self.pair_proof(receipt)

        control(receipt_path, receipt)
        refusal("missing-evidence-flag", supplied=False)
        refusal("missing-evidence-file", evidence=self.work / "receipt/missing.json")
        control(receipt_path, receipt)
        refusal("different-S-same-G", "native-suite@" + changed_s, selected_layout=changed_layout)
        reconstructed = namespace_evidence(receipt, receipt["subject"]["Target"]["NamespaceUID"])
        path = self.work / "receipt/namespace-control.json"
        path.write_bytes(canonical(reconstructed))
        control(path, reconstructed)
        negative = namespace_evidence(receipt, "counterfactual-namespace-uid")
        path = self.work / "receipt/namespace-negative.json"
        path.write_bytes(canonical(negative))
        refusal("counterfactual-namespace-UID", evidence=path)
        resource = "providers.core.orka.ai"
        marker = "kaimahi.dev/suite"
        control(receipt_path, receipt)
        self.kube("-n", "orka-system", "annotate", resource, member, marker + "=foreign-owner", "--overwrite")
        try:
            refusal("foreign-ownership")
        finally:
            self.kube("-n", "orka-system", "annotate", resource, member, marker + "=" + receipt["subject"]["Suite"], "--overwrite")
        control(receipt_path, receipt)
        # Select a distinct live Ready inference identity, leaving the original
        # prerequisite coherent for the final isolated spec-drift negative.
        drift = "native-suite-inference-drift"
        require(not self.kube("-n", "orka-system", "get", resource, drift, "--ignore-not-found", "-o", "name").strip(), "binding-fixture-exists")
        provider = self.get(resource, "native-suite-inference")
        self.kube("create", "-f", "-", raw=canonical({"apiVersion": provider["apiVersion"], "kind": "Provider",
                  "metadata": {"name": drift, "namespace": "orka-system"}, "spec": provider["spec"]}))
        for _ in range(30):
            observed = self.get(resource, drift)
            if observed.get("status", {}).get("ready") is True:
                break
            time.sleep(1)
        require(observed.get("status", {}).get("ready") is True and observed["metadata"]["uid"] != provider["metadata"]["uid"], "binding-fixture-ready")
        refusal("different-live-inference-identity", inference=drift)
        control(receipt_path, receipt)
        # Last: spec generation cannot be restored to the evaluated identity.
        # No later reuse/evaluation/readiness claim uses this altered pair.
        agent = self.get("agents.core.orka.ai", member)
        patch = spec_drift_patch(agent)
        self.kube("-n", "orka-system", "patch", "agents.core.orka.ai", member, "--type=merge", "-p", json.dumps(patch))
        changed = self.get("agents.core.orka.ai", member)
        require(changed["metadata"]["uid"] == agent["metadata"]["uid"] and
                changed["metadata"]["generation"] > agent["metadata"]["generation"] and
                changed["spec"]["model"] == patch["spec"]["model"], "spec-drift-not-admitted")
        refusal("terminal-spec-drift")
        return outcomes

    def author(self, name, directory, instructions, cases):
        self.cli("create", name, "--workspace", directory, "--model", "qwen-3.5-2b", "--instructions", instructions)
        (directory / "eval/example.yaml").unlink()
        for filename, raw in cases.items():
            (directory / "eval" / filename).write_bytes(raw)

    def evaluation(self, directory, output, success=True):
        return self.cli("eval", "--workspace", directory, "--to-context", self.context, "--to-namespace", "orka-system",
                        "--inference", "provider:native-suite-inference", "--case-timeout", "5m", "--out", output,
                        timeout=660, success=success)

    def live_subject(self, subject):
        target = {"Context": self.context, "Namespace": "orka-system",
                  "ClusterUID": json.loads(self.kube("get", "namespace", "kube-system", "-o", "json"))["metadata"]["uid"],
                  "NamespaceUID": json.loads(self.kube("get", "namespace", "orka-system", "-o", "json"))["metadata"]["uid"]}
        require(subject["Target"] == target, "target-identity")
        selected = self.get("providers.core.orka.ai", "native-suite-inference")
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
        instructions.write_text(yaml.safe_load((public / "agent.yaml").read_bytes())["spec"]["instructions"])
        cases = {p.name: p.read_bytes() for p in sorted((public / "eval").glob("*.yaml"))}
        require(len(cases) == 2, "fixture-cases")
        workspace, receipt_path = self.work / "workspace", self.work / "receipt/passing.json"
        member = "ci-native-suite"
        self.author(member, workspace, instructions, cases)
        original = snapshot(workspace)
        require(not self.frames(), "offline-audit")
        self.phase = "evaluate"
        self.evaluation(workspace, receipt_path)  # exactly ONE positive evaluation, never retried
        receipt = json.loads(receipt_path.read_bytes())
        require(receipt_path.stat().st_mode & 0o777 == 0o600 and receipt["result"] == "pass" and
                receipt["sourceFingerprint"] == fingerprint(original) and snapshot(workspace) == original, "receipt-source")
        subject = receipt["subject"]
        require(subject["Member"] == member and subject["Model"] == "qwen-3.5-2b" and subject["RequiredImages"] == [], "receipt-member")
        verify_cases(receipt["cases"], cases)
        require(receipt["casesDigest"] == sha(b"".join(frame("eval/" + n, b) for n, b in sorted(cases.items()))), "case-set")
        self.audit.verify(self.frames(), member, receipt["cases"])
        self.phase = "readback"
        self.live_subject(subject)
        for kind in ("Provider", "Agent"):
            inspect_pair(self.get(kind.lower() + "s.core.orka.ai", member), kind, receipt[kind.lower()], subject)
        tasks = []
        for row, raw in zip(receipt["cases"], cases.values()):
            observed = inspect_task(self.get("tasks.core.orka.ai", row["taskName"]), row, member, yaml.safe_load(raw)["input"])
            answer = self.cli("--context", self.context, "task", "result", row["taskName"], "--namespace", "orka-system").stdout
            require(result_digest(answer) == row["answerDigest"], "answer-hash")
            require(inspect_task(self.get("tasks.core.orka.ai", row["taskName"]), row, member, yaml.safe_load(raw)["input"]) == observed, "task-drift")
            tasks.append(observed)
        self.phase = "transfer"
        layout, extracted, reference = self.work / "layout", self.work / "pulled", "native-suite:v1"
        self.cli("push", "--workspace", workspace, "--evaluation", receipt_path, "--to-layout", layout, reference)
        self.cli("suite", "pull", "--from-layout", layout, "--output", extracted, reference)
        verify_transfer(layout, extracted, subject, cases)
        lift_results = self.lift_qualification(workspace, layout, receipt_path, receipt, cases)
        before = snapshot(layout)
        case_path = workspace / "eval" / sorted(cases)[0]
        case_path.write_bytes(case_path.read_bytes() + b"\n")
        rejected = self.cli("push", "--workspace", workspace, "--evaluation", receipt_path, "--to-layout", layout, reference, success=False)
        verify_refusal(rejected.returncode, rejected.stderr, [], "expected publication identity")
        require(snapshot(layout) == before, "destination-changed")
        changed_layout, changed = self.work / "changed-layout", self.work / "changed-pulled"
        self.cli("push", "--workspace", workspace, "--to-layout", changed_layout, reference)
        self.cli("suite", "pull", "--from-layout", changed_layout, "--output", changed, reference)
        changed_s = json.loads((changed_layout / "index.json").read_bytes())["manifests"][0]["digest"]
        require(changed_s != subject["SuiteDigest"] and sha(canonical(json.loads((changed / "agentsuite.json").read_bytes()))) == subject["GraphDigest"][7:], "mutation-S-not-G")
        case_path.write_bytes(cases[case_path.name])
        self.refusals(workspace, instructions, cases, subject)
        lift_results["denials"] = self.lift_denials(layout, changed_layout, changed_s, receipt_path, receipt)
        self.artifacts(receipt_path, receipt, tasks, lift_results)

    def refusals(self, workspace, instructions, cases, subject):
        self.phase = "refusals"
        member = subject["Member"]
        case_path = workspace / "eval" / sorted(cases)[0]
        legacy_args = ["--context", self.context, "create", "--live", member, "--namespace", "orka-system", "--provider-type", "openai",
                       "--model", "qwen-3.5-2b", "--secret", self.secret_ref["name"], "--secret-key", self.secret_ref["key"],
                       "--base-url", "http://native-suite-aikit.orka-system.svc.cluster.local:8080/v1", "--instructions", instructions]
        # Use the existing offline legacy renderer/writer: a real lift bundle
        # needs both PortableAgent and OrkaBindings, not just agent.yaml.
        copy_bundle = self.work / "native-copy-bundle"
        start = len(self.frames())
        self.cli(*legacy_args, "--no-apply", "--out", self.work / "native-copy.yaml", "--bundle-path", copy_bundle)
        require(len(self.frames()) == start, "copy-fixture-kube-call")
        require(all((copy_bundle / name).is_file() for name in ("agent.yaml", "bindings.yaml")), "copy-fixture-incomplete")
        rejected = self.cli("bundle", "lift", copy_bundle, "--to-context", self.context, "--to-namespace", "orka-system",
                            "--inference", "provider:native-suite-inference", success=False)
        verify_refusal(rejected.returncode, rejected.stderr, self.frames()[start:], "reserves native suite ownership")
        legacy_args += ["--out", self.work / "legacy.yaml", "--bundle-path", self.work / "legacy-bundle"]
        start = len(self.frames())
        rejected = self.cli(*legacy_args, success=False)
        verify_refusal(rejected.returncode, rejected.stderr, self.frames()[start:], "reserves native suite ownership")
        collision, collision_member = self.work / "collision", "ci-native-collision"
        legacy_args[4] = collision_member
        legacy_args[legacy_args.index("--out") + 1] = self.work / "collision-legacy.yaml"
        legacy_args[legacy_args.index("--bundle-path") + 1] = self.work / "collision-legacy-bundle"
        # A normal legacy create applies no Task. Remove only this deliberately
        # created fixture's legacy markers externally to qualify unmarked refusal.
        self.cli(*legacy_args)
        for resource in ("providers.core.orka.ai", "agents.core.orka.ai"):
            self.kube("-n", "orka-system", "annotate", resource, collision_member,
                      "kaimahi.dev/bundle-", "kaimahi.dev/portable-digest-", "kaimahi.dev/rendered-digest-")
        self.author(collision_member, collision, instructions, cases)
        pair_before = [self.get(k + "s.core.orka.ai", collision_member) for k in ("provider", "agent")]
        start = len(self.frames())
        rejected = self.evaluation(collision, self.work / "receipt/collision.json", success=False)
        refusal_frames = self.frames()[start:]
        verify_collision_refusal(rejected.returncode, rejected.stderr, refusal_frames, collision_member)
        require(not (self.work / "receipt/collision.json").exists(), "collision-export")
        self.live_subject(subject)
        require(pair_before == [self.get(k + "s.core.orka.ai", collision_member) for k in ("provider", "agent")], "collision-drift")
        unsupported = yaml.safe_load(cases[sorted(cases)[0]])
        unsupported["assertions"] = [{"id": "unsupported", "type": "toolCalled", "tool": "k8s-get-resources"}]
        case_path.write_text(yaml.safe_dump(unsupported))
        start = len(self.frames())
        rejected = self.evaluation(workspace, self.work / "receipt/unsupported.json", success=False)
        verify_refusal(rejected.returncode, rejected.stderr, self.frames()[start:], "unsupported native suite evaluator")
        require(len(self.frames()) == start and not (self.work / "receipt/unsupported.json").exists(), "unsupported-kube-call")
    def artifacts(self, receipt_path, receipt, tasks, lift_results):
        self.phase = "artifacts"
        subject = receipt["subject"]
        artifacts = self.work / "artifacts"
        artifacts.mkdir(mode=0o700)
        summary = {"schemaVersion": 1, "suiteDigest": subject["SuiteDigest"], "graphDigest": subject["GraphDigest"],
                   "sourceFingerprint": receipt["sourceFingerprint"], "receiptDigest": sha(receipt_path.read_bytes()),
                   "caseCount": len(tasks), "requiredImageCount": 0, "destinationEvaluated": False,
                   "ownershipRefusals": True, "unsupportedRefusal": True, "caseMutationRefusal": True,
                   "lift": lift_results}
        for i, task in enumerate(tasks):
            summary.update({"task" + str(i + 1) + k.capitalize(): v for k, v in task.items()})
        (artifacts / "qualification.json").write_bytes(canonical(summary))
        shutil.copyfile(receipt_path, artifacts / "passing.json")
        print("native qualification: full pass; exact transfer; registry/local CLI lift reuse, fresh Tasks and refusals verified")


def main():
    qualification = None
    try:
        os.umask(0o077)
        require(len(sys.argv) == 3 and re.fullmatch(r"kind-[a-z0-9-]+", sys.argv[1]) and
                sys.argv[1] == "kind-" + os.environ.get("KIND_CLUSTER", "") and os.environ.get("KMX_ORKA_RUNTIME_CREATED") == "1", "owned-context")
        qualification = Qualification(sys.argv[1], sys.argv[2])
        qualification.qualify()
        return 0
    except BaseException:
        phase = qualification.phase if qualification else "preflight"
        print("native qualification: phase=" + phase + " reason=failed", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
