#!/usr/bin/env python3
"""Finite kubectl boundary audit for the native-suite CI scenario; no payload log."""
import hashlib
import json
import os
import pathlib
import re
import subprocess
import sys

SECRET_METADATA = 'go-template={"Kind":{{printf "%q" .kind}},"Metadata":{"Name":{{printf "%q" .metadata.name}},"Namespace":{{printf "%q" .metadata.namespace}},"UID":{{printf "%q" .metadata.uid}},"ResourceVersion":{{printf "%q" .metadata.resourceVersion}}},"Terminating":{{if .metadata.deletionTimestamp}}true{{else}}false{{end}}}'
PRESENCE = re.compile(r'go-template=secret\n\{\{range \$key, \$_ := \.data\}\}\{\{if eq \$key "[-._a-zA-Z0-9]+"\}\}present\{\{end\}\}\{\{end\}\}')
READS = {"namespace", "namespaces", "ns", "providers.core.orka.ai", "agents.core.orka.ai", "tasks.core.orka.ai",
         "secret", "serviceaccount", "serviceaccounts", "sa", "svc", "service", "services", "deployment", "deployments",
         "deploy", "pods", "pod", "crd", "customresourcedefinitions"}
VERBS = {"get", "create", "replace", "config", "version", "port-forward", "rollout"}


def check_scope(args, context):
    contexts = [a.split("=", 1)[1] for a in args if a.startswith("--context=")]
    contexts += [args[i + 1] for i, a in enumerate(args[:-1]) if a == "--context"]
    verb = next((a for a in args if a in VERBS), "")
    if verb not in {"config", "version"} and contexts != [context]:
        raise ValueError("audit-context")
    if any(a.split("=", 1)[0] in {"--server", "--token", "--as", "--as-group", "--username", "--password", "--kubeconfig"} for a in args):
        raise ValueError("audit-override")


def classify(args, raw=b""):
    verb = next((a for a in args if a in VERBS), "")
    if not verb:
        raise ValueError("audit-command")
    tail = args[args.index(verb) + 1:]
    resource, name = (tail + ["", ""])[:2]
    persistent = False
    if verb in {"create", "replace"}:
        if resource == "token" and verb == "create" and name == "orka-result-reader":
            pass
        else:
            if "-f" not in tail or tail[tail.index("-f") + 1] != "-":
                raise ValueError("audit-input")
            doc = json.loads(raw)
            resource, name = doc["kind"], doc["metadata"]["name"]
            if resource not in {"Provider", "Agent", "Task"} or doc["apiVersion"] != "core.orka.ai/v1alpha1":
                raise ValueError("audit-resource")
            if doc["metadata"].get("namespace") != "orka-system":
                raise ValueError("audit-namespace")
            persistent = not any(a in {"--dry-run=server", "--dry-run=client"} for a in tail)
            if "--dry-run" in tail or any(a.startswith("--dry-run=") and a not in {"--dry-run=server", "--dry-run=client"} for a in tail):
                raise ValueError("audit-dry-run")
    elif verb == "get":
        if resource not in READS:
            raise ValueError("audit-read")
        if resource == "secret":
            output = tail[tail.index("-o") + 1] if "-o" in tail else ""
            if output != SECRET_METADATA and not PRESENCE.fullmatch(output):
                raise ValueError("audit-secret")
    elif verb == "rollout":
        if tail != ["status", "deploy/orka-api-controller", "--timeout=10s"] or "-n" not in args or args[args.index("-n") + 1] != "orka-system":
            raise ValueError("audit-rollout")
        resource, name = "deployment", "orka-api-controller"
    elif verb == "port-forward":
        resource = next((a for a in tail if a.startswith("svc/")), "")
        if not resource.startswith("svc/") or "--address" not in args or args[args.index("--address") + 1] != "127.0.0.1":
            raise ValueError("audit-forward")
        name, resource = resource.split("/", 1)[1], "service"
    elif verb == "config":
        if resource not in {"view", "current-context", "get-contexts"}:
            raise ValueError("audit-config")
        name = ""
    else:
        resource, name = "client", ""
    if name.startswith("-"):
        name = ""  # list reads have no named object
    if name and not re.fullmatch(r"[a-zA-Z0-9._-]{1,253}", name):
        raise ValueError("audit-name")
    return {"verb": verb, "resource": resource, "name": name, "persistent": persistent,
            "stdinDigest": hashlib.sha256(raw).hexdigest(), "stdinBytes": len(raw)}


def verify(frames, member, rows):
    if any(f["verb"] == "blocked" or f.get("returnCode") != 0 for f in frames if f["verb"] != "port-forward"):
        raise ValueError("audit-blocked")
    creates = [(f["resource"], f["name"]) for f in frames if f["persistent"]]
    wanted = [("Provider", member), ("Agent", member)] + [("Task", r["taskName"]) for r in rows]
    if sorted(creates) != sorted(wanted):
        raise ValueError("audit-creates")
    if not any(f["verb"] == "create" and f["resource"] == "token" for f in frames):
        raise ValueError("audit-token")
    for row in rows:
        if not any(f["verb"] == "get" and f["resource"] == "tasks.core.orka.ai" and f["name"] == row["taskName"] for f in frames):
            raise ValueError("audit-task-get")


def append(frame):
    path = pathlib.Path(os.environ["KMX_NATIVE_AUDIT"])
    fd = os.open(path, os.O_WRONLY | os.O_APPEND | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, "ab") as stream:
        stream.write(json.dumps(frame, sort_keys=True).encode() + b"\n")


def main():
    try:
        if pathlib.Path(sys.argv[0]).name != "kubectl":
            raise ValueError("audit-builder")
        args = sys.argv[1:]
        check_scope(args, os.environ["KMX_NATIVE_CONTEXT"])
        raw = sys.stdin.buffer.read() if "-f" in args else b""
        frame = classify(args, raw)
        real = os.environ["KMX_NATIVE_KUBECTL"]
        if frame["verb"] == "port-forward":
            append(frame)  # intent only; the existing client proves owned bind
            os.execv(real, [real, *args])
        code = subprocess.run([real, *args], input=raw if "-f" in args else None, check=False).returncode
        append({**frame, "returnCode": code})
        return code
    except Exception:
        append({"verb": "blocked", "resource": "boundary", "name": "", "persistent": False})
        print("native audit: boundary-refused", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
