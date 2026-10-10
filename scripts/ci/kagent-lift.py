#!/usr/bin/env python3
"""Externally provisioned Go lift qualification; no installed KMX execution verb.

All subprocess output is captured, never replayed. Resource projections and
execution artifacts contain identities, counts and hashes, not secret values,
instructions, prompts or terminal answers. Native message/send is one-shot.
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
NAME = "lift-proof"
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
    """A closed, payload-free qualification failure safe for CI output."""


def require(condition, message):
    if not condition:
        raise QualificationError(message)


def digest(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode()).hexdigest()


def run(argv, *, data=None, timeout=120, expected=0):
    result = subprocess.run(argv, input=data, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=timeout)
    require(result.returncode == expected, "external command failed: " + pathlib.Path(argv[0]).name)
    return result.stdout


def kube(*args, data=None):
    return run(["kubectl", "--context", CONTEXT, *args], data=data)


def get(kind, name=None, namespace=NAMESPACE, optional=False):
    args = ["get", kind]
    if name:
        args.append(name)
    if namespace:
        args += ["-n", namespace]
    if optional:
        args.append("--ignore-not-found=true")
    raw = kube(*args, "-o", "json")
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
    clusters = run(["kind", "get", "clusters"]).decode().splitlines()
    require("kmx-kagent-lift" not in clusters, "refusing to reuse/delete a preexisting dedicated cluster")


def node():
    container = json.loads(run(["docker", "inspect", "kmx-kagent-lift-control-plane"]))[0]
    image = json.loads(run(["docker", "image", "inspect", NODE_IMAGE]))[0]
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
    kube("create", "namespace", NAMESPACE)
    # Dummy authority is created independently. Never put its value in argv.
    secret = work / "dummy-key"
    secret.write_text(secrets.token_hex(24))
    secret.chmod(0o600)
    try:
        kube("-n", NAMESPACE, "create", "secret", "generic", "lift-model-key", "--from-file=api-key=" + str(secret))
    finally:
        secret.unlink()
    kube("-n", NAMESPACE, "apply", "-f", "-", data=yaml.safe_dump_all(fixture_manifest()).encode())
    kube("-n", NAMESPACE, "rollout", "status", "deployment/lift-fixture", "--timeout=180s")
    deadline = time.monotonic() + 120
    while time.monotonic() < deadline:
        remote = get("remotemcpservers.kagent.dev", "lift-tools")
        if any(c.get("type") == "Accepted" and c.get("status") == "True" for c in remote.get("status", {}).get("conditions", [])) \
                and [t["name"] for t in remote.get("status", {}).get("discoveredTools", [])] == ["read_sentinel"]:
            return
        time.sleep(1)
    raise ValueError("RemoteMCPServer never accepted/discovered the sole allowed tool")


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
            require(process.poll() is None, "owned forward exited during qualification")
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
    # No retry handler: an ambiguous execution is not permission to resend.
    with urllib.request.urlopen(request, timeout=180) as response:
        raw = response.read(2 * 1024 * 1024 + 1)
        require(len(raw) <= 2 * 1024 * 1024, "bounded native response required")
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
    raw = kube("get", "--raw", "/api/v1/namespaces/kagent/services/http:kagent-controller:8083/proxy/version")
    version = json.loads(raw)
    require(version.get("kagent_version") == "0.10.3" and version.get("git_commit") in {
        "8878c39", "8878c39ac64421cb665c8e62ecc6411dd12f2fc2"}, "controller build differs from exact release")
    with forward("lift-fixture", 9000) as url:
        require(http_json(url + "/healthz") == {"ready": True}, "fixture health failed")
        reply = http_json(url + "/mcp", {"jsonrpc": "2.0", "id": 1, "method": "initialize",
                         "params": {"protocolVersion": "2025-03-26", "capabilities": {},
                                    "clientInfo": {"name": "kmx-qualification", "version": "1"}}})
        require(reply.get("result", {}).get("protocolVersion") == "2025-03-26", "MCP initialization failed")
        tools = http_json(url + "/mcp", {"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": {}})
        require([t["name"] for t in tools["result"]["tools"]] == ["read_sentinel"], "MCP discovery not allowlisted")
        require(http_json(url + "/evidence")["tool_calls"] == 0, "bootstrap invoked the actual proof tool")
    print("pinned node/charts/controller/Postgres/fixture images and MCP discovery verified")
    print("verified_imageIDs=" + json.dumps(images, sort_keys=True))


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
        # RemoteMCPServer has controller-updated status; its resourceVersion
        # churn is not mutation proof. Spec/ownership/UID/generation still bind it.
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
        ("configmap", "kagent-controller", "kagent"), ("deployment", "kagent-postgresql", "kagent")):
        obj = get(kind, name, namespace, optional=True)
        if obj:
            objects.append(obj)
    return project_identity(objects)


def validate_read_only(phase, before, after, local_before, local_after, writes, expected_writes):
    if before != after:
        # Keys come from the fixed snapshot inventory; values are deliberately
        # omitted, including any private paths or upstream condition text.
        changes = [{"resource": key, "fields": sorted(field for field in set(before.get(key, {})) | set(after.get(key, {}))
                    if before.get(key, {}).get(field) != after.get(key, {}).get(field))}
                   for key in sorted(set(before) | set(after)) if before.get(key) != after.get(key)]
        print("evidence_changes=" + json.dumps(changes, sort_keys=True), flush=True)
    require(before == after, phase + " changed cluster evidence")
    require(local_before == local_after, phase + " changed local evidence")
    require(writes == expected_writes, phase + " changed mutation audit")


def filesystem(*roots):
    # Include directory identities: --plan/status must not create local state.
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
        raise ValueError("native A2A returned no terminal task/message; do not resend")
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
    # Deliberately external: status must use these bindings again, not infer live values.
    path = work / "external-bindings.yaml"
    path.write_text(yaml.safe_dump(bindings, sort_keys=False))
    return bundle, path


def kubectl_audit(work):
    wrapper = work / "fail-bin/kubectl"
    wrapper.unlink()
    real = str(work / "bin/kubectl")
    audit = work / "kubectl-mutations.jsonl"
    source = '#!/usr/bin/env python3\nimport json, subprocess, sys\n'
    source += 'writes = {"create", "apply", "replace", "patch", "delete", "edit", "label", "annotate", "scale", "rollout"}\n'
    source += 'commands = writes | {"get", "describe", "auth", "version", "api-resources", "api-versions", "config", "port-forward", "logs", "wait"}\n'
    source += 'verb = next((a for a in sys.argv[1:] if a in commands), "")\n'
    source += 'if verb in writes:\n    with open(' + repr(str(audit)) + ', "a") as f:\n        f.write(json.dumps({"verb": verb, "dryrun": any(a.startswith("--dry-run") for a in sys.argv[1:])}) + "\\n")\n'
    source += 'raise SystemExit(subprocess.call([' + repr(real) + ', *sys.argv[1:]]))\n'
    wrapper.write_text(source)
    wrapper.chmod(0o700)
    return audit


def mutation_count(audit):
    return sum(not json.loads(line)["dryrun"] for line in audit.read_text().splitlines()) if audit.exists() else 0


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


def cli_failure_reason(raw):
    # Match only known source messages to closed codes. Never replay stderr;
    # even a known failure can be surrounded by unsafe upstream payloads.
    patterns = (
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
        (b"ModelConfig", "modelconfig-unqualified"),
        (b"Agent", "agent-unqualified"),
    )
    return next((code for pattern, code in patterns if pattern in raw), "unclassified-cli-error")


def qualify(binary, work):
    require(binary.is_file(), "build matching bin/kmx after Go writes settle")
    bundle, bindings = bundle_files(work)
    audit = kubectl_audit(work)
    home = pathlib.Path(os.environ["KMX_HOME"])
    flags = ["--runtime", "kagent", "--runtime-version", "v0.10.3", "--to-context", CONTEXT, "--bindings", str(bindings)]

    def cli(verb, *extra, success=True, with_bindings=True):
        selected = flags if with_bindings else flags[:-2]
        result = subprocess.run([str(binary), "agent", verb, str(bundle), *selected, *extra],
                                stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=300)
        require((result.returncode == 0) == success,
                "KMX " + verb + " unexpected exit=" + str(result.returncode) + " reason=" + cli_failure_reason(result.stderr))
        return result.stdout

    before, local = snapshot(), filesystem(bundle, home)
    print("qualification_phase=absent-status", flush=True)
    absent = json.loads(cli("status", "-o", "json"))
    if not (len(absent.get("targets", [])) == 1 and absent["targets"][0].get("state") == "absent"):
        state = absent.get("targets", [{}])[0].get("state", "unknown")
        safe_state = state if state in {"absent", "ready", "unreadable", "failed", "drift"} else "unknown"
        print("unexpected_status_state=" + safe_state, flush=True)
        # A read-only plan exposes a more specific refusal than status's
        # deliberately generic qualification detail. Still no actual create.
        cli("lift", "--plan")
    require(len(absent.get("targets", [])) == 1 and absent["targets"][0]["state"] == "absent"
            and absent["targets"][0].get("noReceipt") is True, "new target must report absent without a receipt")
    print("qualification_phase=plan", flush=True)
    cli("lift", "--plan")
    validate_read_only("plan", before, snapshot(), local, filesystem(bundle, home), mutation_count(audit), 0)
    print("qualification_phase=new-lift", flush=True)
    cli("lift")
    require(mutation_count(audit) == 2, "new lift must create only ModelConfig and Agent")
    images = verify_images(include_agent=True)
    live = snapshot()
    uid = get("namespace", "kube-system", namespace=None)["metadata"]["uid"]
    receipt = validate_receipt(bundle, live, uid)
    local = filesystem(bundle, home)
    print("qualification_phase=repeat-lift", flush=True)
    cli("lift")
    validate_read_only("repeat lift", live, snapshot(), local, filesystem(bundle, home), mutation_count(audit), 2)
    print("qualification_phase=ready-status", flush=True)
    status = json.loads(cli("status", "-o", "json"))
    validate_status(status, live, uid)
    require(status["portableDigest"] == receipt["PortableDigest"] and status["renderedDigest"] == receipt["RenderedDigest"],
            "status digests differ from saved deployment")
    validate_read_only("status", live, snapshot(), local, filesystem(bundle, home), mutation_count(audit), 2)
    # Independently native: no KMX execution command, no hosted account, no resend.
    print("qualification_phase=native-a2a-one-shot", flush=True)
    with forward("lift-fixture", 9000) as fixture, forward("kagent-controller", 8083, "kagent") as controller:
        execution = one_message(controller + "/api/a2a/agents/" + NAME, fixture)
    require(snapshot() == live and filesystem(bundle, home) == local, "native execution changed deployment/dependency identity")
    # Missing/default/external changed bindings must not silently use live fields.
    print("qualification_phase=bindings-and-receipt-conflicts", flush=True)
    original = bindings.read_bytes()
    changed = yaml.safe_load(original)
    changed["modelConfig"]["baseURL"] = "http://unused.invalid/v1"
    bindings.write_text(yaml.safe_dump(changed))
    cli("lift", success=False)
    changed_status = json.loads(cli("status", "-o", "json"))
    require(changed_status["targets"][0]["state"] == "drift", "status ignored changed desired bindings")
    bindings.write_bytes(original)
    bindings.rename(work / "saved-bindings.yaml")
    cli("status", "-o", "json", success=False)
    cli("status", "-o", "json", success=False, with_bindings=False)
    (work / "saved-bindings.yaml").rename(bindings)
    # Same objects without a saved receipt cannot authorize reuse.
    history = bundle / "receipts/kagent-v0.10.3"
    saved = bundle / "saved-history"
    history.rename(saved)
    cli("lift", success=False)
    unbound = json.loads(cli("status", "-o", "json"))
    require(not status_ready(unbound) and unbound["targets"][0].get("noReceipt") is True,
            "status without receipt certified saved deployment")
    saved.rename(history)
    require(snapshot() == live and mutation_count(audit) == 2, "bindings/receipt conflicts mutated deployment")
    # Explicit spec drift belongs to the operator; no reconciliation/overwrite.
    print("qualification_phase=operator-drift-conflict", flush=True)
    kube("-n", NAMESPACE, "patch", "agents.kagent.dev", NAME, "--type=merge",
         "-p", '{"spec":{"description":"operator changed"}}')
    drift = snapshot()
    cli("lift", success=False)
    drift_status = json.loads(cli("status", "-o", "json"))
    require(drift_status["targets"][0]["state"] == "drift" and snapshot() == drift and mutation_count(audit) == 3,
            "drift/conflict was overwritten or status certified it")
    artifact = {"kind": "KagentLiftQualification", "runtime": "kagent", "runtime_version": "v0.10.3", "engine": "go",
                "cluster_uid": uid, "context": CONTEXT, "namespace": NAMESPACE, "bundle": NAME,
                "portable_digest": receipt["PortableDigest"], "rendered_digest": receipt["RenderedDigest"],
                "identities": live, "images": images, "execution": execution,
                "proofs": {"plan_no_writes": True, "create_only": True, "repeat_read_only": True,
                           "status_identity": True, "conflict_no_mutation": True, "native_a2a_mcp": True}}
    (work / "qualification.json").write_text(json.dumps(artifact, indent=2) + "\n")
    (work / "qualification.json").chmod(0o600)


def status_ready(status):
    targets = status.get("targets", [])
    return len(targets) == 1 and targets[0].get("state") == "ready" and targets[0].get("ready") is True


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
    # Only a closed state projection. Never replay raw controller/model logs,
    # conditions, events, kubectl stderr, provider data or answer fragments.
    for ns in ("kagent", NAMESPACE):
        try:
            pods = get("pods", namespace=ns)["items"]
            projection = [{"namespace": ns, "name": p["metadata"]["name"], "phase": p.get("status", {}).get("phase"),
                           "containers": [{"name": s["name"], "ready": s.get("ready"), "restarts": s.get("restartCount")}
                                          for s in p.get("status", {}).get("containerStatuses", [])]} for p in pods]
            print(json.dumps(projection))
            for pod in pods:
                for container in pod["spec"]["containers"]:
                    if container["name"] not in IMAGES:
                        continue
                    repository, index, platform = IMAGES[container["name"]]
                    statuses = [s for s in pod.get("status", {}).get("containerStatuses", []) if s["name"] == container["name"]]
                    for status in statuses:
                        # Image refs are public artifact identities, but still
                        # only print strings in this repository's closed pin set.
                        accepted = {repository + "@sha256:" + pin for pin in (index, platform)} | {
                            repository + ":0.10.3@sha256:" + pin for pin in (index, platform)}
                        detail = {"container": container["name"], "spec_image_sha256": hashlib.sha256(container["image"].encode()).hexdigest(),
                                  "status_image_sha256": hashlib.sha256(status.get("image", "").encode()).hexdigest(),
                                  "spec_equals_status_image": container["image"] == status.get("image"),
                                  "spec_image": container["image"] if container["image"] in accepted else "not-closed-pin-spelling",
                                  "status_image": status.get("image") if status.get("image") in accepted else "not-closed-pin-spelling"}
                        print(json.dumps(detail))
        except (ValueError, KeyError, subprocess.SubprocessError):
            print("payload-free cluster state unavailable")


def main(argv):
    mode = argv[0]
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
    else:
        raise ValueError("unknown qualification mode")


if __name__ == "__main__":
    try:
        main(sys.argv[1:])
    except Exception as error:
        # Exception text from network/YAML/upstream tools may contain payloads.
        # Print only the closed error class; preserve no unsafe raw transcript.
        detail = str(error) if isinstance(error, QualificationError) else type(error).__name__
        print("kagent qualification failed: " + detail + "; do not infer success or resend", file=sys.stderr)
        raise SystemExit(1) from None
