#!/usr/bin/env python3
"""Create fresh upstream Orka smoke fixtures; not a suite or bundle deployment."""
import json
import subprocess
import sys


def objects(mode):
    if mode not in ("hello", "tool"):
        raise ValueError("unknown runtime fixture")
    name, model, endpoint = ("orka-tool-proof", "tool-fixture", "http://orka-tool-model.orka-system.svc.cluster.local:8080/v1") if mode == "tool" else (
        "orka-hello", "qwen2.5:3b", "http://ollama.ollama.svc.cluster.local:11434/v1")
    metadata = {"name": name, "namespace": "orka-system"}
    provider = {"apiVersion": "core.orka.ai/v1alpha1", "kind": "Provider", "metadata": dict(metadata),
                "spec": {"type": "openai", "defaultModel": model, "baseURL": endpoint,
                         "secretRef": {"name": "local-provider-key", "key": "api-key"}}}
    agent = {"apiVersion": "core.orka.ai/v1alpha1", "kind": "Agent", "metadata": dict(metadata),
             "spec": {"model": model, "providerRef": {"name": name},
                      "instructions": "Answer the user's request. Use the deployment health Tool when requested."}}
    if mode == "tool":
        agent["spec"]["tools"] = [{"name": "k8s-get-resources", "enabled": True}]
    return provider, agent


def prepare(mode, kube):
    pair = objects(mode)
    for doc in pair:
        if kube("-n", "orka-system", "get", doc["kind"].lower() + "s.core.orka.ai", doc["metadata"]["name"],
                "--ignore-not-found", "-o", "name").strip():
            raise ValueError("runtime fixture already exists")
    for doc in pair:
        kube("create", "-f", "-", raw=json.dumps(doc).encode())
        kube("-n", "orka-system", "wait", "--for=jsonpath={.status.ready}=true", "--timeout=180s",
             doc["kind"].lower() + "s.core.orka.ai/" + doc["metadata"]["name"])


def main():
    if len(sys.argv) != 3:
        raise ValueError("expected context and fixture mode")
    context, mode = sys.argv[1:]
    def kube(*args, raw=None):
        result = subprocess.run(["kubectl", "--context", context, *args], input=raw, capture_output=True, timeout=185)
        if result.returncode != 0:
            raise ValueError("runtime fixture operation failed; inspect owned cluster")
        return result.stdout
    prepare(mode, kube)


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError, subprocess.SubprocessError):
        print("Orka runtime fixture refused or incomplete; no retry or cleanup", file=sys.stderr)
        sys.exit(1)
