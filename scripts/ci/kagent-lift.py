#!/usr/bin/env python3
"""External pinned bootstrap and independent upstream Go MCP/A2A smoke.

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


PHASES = {"fixture-create", "cluster-inventory", "node-container-inspect", "node-image-inspect", "resource-get",
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
    require(before.get("judge_calls") == 0 and before.get("cases") == [], "fixture was already judged/executed; do not resend")
    response = http_json(endpoint, request)
    observed = http_json(fixture + "/evidence")
    proof = validate_a2a(response, observed)
    rows = observed.get("cases", [])
    require(observed.get("judge_calls") == 0 and len(rows) == 1, "independent smoke fixture inventory differs")
    row = rows[0]
    require(tuple(row.get(key) for key in ("model_calls", "tool_calls", "terminal_answers", "user_turns")) == (2, 1, 1, 1)
            and row.get("input_sha256") == hashlib.sha256(request["params"]["message"]["parts"][0]["text"].encode()).hexdigest()
            and row.get("answer_sha256") == proof["answer_sha256"], "native answer/input/counters differ from recorded fixture case")
    proof.update(terminal_answers=1, judge_calls=0, rpc_id=request["id"],
                 request_message_id=request["params"]["message"]["messageId"])
    return proof


def runtime_fixture_manifest():
    # Checkout CI setup only: upstream objects, not PortableAgent or a KMX
    # compatibility/release engine. The independently provisioned key is a ref.
    deployment = {
        "podSecurityContext": {"runAsNonRoot": True, "seccompProfile": {"type": "RuntimeDefault"}},
        "securityContext": {"allowPrivilegeEscalation": False, "capabilities": {"drop": ["ALL"]},
                            "readOnlyRootFilesystem": True},
        "volumes": [{"name": "tmp", "emptyDir": {}}],
        "volumeMounts": [{"name": "tmp", "mountPath": "/tmp"}],
    }
    return [
        {"apiVersion": "kagent.dev/v1alpha2", "kind": "ModelConfig",
         "metadata": {"name": NAME, "namespace": NAMESPACE},
         "spec": {"provider": "OpenAI", "model": "fixture", "apiKeySecret": "lift-model-key",
                  "apiKeySecretKey": "api-key",
                  "openAI": {"baseUrl": "http://lift-fixture.agents.svc.cluster.local:9000/v1"}}},
        {"apiVersion": "kagent.dev/v1alpha2", "kind": "Agent",
         "metadata": {"name": NAME, "namespace": NAMESPACE},
         "spec": {"description": "Independent upstream Go MCP smoke", "type": "Declarative",
                  "declarative": {"runtime": "go", "modelConfig": NAME,
                      "systemMessage": "Use the allowed tool and return its result.", "deployment": deployment,
                      "tools": [{"type": "McpServer", "mcpServer": {"apiGroup": "kagent.dev",
                          "kind": "RemoteMCPServer", "name": "lift-tools", "toolNames": ["read_sentinel"]}}]}}},
    ]


def runtime_fixture():
    kinds = ("modelconfigs.kagent.dev", "agents.kagent.dev")
    require(all(get(kind, NAME, optional=True) is None for kind in kinds), "independent smoke fixture is occupied")
    for obj in runtime_fixture_manifest():
        kube("-n", NAMESPACE, "create", "-f", "-", data=yaml.safe_dump(obj).encode(), phase="fixture-create")
    # Reuse the bootstrap discovery budget; polling readiness never resends a
    # create or native message. This is fixture startup, not product placement.
    deadline = time.monotonic() + 120
    while time.monotonic() < deadline:
        ready = True
        for kind in kinds:
            obj = get(kind, NAME)
            metadata = obj["metadata"]
            require(metadata.get("uid") and metadata.get("generation", 0) > 0 and not metadata.get("deletionTimestamp"),
                    "runtime fixture identity is missing or terminating")
            conditions = obj.get("status", {}).get("conditions", [])
            expected = ("Accepted", "Ready") if kind == "agents.kagent.dev" else ("Accepted",)
            ready = ready and all(any(c.get("type") == condition and c.get("status") == "True"
                and (c.get("observedGeneration") == metadata["generation"]
                     or kind == "modelconfigs.kagent.dev" and c.get("observedGeneration", 0) == 0)
                for c in conditions) for condition in expected)
        if ready:
            return
        time.sleep(1)
    raise QualificationError("independent runtime fixture never became Accepted/Ready")


def verify_control_endpoint():
    controller = get("deployment", "kagent-controller", "kagent")
    service = get("service", "kagent-controller", "kagent")
    metadata, spec, status = controller["metadata"], controller["spec"], controller.get("status", {})
    require(metadata.get("uid") and not metadata.get("deletionTimestamp") and spec.get("replicas", 1) == 1
            and status.get("observedGeneration") == metadata.get("generation")
            and all(status.get(field) == 1 for field in ("readyReplicas", "updatedReplicas", "availableReplicas")),
            "controller rollout is incomplete")
    containers = spec["template"]["spec"]["containers"]
    require(len(containers) == 1 and containers[0]["name"] == "controller"
            and containers[0]["image"] == "ghcr.io/kagent-dev/kagent/controller:0.10.3@sha256:" + CONTROLLER,
            "controller endpoint workload is not pinned")
    auth = [env for env in containers[0].get("env", []) if env.get("name") == "AUTH_MODE"]
    require(len(auth) == 1 and auth[0].get("value") == "unsecure" and not auth[0].get("valueFrom"),
            "native smoke requires exactly one literal unsecure AUTH_MODE")
    selector = spec["selector"]["matchLabels"]
    require(selector and service["metadata"].get("uid") and not service["metadata"].get("deletionTimestamp")
            and service["spec"].get("selector") == selector
            and all(spec["template"]["metadata"]["labels"].get(key) == value for key, value in selector.items()),
            "control Service selector differs from pinned controller")
    # The pinned chart names the Service port controller; HTTP is the proxy
    # scheme, not this name (the Deployment container port is named http).
    ports = [port for port in service["spec"]["ports"] if port.get("name") == "controller"]
    require(len(ports) == 1 and ports[0].get("port") == 8083 and ports[0].get("targetPort") == 8083
            and ports[0].get("protocol", "TCP") == "TCP", "control Service HTTP endpoint differs")


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


QUALIFICATION_PHASES = {"setup", "runtime-fixture", "native-a2a-one-shot"}
QUALIFICATION_REASONS = {"qualification-check-failed", "unclassified-qualifier-error"}
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
    # The binary argument is retained for the shell's paired suite phase only;
    # this upstream runtime/MCP smoke never invokes any KMX command.
    qualification_phase("setup")
    audit = kubectl_audit(work)
    verify_control_endpoint()
    qualification_phase("runtime-fixture")
    runtime_fixture()
    expected = [{"verb": "create", "dry_run": "none", "resources": [{
        "apiVersion": "kagent.dev/v1alpha2", "kind": kind, "namespace": NAMESPACE, "name": NAME}]}
        for kind in ("ModelConfig", "Agent")]
    require(audit_frames(audit) == expected, "runtime fixture mutation audit differs")
    images = verify_images(include_agent=True)
    live = snapshot()
    require(all(kind + "/" + NAME in live for kind in ("ModelConfig", "Agent")), "runtime primary identity missing")
    uid = get("namespace", "kube-system", namespace=None)["metadata"]["uid"]
    qualification_phase("native-a2a-one-shot")
    with forward("lift-fixture", 9000) as fixture, forward("kagent-controller", 8083, "kagent") as controller:
        verify_control_endpoint()
        execution = one_message(controller + "/api/a2a/agents/" + NAME, fixture)
    validate_read_only("native execution", live, snapshot(), {}, {}, audit_frames(audit), expected)
    require(verify_images(include_agent=True) == images, "native execution changed running image evidence")
    artifact = {"kind": "KagentRuntimeSmokeQualification", "runtime": "kagent", "runtime_version": "v0.10.3",
                "engine": "go", "cluster_uid": uid, "context": CONTEXT, "namespace": NAMESPACE, "agent": NAME,
                "identities": live, "images": images, "execution": execution,
                "fixture_counts": {key: execution[key] for key in ("tool_calls", "model_calls", "terminal_answers", "judge_calls")}}
    path = work / "qualification.json"
    path.write_text(json.dumps(artifact, indent=2) + "\n")
    path.chmod(0o600)
    print("native_a2a_counts=tool:1 model:2 terminal:1", flush=True)


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
        print("kagent bootstrap failed: " + detail + "; runtime MCP/native A2A NOT qualified", file=sys.stderr)
        raise SystemExit(1) from None
