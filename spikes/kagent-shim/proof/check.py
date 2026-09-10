#!/usr/bin/env python3
"""SPIKE preflight, not admission: direct kubectl callers can bypass this check.

Usage: python3 proof/check.py installed-crds.json converted.yaml proof/task.yaml
Fetch installed CRDs with an explicitly pinned kubectl --context first.
"""
import hashlib
import json
from pathlib import Path
import sys

import yaml


class StrictLoader(yaml.SafeLoader):
    def construct_mapping(self, node, deep=False):
        result = {}
        for key_node, value_node in node.value:
            key = self.construct_object(key_node, deep=deep)
            if not isinstance(key, str) or key in result:
                raise ValueError("non-string or duplicate YAML key")
            result[key] = self.construct_object(value_node, deep=deep)
        return result

    def compose_node(self, parent, index):
        if self.check_event(yaml.AliasEvent):
            raise ValueError("YAML aliases are outside the spike contract")
        return super().compose_node(parent, index)


def only(obj, keys, path):
    if not isinstance(obj, dict):
        raise ValueError(f"{path}: expected object")
    for key in obj:
        if key not in keys:
            raise ValueError(f"{path}.{key}: outside native Task spike contract")


def check_task(task, agent):
    only(task, {"apiVersion", "kind", "metadata", "spec"}, "Task")
    if task.get("apiVersion") != "core.orka.ai/v1alpha1" or task.get("kind") != "Task":
        raise ValueError("expected core.orka.ai/v1alpha1 Task")
    meta = task.get("metadata")
    only(meta, {"name", "namespace"}, "Task.metadata")
    if not isinstance(meta.get("name"), str) or not meta["name"]:
        raise ValueError("Task.metadata.name required")
    if meta.get("namespace") != agent["metadata"]["namespace"]:
        raise ValueError("Task.metadata.namespace: must match generated Agent")
    spec = task.get("spec")
    only(spec, {"type", "agentRef", "prompt", "timeout", "retryPolicy"}, "Task.spec")
    if spec.get("type") != "ai":
        raise ValueError("Task.spec.type: only ai is supported")
    ref = spec.get("agentRef")
    only(ref, {"name", "namespace"}, "Task.spec.agentRef")
    if ref.get("name") != agent["metadata"]["name"] or ref.get("namespace", meta["namespace"]) != meta["namespace"]:
        raise ValueError("Task.spec.agentRef: must reference generated same-namespace Agent")
    if not isinstance(spec.get("prompt"), str) or not spec["prompt"].strip():
        raise ValueError("Task.spec.prompt: nonempty invocation required")
    if "timeout" in spec and (not isinstance(spec["timeout"], str) or not spec["timeout"]):
        raise ValueError("Task.spec.timeout: duration required (server dry-run validates syntax)")
    if "retryPolicy" in spec:
        only(spec["retryPolicy"], {"maxRetries"}, "Task.spec.retryPolicy")
        retries = spec["retryPolicy"].get("maxRetries")
        if type(retries) is not int or retries != 0:
            raise ValueError("Task.spec.retryPolicy.maxRetries: must be zero")


def check_crds(installed, pins):
    by_name = {item["metadata"]["name"]: item for item in installed["items"]}
    for name, expected in pins.items():
        if name not in by_name:
            raise ValueError(f"missing CRD {name}")
        versions = [v for v in by_name[name]["spec"]["versions"] if v["name"] == "v1alpha1" and v.get("served")]
        if len(versions) != 1:
            raise ValueError(f"{name}: served v1alpha1 required")
        digest = hashlib.sha256(json.dumps(versions[0]["schema"], sort_keys=True, separators=(",", ":")).encode()).hexdigest()
        if digest != expected:
            raise ValueError(f"{name}: installed schema mismatch; target is Orka v0.1.3")


def main():
    if len(sys.argv) != 4:
        raise ValueError(__doc__.strip())
    installed = json.loads(Path(sys.argv[1]).read_text())
    pins = json.loads(Path(__file__).with_name("pins.json").read_text())
    check_crds(installed, pins["schemas"])
    output = list(yaml.load_all(Path(sys.argv[2]).read_text(), Loader=StrictLoader))
    agents = [d for d in output if d.get("kind") == "Agent"]
    if len(agents) != 1:
        raise ValueError("exactly one generated Agent required")
    agent = agents[0]
    ref = agent["spec"].get("providerRef", {}).get("name")
    providers = [d for d in output if d.get("kind") == "Provider" and d["metadata"]["name"] == ref and d["metadata"]["namespace"] == agent["metadata"]["namespace"]]
    if len(providers) != 1:
        raise ValueError("generated Agent must have its Provider in the bundle")
    task = yaml.load(Path(sys.argv[3]).read_text(), Loader=StrictLoader)
    check_task(task, agent)
    print("PASS: installed Orka v0.1.3 schemas, Provider bundle, native Task authority preflight")


if __name__ == "__main__":
    try:
        main()
    except (ValueError, KeyError, TypeError, OSError, yaml.YAMLError) as exc:
        sys.exit(f"refused: {exc}")
