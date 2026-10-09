#!/usr/bin/env python3
"""Opt-in real AgentKit build -> registry -> cluster -> answer proof.

Requires a prepared cluster, authenticated registry, Docker/BuildKit and a built
kmx. Creates a new dedicated namespace; never reuses or deletes an existing one.
AKS nodes must already be authorized to pull the supplied registry repository.
"""
import argparse
import json
import os
from pathlib import Path
import re
import secrets
import subprocess
import tempfile
import base64

p = argparse.ArgumentParser(description=__doc__)
p.add_argument("--kmx", required=True)
p.add_argument("--context", required=True)
p.add_argument("--registry", required=True, help="registry/repository prefix")
p.add_argument("--namespace", default="kmx-suite-smoke")
p.add_argument("--plain-http", action="store_true")
p.add_argument("--kind-name", help="local kind preload; avoids node-to-host registry routing")
p.add_argument("--confirm-context", required=True)
p.add_argument("--workspace", help="retain source, image association and deployment records for console verification")
p.add_argument("--builder", help="Docker buildx builder with OCI export support")
p.add_argument("--require-attestations", action="store_true")
p.add_argument("--acr-pull-credentials", help="ACR name: provision a namespace-scoped pull Secret using the current Azure login (for local kind)")
a = p.parse_args()
if a.confirm_context != a.context:
    p.error("--confirm-context must equal --context")
if not re.fullmatch(r"[a-z][a-z0-9-]{0,61}[a-z0-9]", a.namespace):
    p.error("invalid namespace")
if a.plain_http and not a.kind_name:
    p.error("plain HTTP is local-kind-only")
repo = Path(__file__).resolve().parents[2]
kmx = str(Path(a.kmx).resolve())
kube = ["kubectl", "--context", a.context, "--request-timeout=30s"]
transport = ["--plain-http"] if a.plain_http else []
build_options = (["--builder", a.builder] if a.builder else []) + (["--require-attestations"] if a.require_attestations else [])


def run(args, data=None):
    result = subprocess.run(args, input=data, text=True, stdout=subprocess.PIPE,
                            stderr=subprocess.PIPE, timeout=600)
    if result.returncode:
        # Native errors can echo request objects; the invocation identifies the
        # failed operation without printing credentials supplied on stdin.
        raise RuntimeError(f"command failed ({result.returncode}): {args[0:5]}")
    return result.stdout.strip()


def create(value):
    return json.loads(run(kube + ["create", "-f", "-", "-o", "json"], json.dumps(value)))


uid = run(kube + ["get", "namespace", "kube-system", "-o", "jsonpath={.metadata.uid}"])
namespace = create({"apiVersion": "v1", "kind": "Namespace", "metadata": {"name": a.namespace}})
print(f"Created test namespace {a.namespace} UID {namespace['metadata']['uid']} on {a.context}", flush=True)
model_token, agent_token = secrets.token_urlsafe(24), secrets.token_urlsafe(24)
for name, token in [("sample-model-key", model_token), ("sample-agent-auth", agent_token)]:
    create({"apiVersion": "v1", "kind": "Secret", "metadata": {"name": name, "namespace": a.namespace},
            "stringData": {"token": token}})
if a.acr_pull_credentials:
    login = json.loads(run(["az", "acr", "login", "--name", a.acr_pull_credentials, "--expose-token", "-o", "json"]))
    if not a.registry.startswith(login["loginServer"] + "/"):
        raise RuntimeError("ACR login does not match the requested registry")
    auth = base64.b64encode(("00000000-0000-0000-0000-000000000000:" + login["accessToken"]).encode()).decode()
    create({"apiVersion": "v1", "kind": "Secret", "metadata": {"name": "registry-pull", "namespace": a.namespace},
            "type": "kubernetes.io/dockerconfigjson",
            "stringData": {".dockerconfigjson": json.dumps({"auths": {login["loginServer"]: {"auth": auth}}})}})
create({"apiVersion": "v1", "kind": "ConfigMap", "metadata": {"name": "sample-model", "namespace": a.namespace},
        "data": {"server.py": (repo / "scripts/ci/suite-model.py").read_text()}})
create({"apiVersion": "apps/v1", "kind": "Deployment", "metadata": {"name": "kmx-lift-model", "namespace": a.namespace},
        "spec": {"replicas": 1, "selector": {"matchLabels": {"app": "kmx-lift-model"}}, "template": {
            "metadata": {"labels": {"app": "kmx-lift-model"}}, "spec": {
                "automountServiceAccountToken": False,
                "containers": [{"name": "model", "image": "docker.io/library/python:3.12-slim",
                                "command": ["python", "/sample/server.py"],
                                "env": [{"name": "MODEL_TOKEN", "valueFrom": {"secretKeyRef": {"name": "sample-model-key", "key": "token"}}},
                                        {"name": "AGENT_TOKEN", "valueFrom": {"secretKeyRef": {"name": "sample-agent-auth", "key": "token"}}}],
                                "volumeMounts": [{"name": "source", "mountPath": "/sample", "readOnly": True}]}],
                "volumes": [{"name": "source", "configMap": {"name": "sample-model"}}]}}}})
run(kube + ["-n", a.namespace, "expose", "deployment", "kmx-lift-model", "--port=8000", "--target-port=8000"])
run(kube + ["-n", a.namespace, "rollout", "status", "deployment/kmx-lift-model", "--timeout=180s"])

with tempfile.TemporaryDirectory(prefix="kmx-suite-smoke-") as work:
    workspace = Path(a.workspace or work)
    workspace.mkdir(parents=True, exist_ok=True)
    suite, archive, envfile = [str(workspace / name) for name in ["hello-world", "agent.oci.tar", a.namespace + "-environment.json"]]
    if not Path(suite).exists():
        run([kmx, "suite", "create", suite, "--instructions",
             "Answer greetings briefly. Identify yourself as the AgentSuite sample."])
    publication = json.loads(run([kmx, "suite", "publish", "hello-world", "--workspace", str(workspace),
                                  "--registry", a.registry] + transport + build_options))
    suite_ref, image_ref = publication["suite"], publication["images"]["hello-world"]
    if a.kind_name:
        run([kmx, "suite", "build", suite, "--agent", "hello-world", "--platform", "linux/amd64",
             "--suite-ref", suite_ref, "--output", archive] + transport + build_options)
        # Assert the newly built manifest matches the published image before preload.
        rebuilt = run([kmx, "suite", "push-image", archive, a.registry.rstrip("/") + "/hello-world:rebuild"] + transport)
        assert rebuilt == image_ref, (rebuilt, image_ref)
        run(["kind", "load", "image-archive", archive, "--name", a.kind_name])
        run(["docker", "exec", a.kind_name + "-control-plane", "ctr", "-n", "k8s.io", "images", "tag",
             "docker.io/library/hello-world:latest", image_ref])
    environment = {
        "apiVersion": "kaimahi.dev/lift/v1alpha1", "name": a.namespace,
        "context": a.context, "clusterUID": uid, "namespace": a.namespace,
        "adapter": "kubernetes-http-v1", "platform": {"os": "linux", "architecture": "amd64"},
        "plainHTTP": a.plain_http,
        "members": {"hello-world": {"name": "hello-world", "image": image_ref,
          "inference": {"model": "sample-model", "endpoint": f"http://kmx-lift-model.{a.namespace}.svc.cluster.local:8000/v1",
            "credential": {"name": "sample-model-key", "key": "token"},
            "capabilities": {"api": "openai-chat-completions-v1", "contextTokens": 8192,
                             "outputTokens": 1024, "streaming": False, "toolCalling": False},
            "evidence": "operator-declared"}, "inputs": {
            "agent-auth": {"secretRef": {"name": "sample-agent-auth", "key": "token"}},
            "listen": {"value": "0.0.0.0"}}}}}
    Path(envfile).write_text(json.dumps(environment))
    if a.acr_pull_credentials:
        environment["imagePullSecrets"] = ["registry-pull"]
        Path(envfile).write_text(json.dumps(environment))
    command = [kmx, "suite", "deploy", "hello-world", "--workspace", str(workspace), "--environment", envfile]
    # Preserve the repository's existing explicit remote-context guard contract.
    os.environ["KAIMAHI_CONFIRM"] = a.context
    plan = json.loads(run(command + ["--plan"]))
    receipt = json.loads(run(command))
    assert receipt["state"] == "ready" and receipt["plan"]["planDigest"] == plan["planDigest"]
    request = '''import os,json,urllib.request
req=urllib.request.Request('http://hello-world/v1/chat/completions',data=json.dumps({'model':'sample-model','messages':[{'role':'user','content':'Hello'}]}).encode(),headers={'Authorization':'Bearer '+os.environ['AGENT_TOKEN'],'Content-Type':'application/json'})
body=json.load(urllib.request.urlopen(req,timeout=30))
assert body['choices'][0]['message']['content']=='Hello from the AgentSuite sample. Model: sample-model',body
print('authenticated agent response verified')'''
    print(run(kube + ["-n", a.namespace, "exec", "-i", "deployment/kmx-lift-model", "--", "python", "-"], request))
    repeated = json.loads(run(command))
    assert repeated["state"] == "ready"
    assert [r["UID"] for r in receipt["members"][0]["resources"]] == [r["UID"] for r in repeated["members"][0]["resources"]]
    environment["members"]["hello-world"]["inference"]["model"] = "sample-model-b"
    Path(envfile).write_text(json.dumps(environment))
    changed = json.loads(run(command))
    assert changed["state"] == "ready" and changed["plan"]["planDigest"] != plan["planDigest"]
    assert [r["UID"] for r in receipt["members"][0]["resources"]] == [r["UID"] for r in changed["members"][0]["resources"]]
    answer = run([kmx, "suite", "run", "hello-world", "--workspace", str(workspace),
                  "--deployment", a.namespace, "--member", "hello-world", "--prompt", "Hello"])
    assert answer == "Hello from the AgentSuite sample. Model: sample-model-b", answer
    current_image = run(kube + ["-n", a.namespace, "get", "deployment", "hello-world", "-o",
                               "jsonpath={.spec.template.spec.containers[0].image}"])
    assert current_image == image_ref
    print(json.dumps({"suite": suite_ref, "image": image_ref, "plan": plan["planDigest"], "context": a.context,
                      "namespace": a.namespace, "repeatPreservedUIDs": True,
                      "modelChangeReusedImage": True, "deploymentConnectionVerified": True,
                      "workspace": str(workspace)}, indent=2))
print("Test resources retained in the dedicated namespace; remove them explicitly when finished.")
