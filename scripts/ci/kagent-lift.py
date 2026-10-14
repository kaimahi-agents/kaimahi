#!/usr/bin/env python3
"""External pinned bootstrap, Go lift/status, native A2A and product eval proof.

All external output is private. Public failures use closed phases and codes.
"""
import contextlib
import hashlib
import json
import os
import pathlib
import re
import secrets
import stat
import subprocess
import sys
import tempfile
import time
import urllib.request

import yaml

CONTEXT = "kind-kmx-kagent-lift"
NAMESPACE = "agents"
CONTROLLER = "b2572a59386b6bac1f5e652e135fdad54c0ee603d3f9ee60eb7c80bbaedf9b76"
GO_ADK = "5ee9592cb7d959538d0f0fe36acf31ba3a06de084f84048b1459c3bdfafcda1f"
PYTHON_ADK = "9a41ef053b264630ae078d2d2c7c72a85d5d77e6b2ce392169006f84055dacfb"
POSTGRES = "885cf05d376c7cf27afef02073e6bdac3841252537f16e244fd1c1e6a7c99fb1"
FIXTURE = "2d9aefe2fef018a7eb2c13064c89c71929800fd2e5dccdbf52ea5da5bb8d929a"
NODE_IMAGE = "kindest/node:v1.35.0@sha256:4613778f3cfcd10e615029370f5786704559103cf27bef934597ba562b269661"
# CI is Linux amd64. Do not accept a digest from a different platform.
IMAGES = {
    "controller": ("ghcr.io/kagent-dev/kagent/controller", CONTROLLER,
                   "8ca13042c21181ecf280d6f3ca005d58e216a0a56978bb8824fd73cc02b685d8"),
    "kagent": ("ghcr.io/kagent-dev/kagent/golang-adk", GO_ADK,
               "7f788d09d5f21ccc3068679310943dcf4f0df19e1a93960423810438f4238b24"),
    "postgresql": ("docker.io/library/postgres", POSTGRES,
                   "3928680cee9028902891672c0a6ce84de58ed3eb1b987588e7c9424d8f08d21d"),
    "fixture": ("docker.io/library/python", FIXTURE,
                "ba27df8b6499cc3756cff167690f3fcb5321faa1703b3a914df5f4fdadb32f05"),
}


class QualificationError(ValueError):
    """Private failure detail with a separately whitelisted public reason."""

    def __init__(self, message, *, reason="qualification-check-failed"):
        super().__init__(message)
        self.reason = reason


def require(condition, message, *, reason="qualification-check-failed"):
    if not condition:
        raise QualificationError(message, reason=reason)


PHASES = {"operator-patch", "cluster-inventory", "node-container-inspect", "node-image-inspect", "resource-get",
          "namespace-create", "secret-create", "fixture-apply", "fixture-ready", "controller-version"}


def run(argv, *, phase, data=None, timeout=120):
    require(phase in PHASES, "unknown external phase")
    try:
        result = subprocess.run(argv, input=data, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=timeout)
    except subprocess.TimeoutExpired:
        raise QualificationError("phase=" + phase + " timeout") from None
    except FileNotFoundError:
        raise QualificationError("phase=" + phase + " missing-executable") from None
    require(result.returncode == 0, "phase=" + phase + " exitstatus=" + str(result.returncode))
    return result.stdout


def kube(*args, phase, data=None):
    return run(["kubectl", "--context", CONTEXT, *args], phase=phase, data=data)


def get(kind, name=None, namespace=NAMESPACE, optional=False):
    args = ["get", kind]
    if name:
        args.append(name)
    if namespace:
        args += ["-n", namespace]
    if optional:
        args.append("--ignore-not-found=true")
    raw = kube(*args, "-o", "json", phase="resource-get")
    return json.loads(raw) if raw.strip() else None


def image_digest(image_id, repository, allowed):
    value = image_id.split("://", 1)[-1]
    if "@" in value:
        repo, value = value.split("@", 1)
        aliases = {repository, repository.removeprefix("docker.io/")}
        require(repo in aliases, "running image repository differs from locked repository")
    require(re.fullmatch(r"sha256:[0-9a-f]{64}", value) is not None, "running imageID is not one manifest digest")
    value = value.removeprefix("sha256:")
    require(value in allowed, "running imageID is not the locked amd64 manifest/index")
    return value


def values():
    optional = ["kmcp", "substrate", "kagent-tools", "k8s-agent", "kgateway-agent", "istio-agent",
                "promql-agent", "observability-agent", "argo-rollouts-agent", "helm-agent", "cilium-policy-agent",
                "cilium-manager-agent", "cilium-debug-agent", "grafana-mcp", "oauth2-proxy"]
    result = {name: {"enabled": False} for name in optional}
    result.update({
        "controller": {"loglevel": "error", "image": {"tag": "0.10.3@sha256:" + CONTROLLER},
                       "goAgentImage": {"tag": "0.10.3@sha256:" + GO_ADK},
                       "agentImage": {"tag": "0.10.3@sha256:" + PYTHON_ADK}},
        "database": {"postgres": {"bundled": {"image": {"tag": "18.6-alpine3.23@sha256:" + POSTGRES}}}},
        "ui": {"replicas": 0, "route": {"enabled": False}},
        "providers": {"default": "ollama", "ollama": {"config": {"host": "http://unused.invalid:11434"}}},
        "otel": {"tracing": {"enabled": False}, "logging": {"enabled": False}},
    })
    return result


def check_chart(charts, rendered):
    for directory, name in (("crds", "kagent-crds"), ("runtime", "kagent")):
        chart = yaml.safe_load((charts / directory / name / "Chart.yaml").read_text())
        require(chart["name"] == name and str(chart["version"]) == "0.10.3", "chart identity differs from exact pin")
    deployments = [obj for obj in rendered if obj and obj.get("kind") == "Deployment" and obj["spec"].get("replicas", 1)]
    require({obj["metadata"]["name"] for obj in deployments} == {"kagent-controller", "kagent-postgresql"},
            "optional agents/subcharts/UI must not run")
    containers = [c for obj in deployments for c in obj["spec"]["template"]["spec"]["containers"]]
    require({c["image"] for c in containers} == {
        "ghcr.io/kagent-dev/kagent/controller:0.10.3@sha256:" + CONTROLLER,
        "docker.io/library/postgres:18.6-alpine3.23@sha256:" + POSTGRES}, "chart did not honor image digest overrides")
    configs = [obj for obj in rendered if obj and obj.get("kind") == "ConfigMap" and obj["metadata"]["name"] == "kagent-controller"]
    require(len(configs) == 1 and configs[0]["data"]["GO_IMAGE_TAG"] == "0.10.3@sha256:" + GO_ADK,
            "controller must configure exact Go ADK digest")
    require(configs[0]["data"]["IMAGE_TAG"] == "0.10.3@sha256:" + PYTHON_ADK, "unused Python ADK configuration must be locked")


def cluster_absent():
    clusters = run(["kind", "get", "clusters"], phase="cluster-inventory").decode().splitlines()
    require("kmx-kagent-lift" not in clusters, "refusing to reuse/delete a preexisting dedicated cluster")


def node():
    container = json.loads(run(["docker", "inspect", "kmx-kagent-lift-control-plane"], phase="node-container-inspect"))[0]
    image = json.loads(run(["docker", "image", "inspect", NODE_IMAGE], phase="node-image-inspect"))[0]
    require(container["Config"]["Image"] == NODE_IMAGE and container["Image"] == image["Id"], "kind node image identity differs")
    require(any(ref.endswith("@sha256:4613778f3cfcd10e615029370f5786704559103cf27bef934597ba562b269661")
                or ref.endswith("@sha256:986401fce0e567a9860075ba9df8c8459bd99a35f79024d8a682773fb40f0753")
                for ref in image.get("RepoDigests", [])), "kind node lacks locked index/platform identity")


def fixture_manifest():
    source = pathlib.Path(__file__).with_name("kagent-lift-fixture.py").read_text()
    return [
        {"apiVersion": "v1", "kind": "ConfigMap", "metadata": {"name": "lift-fixture"}, "data": {"server.py": source}},
        {"apiVersion": "apps/v1", "kind": "Deployment", "metadata": {"name": "lift-fixture"}, "spec": {
            "replicas": 1, "selector": {"matchLabels": {"app": "lift-fixture"}}, "template": {
                "metadata": {"labels": {"app": "lift-fixture"}}, "spec": {
                    "automountServiceAccountToken": False,
                    "securityContext": {"runAsNonRoot": True, "runAsUser": 1000, "runAsGroup": 1000,
                                        "seccompProfile": {"type": "RuntimeDefault"}},
                    "containers": [{"name": "fixture", "image": "docker.io/library/python:3.13-alpine@sha256:" + FIXTURE,
                                    "command": ["python3", "-B", "/app/server.py"], "ports": [{"name": "http", "containerPort": 9000}],
                                    "readinessProbe": {"httpGet": {"path": "/healthz", "port": "http"}, "periodSeconds": 1},
                                    "resources": {"requests": {"cpu": "10m", "memory": "24Mi"},
                                                  "limits": {"cpu": "100m", "memory": "64Mi"}},
                                    "securityContext": {"allowPrivilegeEscalation": False, "readOnlyRootFilesystem": True,
                                                        "capabilities": {"drop": ["ALL"]}},
                                    "volumeMounts": [{"name": "source", "mountPath": "/app", "readOnly": True},
                                                     {"name": "tmp", "mountPath": "/tmp"}]}],
                    "volumes": [{"name": "source", "configMap": {"name": "lift-fixture"}},
                                {"name": "tmp", "emptyDir": {"sizeLimit": "8Mi"}}]}}}},
        {"apiVersion": "v1", "kind": "Service", "metadata": {"name": "lift-fixture"},
         "spec": {"selector": {"app": "lift-fixture"}, "ports": [{"name": "http", "port": 9000, "targetPort": "http"}]}},
        {"apiVersion": "kagent.dev/v1alpha2", "kind": "RemoteMCPServer", "metadata": {"name": "lift-tools"},
         "spec": {"description": "Deterministic allowed CI tool", "protocol": "STREAMABLE_HTTP",
                  "url": "http://lift-fixture.agents.svc.cluster.local:9000/mcp"}},
    ]


def provision(work):
    namespaces = (NAMESPACE, "suite-destination")
    for namespace in namespaces:
        kube("create", "namespace", namespace, phase="namespace-create")
    # Dummy authority is created independently. Never put its value in argv.
    secret = work / "dummy-key"
    secret.write_text(secrets.token_hex(24))
    secret.chmod(0o600)
    try:
        for namespace in namespaces:
            kube("-n", namespace, "create", "secret", "generic", "lift-model-key", "--from-file=api-key=" + str(secret), phase="secret-create")
    finally:
        secret.unlink()
    kube("-n", NAMESPACE, "apply", "-f", "-", data=yaml.safe_dump_all(fixture_manifest()).encode(), phase="fixture-apply")
    kube("-n", NAMESPACE, "rollout", "status", "deployment/lift-fixture", "--timeout=180s", phase="fixture-ready")
    deadline = time.monotonic() + 120
    while time.monotonic() < deadline:
        remote = get("remotemcpservers.kagent.dev", "lift-tools")
        if any(c.get("type") == "Accepted" and c.get("status") == "True" for c in remote.get("status", {}).get("conditions", [])) \
                and [t["name"] for t in remote.get("status", {}).get("discoveredTools", [])] == ["read_sentinel"]:
            return
        time.sleep(1)
    raise QualificationError("RemoteMCPServer never accepted/discovered the sole allowed tool")


@contextlib.contextmanager
def forward(service, remote_port, namespace=NAMESPACE):
    # kubectl allocates an available loopback port; its own stdout proves the
    # port belongs to this child process. No preselected/shared forward.
    with tempfile.TemporaryFile() as log:
        process = subprocess.Popen(["kubectl", "--context", CONTEXT, "-n", namespace, "port-forward", "--address", "127.0.0.1",
                                    "service/" + service, ":" + str(remote_port)], stdout=log, stderr=log)
        try:
            deadline = time.monotonic() + 30
            port = None
            while time.monotonic() < deadline and process.poll() is None:
                log.seek(0)
                match = re.search(rb"Forwarding from 127\.0\.0\.1:(\d+) ->", log.read())
                if match:
                    port = int(match[1])
                    break
                time.sleep(0.1)
            require(port is not None and process.poll() is None, "owned service forward could not bind")
            yield "http://127.0.0.1:" + str(port)
            require(process.poll() is None, "owned forward exited during bootstrap")
        finally:
            if process.poll() is None:
                process.terminate()
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()


def http_json(url, data=None):
    body = None if data is None else json.dumps(data).encode()
    request = urllib.request.Request(url, body, {"Content-Type": "application/json", "Accept": "application/json, text/event-stream"})
    # Bootstrap performs health and discovery requests, never tool execution.
    with urllib.request.urlopen(request, timeout=180) as response:
        raw = response.read(2 * 1024 * 1024 + 1)
        require(len(raw) <= 2 * 1024 * 1024, "bounded bootstrap response required")
        return json.loads(raw)


def verify_images(include_agent=False):
    found = set()
    evidence = {}
    for ns in ("kagent", NAMESPACE):
        for pod in get("pods", namespace=ns)["items"]:
            if pod["metadata"].get("deletionTimestamp"):
                continue
            for container in pod["spec"].get("initContainers", []) + pod["spec"]["containers"]:
                name = container["name"]
                role = name
                require(role in IMAGES, "unexpected unqualified running container")
                statuses = pod.get("status", {}).get("initContainerStatuses", []) + pod.get("status", {}).get("containerStatuses", [])
                status = next((s for s in statuses if s["name"] == name), {})
                require(pod.get("status", {}).get("phase") == "Running" and status.get("ready") is True,
                        "container not running and ready")
                repository, index, platform = IMAGES[role]
                evidence[role] = image_digest(status.get("imageID", ""), repository, {index, platform})
                found.add(role)
    expected = {"controller", "postgresql", "fixture"} | ({"kagent"} if include_agent else set())
    require(found == expected, "required running image evidence missing")
    return evidence


def bootstrap():
    images = verify_images()
    raw = kube("get", "--raw", "/api/v1/namespaces/kagent/services/http:kagent-controller:8083/proxy/version", phase="controller-version")
    version = json.loads(raw)
    require(version.get("kagent_version") == "0.10.3" and version.get("git_commit") in {
        "8878c39", "8878c39ac64421cb665c8e62ecc6411dd12f2fc2"}, "controller build differs from exact release")
    with forward("lift-fixture", 9000) as url:
        require(http_json(url + "/healthz") == {"ready": True}, "fixture health failed")
        reply = http_json(url + "/mcp", {"jsonrpc": "2.0", "id": 1, "method": "initialize",
                         "params": {"protocolVersion": "2025-03-26", "capabilities": {},
                                    "clientInfo": {"name": "kmx-bootstrap", "version": "1"}}})
        require(reply.get("result", {}).get("protocolVersion") == "2025-03-26", "MCP initialization failed")
        tools = http_json(url + "/mcp", {"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": {}})
        require([t["name"] for t in tools["result"]["tools"]] == ["read_sentinel"], "MCP discovery not allowlisted")
        require(http_json(url + "/evidence")["tool_calls"] == 0, "bootstrap invoked the actual proof tool")
    print("pinned node/charts/controller/Postgres/fixture images and MCP discovery verified")
    print("verified_imageIDs=" + json.dumps(images, sort_keys=True))


NAME = "lift-proof"


def digest(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode()).hexdigest()


def project_identity(objects):
    result = {}
    for obj in objects:
        metadata = obj["metadata"]
        key = obj["kind"] + "/" + metadata["name"]
        require(metadata.get("uid") and key not in result, "resource identity is missing or ambiguous")
        markers = ("kaimahi.dev/bundle", "kaimahi.dev/runtime-version", "kaimahi.dev/portable-digest", "kaimahi.dev/rendered-digest")
        ownership = {"managed_by": metadata.get("labels", {}).get("app.kubernetes.io/managed-by"),
                     "markers": {marker: metadata.get("annotations", {}).get(marker) for marker in markers},
                     "owner_references": [{field: ref.get(field) for field in
                         ("apiVersion", "kind", "name", "uid", "controller", "blockOwnerDeletion")}
                         for ref in metadata.get("ownerReferences", [])]}
        value = {"uid": metadata["uid"], "namespace": metadata.get("namespace", ""),
                 "generation": metadata.get("generation", 0), "ownership_sha256": digest(ownership),
                 "terminating": bool(metadata.get("deletionTimestamp"))}
        # Controller-updated status/resourceVersion churn is not mutation proof.
        if obj["kind"] in {"Secret", "ConfigMap", "ServiceAccount"}:
            value["resource_version"] = metadata["resourceVersion"]
        if obj["kind"] != "Secret":
            value["spec_sha256"] = digest(obj.get("spec", obj.get("data", {})))
        result[key] = value
    return result


def snapshot():
    objects = []
    for kind, name, namespace in (
        ("agents.kagent.dev", NAME, NAMESPACE), ("modelconfigs.kagent.dev", NAME, NAMESPACE),
        ("deployment", NAME, NAMESPACE), ("service", NAME, NAMESPACE), ("serviceaccount", NAME, NAMESPACE),
        ("secret", NAME, NAMESPACE), ("secret", "lift-model-key", NAMESPACE),
        ("remotemcpservers.kagent.dev", "lift-tools", NAMESPACE),
        ("deployment", "lift-fixture", NAMESPACE), ("service", "lift-fixture", NAMESPACE),
        ("configmap", "lift-fixture", NAMESPACE), ("deployment", "kagent-controller", "kagent"),
        ("configmap", "kagent-controller", "kagent"), ("service", "kagent-controller", "kagent"),
        ("deployment", "kagent-postgresql", "kagent")):
        obj = get(kind, name, namespace, optional=True)
        if obj:
            objects.append(obj)
    return project_identity(objects)


def validate_read_only(phase, before, after, local_before, local_after, writes, expected_writes):
    if before != after:
        changes = [{"resource": key, "fields": sorted(field for field in set(before.get(key, {})) | set(after.get(key, {}))
                    if before.get(key, {}).get(field) != after.get(key, {}).get(field))}
                   for key in sorted(set(before) | set(after)) if before.get(key) != after.get(key)]
        print("evidence_changes=" + json.dumps(changes, sort_keys=True), flush=True)
    require(before == after, phase + " changed cluster evidence")
    require(local_before == local_after, phase + " changed local evidence")
    require(writes == expected_writes, phase + " changed mutation audit")


def filesystem(*roots):
    # Directory identities detect local state created by --plan/status too.
    return {str(path): (stat.S_IMODE(path.stat().st_mode), None if path.is_dir() else hashlib.sha256(path.read_bytes()).hexdigest())
            for root in roots for path in ([root] + sorted(root.rglob("*"))) if path.exists()}


def validate_a2a(response, evidence):
    require(response.get("jsonrpc") == "2.0" and response.get("id") == "kmx-lift-once" and "error" not in response,
            "native A2A returned an invalid/error response; do not resend")
    result = response.get("result", {})
    context_id = result.get("contextId")
    valid_id = lambda value: isinstance(value, str) and re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._:-]{0,127}", value) is not None
    if result.get("kind") == "task":
        native_id = result.get("id")
        require(valid_id(native_id) and valid_id(context_id) and "messageId" not in result,
                "native A2A task/context identity is missing, invalid or conflicting; do not resend")
        require(result.get("status", {}).get("state") == "completed", "native A2A task did not terminally complete; do not resend")
        parts = [part for artifact in result.get("artifacts", []) for part in artifact.get("parts", [])]
    elif result.get("kind") == "message":
        native_id = result.get("messageId")
        require(valid_id(native_id) and "id" not in result and ("contextId" not in result or valid_id(context_id)),
                "native A2A message/context identity is missing, invalid or conflicting; do not resend")
        require(result.get("role") == "agent", "native A2A message is not an agent answer")
        parts = result.get("parts", [])
    else:
        raise QualificationError("native A2A returned no terminal task/message; do not resend")
    text = "\n".join(part["text"] for part in parts if part.get("kind") == "text" and isinstance(part.get("text"), str))
    require(text.strip(), "native terminal answer has no text")
    markers = re.findall(r"kmx-mcp-[A-Za-z0-9-]+", text)
    require(any(hashlib.sha256(marker.encode()).hexdigest() == evidence.get("sentinel_sha256") for marker in markers),
            "terminal text did not return the actual MCP sentinel")
    require(evidence.get("tool_calls") == 1 and evidence.get("model_calls") == 2 and evidence.get("terminal_answers") == 1,
            "one actual MCP call and tool-then-terminal model calls were not observed")
    proof = {"answer_sha256": hashlib.sha256(text.encode()).hexdigest(), "native_kind": result["kind"],
             "native_id": native_id, "tool_calls": 1, "model_calls": 2}
    if context_id is not None:
        proof["native_context_id"] = context_id
    return proof


def one_message(endpoint, fixture):
    before = http_json(fixture + "/evidence")
    require(all(before.get(key) == 0 for key in ("model_calls", "tool_calls", "terminal_answers")), "fixture was already executed; do not resend")
    request = {"jsonrpc": "2.0", "id": "kmx-lift-once", "method": "message/send", "params": {
        "message": {"kind": "message", "role": "user", "messageId": "kmx-lift-one-message",
                    "parts": [{"kind": "text", "text": "Use the allowed tool and return its result."}]},
        "configuration": {"blocking": True}}}
    response = http_json(endpoint, request)
    return validate_a2a(response, http_json(fixture + "/evidence"))


def bundle_files(work):
    bundle = work / "bundle"
    bundle.mkdir(mode=0o700)
    agent = {"apiVersion": "kmx.kaimahi.dev/v1alpha1", "kind": "PortableAgent", "metadata": {"name": NAME},
             "spec": {"description": "Deterministic Go lift qualification", "instructions": "Use the allowed tool and return its result.",
                      "model": {"name": "fixture"}},
             "extensions": {"kagent": {"apiVersion": "kagent.dev/v1alpha2", "runtime": "go", "tools": [{
                 "server": {"kind": "RemoteMCPServer", "name": "lift-tools"}, "toolNames": ["read_sentinel"]}]}}}
    (bundle / "agent.yaml").write_text(yaml.safe_dump(agent, sort_keys=False))
    bindings = {"apiVersion": "kmx.kaimahi.dev/v1alpha1", "kind": "KagentBindings", "namespace": NAMESPACE,
                "modelConfig": {"provider": "openai", "baseURL": "http://lift-fixture.agents.svc.cluster.local:9000/v1",
                                "secretRef": {"name": "lift-model-key", "key": "api-key"}}}
    # Status must read author-owned external bindings, never infer live values.
    path = work / "external-bindings.yaml"
    path.write_text(yaml.safe_dump(bindings, sort_keys=False))
    return bundle, path


def hash_bytes(data):
    return hashlib.sha256(data).hexdigest()


def framed_digest(entries):
    return hash_bytes(b"".join(name.encode() + b" " + str(len(data)).encode() + b"\n" + data + b"\n" for name, data in entries))


def evaluation_cases(bundle):
    directory = bundle / "eval"
    directory.mkdir(mode=0o700)
    cases = []
    for ordinal in ("one", "two"):
        case = {"id": "case-" + ordinal, "input": "Use the allowed tool and return its result. Qualification case " + ordinal + ".",
                "expectContains": ["KMX MCP proof:"]}
        if ordinal == "one":
            case["assertions"] = [{"id": "quality", "type": "llmJudge", "rubric": "Return the allowed tool result.",
                                    "minScore": 50, "judgeModel": "fixture-judge", "judgeVersion": "1"}]
        path = directory / (case["id"] + ".yaml")
        path.write_text(yaml.safe_dump(case, sort_keys=False))
        path.chmod(0o600)
        cases.append((path, case))
    return cases


def closed(value, fields):
    require(isinstance(value, dict) and set(value) <= set(fields.split()), "evaluation evidence is not payload-free")


def private_file(path):
    info = path.lstat()
    require(stat.S_ISREG(info.st_mode) and stat.S_IMODE(info.st_mode) == 0o600 and info.st_nlink == 1
            and info.st_size <= 1024 * 1024, "evaluation receipt must be a bounded private regular file")
    return path.read_bytes()


def validate_evaluation(report, cases, receipt, deployment_digest, live, uid, images, observed, seen, judge_url):
    closed(report, "schemaVersion runID bundle portableDigest renderedDigest deploymentReceiptDigest casesDigest fullCaseSet gitCommit target result cases")
    require(report.get("schemaVersion") == 2 and report.get("bundle") == NAME and report.get("result") == "pass",
            "product evaluation is not a passing v2 receipt")
    require(report.get("portableDigest") == receipt["PortableDigest"] and report.get("renderedDigest") == receipt["RenderedDigest"]
            and report.get("deploymentReceiptDigest") == deployment_digest, "evaluation source/deployment digests differ")
    require(report.get("casesDigest") == framed_digest([("eval/" + path.name, path.read_bytes()) for path, _ in cases]),
            "evaluation case set digest differs")
    target = report.get("target", {})
    closed(target, "runtime runtimeVersion engine context clusterUID namespace agent agentUID agentGeneration modelConfigUID modelConfigGeneration model platform snapshot")
    expected = {"runtime": "kagent", "runtimeVersion": "v0.10.3", "engine": "go", "context": CONTEXT,
                "clusterUID": uid, "namespace": NAMESPACE, "agent": NAME, "model": "fixture"}
    for field, kind in (("agent", "Agent"), ("modelConfig", "ModelConfig")):
        expected[field + "UID"] = live[kind + "/" + NAME]["uid"]
        expected[field + "Generation"] = live[kind + "/" + NAME]["generation"]
    require(all(target.get(key) == value for key, value in expected.items()), "evaluation actual target identity differs")
    platform = target.get("platform", {})
    closed(platform, "sourceCommit controllerImage adkImage controller")
    require(platform.get("sourceCommit") == "8878c39ac64421cb665c8e62ecc6411dd12f2fc2", "evaluation platform source differs")
    for field, role in (("controllerImage", "controller"), ("adkImage", "kagent")):
        require(platform.get(field) == {"os": "linux", "architecture": "amd64", "digest": images[role]},
                "evaluation platform image differs from actual running imageID")
    resource_fields = "Kind Name Namespace UID Generation ResourceVersion SpecDigest Key"
    controller = platform.get("controller", [])
    require(len(controller) == 3, "evaluation controller platform snapshot is incomplete")
    for row, kind in zip(controller, ("Service", "Deployment", "ConfigMap")):
        closed(row, resource_fields)
        identity = live[kind + "/kagent-controller"]
        require(row.get("Kind") == kind and row.get("Name") == "kagent-controller" and row.get("Namespace") == "kagent"
                and row.get("UID") == identity["uid"] and row.get("Generation", 0) == identity["generation"]
                and row.get("ResourceVersion"), "evaluation controller platform snapshot differs")
    snap = target.get("snapshot", {})
    closed(snap, "proofs workload dependencies")
    for key, saved in (("proofs", "Proofs"), ("workload", "Workload"), ("dependencies", "Dependencies")):
        rows = snap.get(key, [])
        require(len(rows) == len(receipt[saved]) and bool(rows), "evaluation snapshot is incomplete")
        for row, previous in zip(rows, receipt[saved]):
            closed(row, resource_fields)
            require(all(row.get(field, "" if isinstance(value, str) else 0) == value for field, value in previous.items()),
                    "evaluation snapshot differs from bound deployment")
    require(len(report.get("cases", [])) == len(cases) == len(observed), "evaluation case counts differ")
    projected = []
    for row, (path, case), native in zip(report["cases"], cases, observed):
        closed(row, "id verdict caseDigest inputDigest assertions execution answerSHA256 detail")
        require(row.get("id") == case["id"] and row.get("verdict") == "pass" and not row.get("detail")
                and row.get("caseDigest") == hash_bytes(path.read_bytes()) and row.get("inputDigest") == hash_bytes(case["input"].encode()),
                "evaluation case identity/digests differ")
        require(tuple(native.get(key) for key in ("model_calls", "tool_calls", "terminal_answers", "user_turns")) == (2, 1, 1, 1)
                and native.get("input_sha256") == row["inputDigest"], "fixture case counts/fresh user history differ")
        require(row.get("answerSHA256") == native.get("answer_sha256") and re.fullmatch(r"[0-9a-f]{64}", row.get("answerSHA256", "")),
                "evaluation complete answer hash differs from fixture")
        execution = row.get("execution", {})
        closed(execution, "rpcID requestMessageID taskID contextID state")
        require(execution.get("state") == "completed", "evaluation native task did not complete")
        for field in ("rpcID", "requestMessageID", "taskID", "contextID"):
            value = execution.get(field, "")
            require(isinstance(value, str) and re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]{0,255}", value)
                    and value not in seen[field], "evaluation IDs are not fresh across runs/cases")
            seen[field].add(value)
        assertions = row.get("assertions", [])
        require(len(assertions) == 1 + len(case.get("assertions", [])), "evaluation assertion evidence is incomplete")
        for index, assertion in enumerate(assertions):
            closed(assertion, "id type definitionDigest verdict reason judge")
            require(assertion.get("verdict") == "pass", "evaluation assertion did not pass")
            if index == 0:
                require(assertion.get("id") == "expectContains.0" and assertion.get("type") == "contains"
                        and assertion.get("reason") == "assertion satisfied" and "judge" not in assertion
                        and assertion.get("definitionDigest") == framed_digest([("type", b"contains"), ("operand", b"KMX MCP proof:")]),
                        "evaluation contains definition differs")
            else:
                authored = case["assertions"][index - 1]
                judge = assertion.get("judge", {})
                closed(judge, "model modelVersion provenance evaluatorDigest configurationDigest minScore scores")
                require(assertion.get("id") == authored["id"] and assertion.get("type") == "llmJudge"
                        and judge.get("model") == "fixture-judge" and judge.get("modelVersion") == "1"
                        and judge.get("provenance") == "user-declared" and judge.get("minScore") == 50 and judge.get("scores") == [100, 100]
                        and re.fullmatch(r"[0-9a-f]{64}", judge.get("evaluatorDigest", "")), "evaluation two-sample judge differs")
                evaluator = judge["evaluatorDigest"].encode()
                require(judge.get("configurationDigest") == framed_digest([("endpoint", judge_url.encode()), ("model", b"fixture-judge"), ("modelVersion", b"1"), ("evaluatorDigest", evaluator)])
                        and assertion.get("definitionDigest") == framed_digest([("type", b"llmJudge"), ("operand", authored["rubric"].encode()), ("minScore", b"50"), ("judgeModel", b"fixture-judge"), ("judgeVersion", b"1"), ("evaluatorDigest", evaluator)]),
                        "evaluation judge digests differ")
        projected.append({key: row[key] for key in ("id", "caseDigest", "inputDigest", "answerSHA256", "execution")})
    return projected


def product_evaluations(cli, binary, bundle, home, receipt, live, uid, images, fixture, execution):
    cases = evaluation_cases(bundle)
    deploy = list((bundle / "receipts/kagent-v0.10.3").glob("*.json"))
    require(len(deploy) == 1, "one bound deployment receipt required for evaluation")
    deployment_digest = hash_bytes(private_file(deploy[0]))
    key = hash_bytes(("kagent\x00v0.10.3\x00" + CONTEXT + "\x00" + NAMESPACE + "\x00" + uid).encode())
    latest = bundle / ("receipts/eval-kagent-v0.10.3-" + key + ".json")
    seen = {field: set() for field in ("rpcID", "requestMessageID", "taskID", "contextID")}
    seen["taskID"].add(execution["native_id"])
    seen["contextID"].add(execution["native_context_id"])
    runs, archives = [], {}
    for phase, selected in (("product-evaluate", False), ("repeat-evaluate", False), ("selected-evaluate", True)):
        qualification_phase(phase)
        local = filesystem(bundle, home)
        before = http_json(fixture + "/evidence")
        require(len(before.get("cases", [])) == before.get("terminal_answers"), "fixture case counts are incomplete")
        chosen = cases[:1] if selected else cases
        # Normal CLI answers/reasoning are captured, never emitted by qualifier.
        cli("evaluate", "--judge-base-url", fixture + "/v1", *(["--case", "case-one"] if selected else []))
        after = http_json(fixture + "/evidence")
        counts = {field: after.get(field, 0) - before.get(field, 0) for field in ("tool_calls", "model_calls", "terminal_answers", "judge_calls")}
        require(counts == {"tool_calls": len(chosen), "model_calls": 2 * len(chosen), "terminal_answers": len(chosen), "judge_calls": 2},
                "product evaluation fixture case counts differ")
        current = set((bundle / "receipts/runs").glob("eval-*.json"))
        require(all(path in current and private_file(path) == raw for path, raw in archives.items()), "immutable evaluation archive changed")
        new = current - set(archives)
        require(len(new) == 1, "one fresh evaluation archive per run required")
        path = new.pop()
        raw = private_file(path)
        report = json.loads(raw)
        run_id = report.get("runID", "")
        require(isinstance(run_id, str) and re.fullmatch(r"[0-9a-f]{32}", run_id) and path.name == "eval-" + run_id + ".json"
                and path.parent == bundle / "receipts/runs" and run_id not in {r["run_id"] for r in runs}, "evaluation archive/run identity differs")
        require(private_file(latest) == raw, "evaluation latest differs from new archive")
        require(report.get("fullCaseSet") is (not selected), "evaluation full/selected case set marker differs")
        for directory in (bundle / "receipts", path.parent):
            info = directory.lstat()
            require(stat.S_ISDIR(info.st_mode) and stat.S_IMODE(info.st_mode) == 0o700, "evaluation history directories are not private")
        rows = validate_evaluation(report, chosen, receipt, deployment_digest, live, uid, images,
                                   after["cases"][len(before["cases"]):], seen, fixture + "/v1")
        updated = filesystem(bundle, home)
        allowed = {str(path.parent), str(path), str(latest)}
        require(all(local.get(key) == updated.get(key) for key in set(local) | set(updated) if key not in allowed),
                "evaluation changed non-receipt local evidence")
        archives[path] = raw
        runs.append({"run_id": run_id, "full_case_set": not selected, "receipt_sha256": hash_bytes(raw), "cases": rows, "counts": counts})
        if len(runs) == 2:
            qualification_phase("offline-eval-diff")
            result = subprocess.run([str(binary), "eval", "diff", str(next(iter(archives))), str(path)],
                                    stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=30)
            require(result.returncode == 0, "offline repeated evaluation diff failed", reason=cli_failure_reason(result.stderr))
            require(filesystem(bundle, home) == updated and http_json(fixture + "/evidence") == after,
                    "offline evaluation diff changed evidence or invoked fixture")
    evidence = http_json(fixture + "/evidence")
    totals = {field: evidence[field] for field in ("tool_calls", "model_calls", "terminal_answers", "judge_calls")}
    require(totals == {"tool_calls": 6, "model_calls": 12, "terminal_answers": 6, "judge_calls": 6}, "native plus product fixture counts differ")
    return {"runs": runs, "fixture_counts": totals}


def kubectl_audit(work):
    wrapper = work / "fail-bin/kubectl"
    wrapper.unlink()
    audit = work / "kubectl-mutations.jsonl"
    classifier = pathlib.Path(__file__).with_name("kagent-lift-audit.py")
    wrapper.write_text('#!/usr/bin/env python3\nimport os,sys\nos.execv(sys.executable, [sys.executable, '
                       + repr(str(classifier)) + ', ' + repr(str(work / "bin/kubectl")) + ', '
                       + repr(str(audit)) + ', *sys.argv[1:]])\n')
    wrapper.chmod(0o700)
    return audit


def audit_frames(audit):
    return [json.loads(line) for line in audit.read_text().splitlines()] if audit.exists() else []


def validate_receipt(bundle, identities, cluster_uid):
    files = list((bundle / "receipts/kagent-v0.10.3").glob("*.json"))
    require(len(files) == 1 and stat.S_IMODE(files[0].stat().st_mode) == 0o600, "one private lift receipt required")
    wrapped = json.loads(files[0].read_text())
    receipt = wrapped["receipt"]
    require(set(wrapped) <= {"receipt", "gitCommit", "gateOverrideReason", "gateFailedCondition"}
            and set(receipt) <= {"Bundle", "PortableDigest", "RenderedDigest", "Target", "Resources", "RuntimeVersion",
                                 "ClusterUID", "Ready", "ObservedAt", "Proofs", "Dependencies", "Workload"},
            "deployment receipt is not the closed payload-free schema")
    require(receipt["Target"] == {"Runtime": "kagent", "Context": CONTEXT, "Namespace": NAMESPACE}, "receipt target differs")
    require(receipt["Bundle"] == NAME and receipt["RuntimeVersion"] == "v0.10.3" and receipt["ClusterUID"] == cluster_uid
            and receipt["Ready"] is True and receipt["ObservedAt"], "receipt qualification identity is incomplete")
    require(len(receipt["Resources"]) == 2 and len(receipt["Proofs"]) == 2 and len(receipt["Workload"]) == 4,
            "receipt primary/workload proof incomplete")
    for section in ("Resources", "Proofs", "Workload", "Dependencies"):
        expected_kinds = {"ModelConfig", "Agent"} if section in {"Resources", "Proofs"} else (
            {"Deployment", "Service", "ServiceAccount", "Secret"} if section == "Workload" else None)
        if expected_kinds:
            require({r["Kind"] for r in receipt[section]} == expected_kinds, "receipt resource kinds are incomplete")
        for resource in receipt[section]:
            require(set(resource) <= {"Kind", "Name", "Namespace", "UID", "Generation", "Outcome", "ResourceVersion", "SpecDigest", "Key"},
                    "deployment resource evidence is not payload-free")
            identity = identities[resource["Kind"] + "/" + resource["Name"]]
            require(resource["UID"] == identity["uid"] and resource["Namespace"] == NAMESPACE, "receipt resource identity differs")
    for kind in ("Agent", "ModelConfig"):
        obj = get(kind.lower() + "s.kagent.dev", NAME)
        annotations = obj["metadata"].get("annotations", {})
        require(obj["metadata"].get("labels", {}).get("app.kubernetes.io/managed-by") == "kmx", "ownership label missing")
        require(annotations.get("kaimahi.dev/bundle") == NAME and annotations.get("kaimahi.dev/runtime-version") == "v0.10.3",
                "ownership bundle/version marker missing")
        for field, marker in (("PortableDigest", "portable-digest"), ("RenderedDigest", "rendered-digest")):
            require(re.fullmatch(r"[0-9a-f]{64}", receipt[field]) is not None
                    and annotations.get("kaimahi.dev/" + marker) == receipt[field], "receipt digest differs from ownership")
    return receipt


CLI_FAILURE_PATTERNS = (
    (b"pod does not have exactly one controller container using", "controller-pod-reference-mismatch"),
    (b"controller Deployment does not use official image", "controller-deployment-reference-mismatch"),
    (b"ready pod controller container image identity does not match", "controller-running-image-unqualified"),
    (b"Deployment is not a complete current-generation rollout", "controller-rollout-incomplete"),
    (b"Deployment selected an invalid or non-running pod", "controller-pod-state-invalid"),
    (b"Kagent pod node", "controller-node-platform-unqualified"),
    (b"Kagent pod image digest differs", "controller-platform-digest-mismatch"),
    (b"controller workload:", "controller-workload-unqualified"),
    (b"openAPIV3Schema SHA-256 differs", "crd-schema-mismatch"),
    (b"strict Kagent server dry-run", "server-dryrun-refused"),
    (b"dependency", "dependency-unqualified"),
    (b"ModelConfig", "modelconfig-unqualified"), (b"Agent", "agent-unqualified"),
)


def cli_failure_reason(raw):
    # Closed codes only; never replay even surrounding text from known errors.
    return next((code for pattern, code in CLI_FAILURE_PATTERNS if pattern in raw), "unclassified-cli-error")


QUALIFICATION_PHASES = {"setup", "absent-status", "plan", "new-lift", "repeat-lift", "ready-status",
                        "native-a2a-one-shot", "product-evaluate", "repeat-evaluate", "selected-evaluate", "offline-eval-diff",
                        "bindings-and-receipt-conflicts", "operator-drift-conflict"}
QUALIFICATION_REASONS = {code for _, code in CLI_FAILURE_PATTERNS} | {
    "qualification-check-failed", "expected-refusal-not-observed", "unclassified-cli-error", "unclassified-qualifier-error"}
QUALIFICATION_PHASE = "setup"


def qualification_phase(phase):
    global QUALIFICATION_PHASE
    require(phase in QUALIFICATION_PHASES, "unknown qualifier phase", reason="unclassified-qualifier-error")
    QUALIFICATION_PHASE = phase
    if os.environ.get("KMX_QUALIFICATION_DIAGNOSTIC_FD") == "4":
        os.write(4, ("kagent qualification: phase=" + phase + "\n").encode())


def qualification_failure(error):
    # The shell supplies its saved stderr FD only at the qualifier boundary.
    # Neither exception detail nor captured tool output enters this channel.
    reason = error.reason if isinstance(error, QualificationError) else "unclassified-qualifier-error"
    if reason not in QUALIFICATION_REASONS:
        reason = "unclassified-qualifier-error"
    if os.environ.get("KMX_QUALIFICATION_DIAGNOSTIC_FD") == "4":
        os.write(4, ("kagent qualification failed: phase=" + QUALIFICATION_PHASE + " reason=" + reason + "\n").encode())


def qualify(binary, work):
    qualification_phase("setup")
    require(binary.is_file(), "build matching bin/kmx after Go writes settle")
    bundle, bindings = bundle_files(work)
    audit = kubectl_audit(work)
    home = pathlib.Path(os.environ["KMX_HOME"])
    flags = ["--runtime", "kagent", "--runtime-version", "v0.10.3", "--to-context", CONTEXT, "--bindings", str(bindings)]

    def cli(verb, *extra, expected_refusal=None, with_bindings=True):
        selected = flags if with_bindings else flags[:-2]
        result = subprocess.run([str(binary), "bundle", verb, str(bundle), *selected, *extra],
                                stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=300)
        if expected_refusal is not None:
            lines = [line for line in result.stderr.splitlines() if line.strip()]
            require(result.returncode == 1 and bool(lines) and lines[-1] == expected_refusal.encode(),
                    "KMX " + verb + " expected refusal not observed exit=" + str(result.returncode),
                    reason="expected-refusal-not-observed")
        else:
            require(result.returncode == 0,
                    "KMX " + verb + " unexpected exit=" + str(result.returncode), reason=cli_failure_reason(result.stderr))
        return result.stdout

    def frame(kind, strategy, verb="create"):
        return {"verb": verb, "dry_run": strategy, "resources": [{"apiVersion": "kagent.dev/v1alpha2",
                "kind": kind, "namespace": "agents", "name": "lift-proof"}]}

    expected = []
    def read_only(phase, live, local):
        validate_read_only(phase, live, snapshot(), local, filesystem(bundle, home), audit_frames(audit), expected)

    before, local = snapshot(), filesystem(bundle, home)
    qualification_phase("absent-status")
    absent = json.loads(cli("status", "-o", "json"))
    require(len(absent.get("targets", [])) == 1 and absent["targets"][0]["state"] == "absent"
            and absent["targets"][0].get("noReceipt") is True, "new target must report absent without a receipt")
    read_only("absent status", before, local)
    qualification_phase("plan")
    cli("lift", "--plan")
    expected += [frame(kind, "server") for kind in ("ModelConfig", "Agent")]
    read_only("plan", before, local)
    qualification_phase("new-lift")
    cli("lift")
    expected += [frame(kind, strategy) for strategy in ("server", "none") for kind in ("ModelConfig", "Agent")]
    require(audit_frames(audit) == expected, "new lift mutation audit differs from ordered admission/create metadata")
    images = verify_images(include_agent=True)
    live = snapshot()
    uid = get("namespace", "kube-system", namespace=None)["metadata"]["uid"]
    receipt = validate_receipt(bundle, live, uid)
    local = filesystem(bundle, home)
    qualification_phase("repeat-lift")
    cli("lift")
    read_only("repeat lift", live, local)
    qualification_phase("ready-status")
    status = json.loads(cli("status", "-o", "json"))
    validate_status(status, live, uid)
    require(status["portableDigest"] == receipt["PortableDigest"] and status["renderedDigest"] == receipt["RenderedDigest"],
            "status digests differ from saved deployment")
    read_only("status", live, local)
    # Independently native: no KMX execution command, hosted account or resend.
    qualification_phase("native-a2a-one-shot")
    with forward("lift-fixture", 9000) as fixture, forward("kagent-controller", 8083, "kagent") as controller:
        execution = one_message(controller + "/api/a2a/agents/" + NAME, fixture)
    read_only("native execution", live, local)
    with forward("lift-fixture", 9000) as fixture:
        evaluation = product_evaluations(cli, binary, bundle, home, receipt, live, uid, images, fixture, execution)
    local = filesystem(bundle, home)
    read_only("product evaluation", live, local)
    qualification_phase("bindings-and-receipt-conflicts")
    original = bindings.read_bytes()
    changed = yaml.safe_load(original)
    changed["modelConfig"]["baseURL"] = "http://unused.invalid/v1"
    bindings.write_text(yaml.safe_dump(changed))
    local = filesystem(bundle, home)
    cli("lift", expected_refusal="Kagent receipt-bound reuse conflict: drift (desired source or bindings differ from saved deployment)")
    changed_status = json.loads(cli("status", "-o", "json"))
    validate_refusal_status(changed_status, "drift", False, "desired source or bindings differ from saved deployment")
    read_only("changed bindings", live, local)
    bindings.write_bytes(original)
    bindings.rename(work / "saved-bindings.yaml")
    local = filesystem(bundle, home)
    missing = "open Kagent bounded regular file, not a link: no such file or directory"
    cli("status", "-o", "json", expected_refusal=missing)
    cli("status", "-o", "json", expected_refusal=missing, with_bindings=False)
    read_only("missing bindings", live, local)
    (work / "saved-bindings.yaml").rename(bindings)
    history = bundle / "receipts/kagent-v0.10.3"
    saved = bundle / "saved-history"
    history.rename(saved)
    local = filesystem(bundle, home)
    cli("lift", expected_refusal="Kagent lift conflict: primary already exists without a complete matching receipt; no adoption or updates")
    unbound = json.loads(cli("status", "-o", "json"))
    validate_refusal_status(unbound, "failed", True, "unbound: no complete selected-target deployment receipt; live readiness is not a saved deployment")
    read_only("missing receipt", live, local)
    saved.rename(history)
    qualification_phase("operator-drift-conflict")
    kube("-n", NAMESPACE, "patch", "agents.kagent.dev", NAME, "--type=merge",
         "-p", '{"spec":{"description":"operator changed"}}', phase="operator-patch")
    expected += [frame("Agent", "none", "patch")]
    require(audit_frames(audit) == expected, "operator patch mutation audit differs")
    drift, local = snapshot(), filesystem(bundle, home)
    cli("lift", expected_refusal="Kagent receipt-bound reuse conflict: drift (primary ownership, digest or admitted spec differs from desired)")
    drift_status = json.loads(cli("status", "-o", "json"))
    validate_refusal_status(drift_status, "drift", False, "primary ownership, digest or admitted spec differs from desired")
    read_only("operator drift", drift, local)
    artifact = {"kind": "KagentLiftQualification", "runtime": "kagent", "runtime_version": "v0.10.3", "engine": "go",
                "cluster_uid": uid, "context": CONTEXT, "namespace": NAMESPACE, "bundle": NAME,
                "portable_digest": receipt["PortableDigest"], "rendered_digest": receipt["RenderedDigest"],
                "identities": live, "images": images, "execution": execution, "evaluation": evaluation,
                "proofs": {"plan_no_writes": True, "create_only": True, "repeat_read_only": True,
                           "status_identity": True, "conflict_no_mutation": True, "native_a2a_mcp": True,
                           "product_evaluation": True, "repeat_diff_offline": True, "selected_case_marker": True}}
    (work / "qualification.json").write_text(json.dumps(artifact, indent=2) + "\n")
    (work / "qualification.json").chmod(0o600)
    print("native_a2a_counts=tool:1 model:2 terminal:1", flush=True)


def status_ready(status):
    targets = status.get("targets", [])
    return len(targets) == 1 and targets[0].get("state") == "ready" and targets[0].get("ready") is True


def validate_refusal_status(status, state, no_receipt, detail):
    targets = status.get("targets", [])
    require(len(targets) == 1 and targets[0].get("state") == state and targets[0].get("ready") is False
            and bool(targets[0].get("noReceipt")) == no_receipt and targets[0].get("detail") == detail,
            "adjacent status did not report the specific refusal state/detail")


def validate_status(status, identities, cluster_uid):
    require(status_ready(status), "status did not report one ready target")
    require(status.get("bundle") == NAME and status.get("runtime") == "kagent" and status.get("runtimeVersion") == "v0.10.3",
            "status bundle/runtime/version differs")
    target = status["targets"][0]
    require(target.get("context") == CONTEXT and target.get("namespace") == NAMESPACE
            and target.get("clusterUID") == cluster_uid and not target.get("noReceipt"), "status is not receipt-bound to target")
    for field, kind in (("agentUID", "Agent"), ("modelConfigUID", "ModelConfig"), ("deploymentUID", "Deployment")):
        require(target.get(field) == identities[kind + "/" + NAME]["uid"], "status resource identity differs")


def diagnostics():
    # Normalize everything from the API: names, conditions and events are not
    # safe diagnostics. Only fixed namespaces, enums, booleans and counts leave.
    for ns in ("kagent", NAMESPACE):
        try:
            projection = []
            for pod in get("pods", namespace=ns)["items"]:
                state = pod.get("status", {})
                phase = state.get("phase")
                containers = []
                for status in state.get("containerStatuses", []):
                    role = status.get("name")
                    restarts = status.get("restartCount")
                    containers.append({"role": role if role in IMAGES else "unknown",
                        "ready": status.get("ready") is True,
                        "restarts": restarts if type(restarts) is int and restarts >= 0 else 0})
                projection.append({"namespace": ns,
                    "phase": phase if phase in ("Pending", "Running", "Succeeded", "Failed", "Unknown") else "Unknown",
                    "containers": containers})
            print(json.dumps(projection))
        except Exception:
            print("payload-free cluster state unavailable")


def main(argv):
    modes = {"values": 1, "chart": 2, "cluster-absent": 0, "node": 0,
             "provision": 1, "bootstrap": 0, "qualify": 2, "diagnostics": 0}
    require(bool(argv) and argv[0] in modes, "unknown bootstrap mode")
    mode = argv[0]
    require(len(argv) == modes[mode] + 1, "invalid bootstrap arguments")
    if mode == "values":
        pathlib.Path(argv[1]).write_text(json.dumps(values()))
    elif mode == "chart":
        check_chart(pathlib.Path(argv[1]), list(yaml.safe_load_all(pathlib.Path(argv[2]).read_text())))
    elif mode == "cluster-absent":
        cluster_absent()
    elif mode == "node":
        node()
    elif mode == "provision":
        provision(pathlib.Path(argv[1]))
    elif mode == "bootstrap":
        bootstrap()
    elif mode == "qualify":
        qualify(pathlib.Path(argv[1]), pathlib.Path(argv[2]))
    elif mode == "diagnostics":
        diagnostics()


if __name__ == "__main__":
    try:
        main(sys.argv[1:])
    except Exception as error:
        if sys.argv[1:2] == ["qualify"]:
            qualification_failure(error)
        # Network/YAML/tool exception text may contain payloads. Only our own
        # fixed reasons (and a closed generic category) are public.
        detail = str(error) if isinstance(error, QualificationError) else "unclassified-bootstrap-error"
        print("kagent bootstrap failed: " + detail + "; lift/native A2A NOT qualified", file=sys.stderr)
        raise SystemExit(1) from None
