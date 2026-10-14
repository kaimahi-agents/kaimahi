#!/usr/bin/env python3
"""Create fresh upstream Orka smoke fixtures; not a suite or bundle deployment."""
import base64
import json
import pathlib
import subprocess
import sys

import yaml

RENDERER = pathlib.Path(__file__).resolve().parents[2] / "bin/orka-native-reference"


def objects(mode):
    if mode not in ("hello", "tool"):
        raise ValueError("unknown runtime fixture")
    name, model, endpoint = ("orka-tool-proof", "tool-fixture", "http://orka-tool-model.orka-system.svc.cluster.local:8080/v1") if mode == "tool" else (
        "orka-hello", "qwen2.5:3b", "http://ollama.ollama.svc.cluster.local:11434/v1")
    reference = {"Name": name, "Namespace": "orka-system", "Model": model,
                 "ProviderType": "openai", "BaseURL": endpoint, "SecretName": "local-provider-key", "SecretKey": "api-key",
                 "Instructions": "Answer the user's request. Use the deployment health Tool when requested." if mode == "tool" else "",
                 "Tools": ["k8s-get-resources"] if mode == "tool" else []}
    # Generate and validate the complete native CRDs before any kubectl call.
    result = subprocess.run([str(RENDERER)], input=json.dumps(reference).encode(),
                            capture_output=True, timeout=60)
    if result.returncode != 0:
        raise ValueError("native runtime fixture rendering refused")
    documents = [yaml.safe_load(base64.b64decode(raw, validate=True)) for raw in json.loads(result.stdout)]
    if [doc["kind"] for doc in documents] != ["Secret", "Provider", "Agent"]:
        raise ValueError("native runtime fixture documents differ")
    _, provider, agent = documents
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
