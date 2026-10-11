#!/usr/bin/env python3
"""External pinned kind/bootstrap validation; not execution qualification.

All external output is private. Public failures use closed phases and statuses.
"""
import contextlib
import json
import pathlib
import re
import secrets
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
    "postgresql": ("docker.io/library/postgres", POSTGRES,
                   "3928680cee9028902891672c0a6ce84de58ed3eb1b987588e7c9424d8f08d21d"),
    "fixture": ("docker.io/library/python", FIXTURE,
                "ba27df8b6499cc3756cff167690f3fcb5321faa1703b3a914df5f4fdadb32f05"),
}


class QualificationError(ValueError):
    """A closed, payload-free bootstrap failure safe for CI output."""


def require(condition, message):
    if not condition:
        raise QualificationError(message)


PHASES = {"cluster-inventory", "node-container-inspect", "node-image-inspect", "resource-get",
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
    kube("create", "namespace", NAMESPACE, phase="namespace-create")
    # Dummy authority is created independently. Never put its value in argv.
    secret = work / "dummy-key"
    secret.write_text(secrets.token_hex(24))
    secret.chmod(0o600)
    try:
        kube("-n", NAMESPACE, "create", "secret", "generic", "lift-model-key", "--from-file=api-key=" + str(secret), phase="secret-create")
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


def verify_images():
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
    expected = {"controller", "postgresql", "fixture"}
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
             "provision": 1, "bootstrap": 0, "diagnostics": 0}
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
    elif mode == "diagnostics":
        diagnostics()


if __name__ == "__main__":
    try:
        main(sys.argv[1:])
    except Exception as error:
        # Network/YAML/tool exception text may contain payloads. Only our own
        # fixed reasons (and a closed generic category) are public.
        detail = str(error) if isinstance(error, QualificationError) else "unclassified-bootstrap-error"
        print("kagent bootstrap failed: " + detail + "; lift/native A2A NOT qualified", file=sys.stderr)
        raise SystemExit(1) from None
