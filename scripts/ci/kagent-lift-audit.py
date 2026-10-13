#!/usr/bin/env python3
"""Classify only the kubectl forms used by the isolated lift proof.

Not a general kubectl parser: unsupported forms refuse before forwarding and
leave a closed rejection frame, even if a caller swallows the refusal.
"""
import json
import os
import re
import subprocess
import sys

import yaml

LIMIT = 1024 * 1024
OWNED_FORWARDS = [
    ["--context", "kind-kmx-kagent-lift", "-n", "agents", "port-forward",
     "--address", "127.0.0.1", "service/lift-fixture", ":9000"],
    ["--context", "kind-kmx-kagent-lift", "-n", "kagent", "port-forward",
     "--address", "127.0.0.1", "service/kagent-controller", ":8083"],
]


def require(condition):
    if not condition:
        raise ValueError("unsupported kubectl invocation")


def classify(argv, data):
    require(len(argv) <= 80 and all(isinstance(arg, str) and len(arg) <= LIMIT for arg in argv))
    args = list(argv)
    if args in OWNED_FORWARDS:
        return None
    namespace = ""
    # Consume actual leading options; their values are never verb candidates.
    leading = {"--context", "-n", "--namespace", "--request-timeout", "--cache-dir", "--kubeconfig"}
    while args and args[0].startswith("-"):
        option, equal, value = args.pop(0).partition("=")
        require(option in leading)
        if not equal:
            require(bool(args) and not args[0].startswith("-"))
            value = args.pop(0)
        require(bool(value))
        if option in {"-n", "--namespace"}:
            namespace = value
    require(bool(args))
    verb = args.pop(0)
    subcommand = None
    if verb in {"auth", "config", "rollout"}:
        require(bool(args))
        subcommand = args.pop(0)
        require((verb, subcommand) in {("auth", "can-i"), ("config", "view"), ("rollout", "status")})
    require(verb in {"get", "describe", "version", "api-resources", "api-versions", "auth", "config", "rollout", "create", "patch"})
    # Each accepted command has a bounded option vocabulary. No unknown flag
    # (especially a dry-run lookalike) can earn read-only credit.
    valued = {"get": {"-o", "--output", "-l", "--selector", "--raw"},
              "describe": {"-l", "--selector"}, "version": {"-o", "--output"},
              "api-resources": {"-o", "--output", "--api-group"}, "api-versions": set(),
              "auth": {"--as", "--as-group", "--subresource"},
              "config": {"-o", "--output"}, "rollout": {"--timeout"},
              "create": {"-f", "--filename", "-o", "--output", "--validate", "--raw"},
              "patch": {"-p", "--patch", "--type", "-o", "--output"}}[verb] | leading
    booleans = {"get": {"--all-namespaces", "-A", "--ignore-not-found"},
                "version": {"--client"}, "config": {"--minify", "--flatten"},
                "api-resources": {"--namespaced"}}.get(verb, set())
    positionals, options = [], {}
    strategy = "none"
    dry_run_seen = False
    while args:
        arg = args.pop(0)
        if not arg.startswith("-"):
            positionals.append(arg)
            continue
        option, equal, value = arg.partition("=")
        if option == "--dry-run":
            require(verb in {"create", "patch"})
            dry_run_seen = True
            # Pinned kubectl uses NoOptDefVal=client: a separated strategy is
            # positional, NOT consumed. Position validation below rejects it.
            strategy = value if equal else "client"
            require(strategy in {"none", "client", "server"})
            continue
        if option in booleans:
            require(not equal or value in {"true", "false"})
            continue
        require(option in valued)
        if not equal:
            require(bool(args) and (not args[0].startswith("-") or args[0] == "-"))
            value = args.pop(0)
        require(bool(value))
        # kubectl accumulates filename slices, unlike scalar dry-run flags.
        if option in {"-f", "--filename"}:
            require("-f" not in options and "--filename" not in options)
        options[option] = value
        if option in {"-n", "--namespace"}:
            namespace = value
    if verb not in {"create", "patch"}:
        if verb == "auth":
            require(len(positionals) == 2)
        elif verb in {"config", "version", "api-resources", "api-versions"}:
            require(not positionals)
        elif verb == "rollout":
            require(len(positionals) == 1)
        else:
            require(1 <= len(positionals) <= 3 or (not positionals and "--raw" in options))
        return None
    if verb == "create" and "--raw" in options:
        # A2A Service-proxy POST is native execution, not object creation.
        # No other raw endpoint earns exemption from the mutation audit.
        require(options["--raw"] in {
            "/api/v1/namespaces/kagent/services/http:kagent-controller:8083/proxy/api/a2a/agents/lift-proof",
            "/api/v1/namespaces/kagent/services/http:kagent-controller:8083/proxy/api/a2a/agents/suite-eval-proof"})
        require(not positionals and not dry_run_seen and options.get("-f", options.get("--filename")) == "-"
                and set(options) <= leading | {"--raw", "-f", "--filename"} and 0 < len(data) <= LIMIT)
        return None
    if verb == "patch":
        require(positionals == ["agents.kagent.dev", "lift-proof"] and namespace == "agents")
        require(options.get("--type") == "merge" and bool(options.get("-p", options.get("--patch"))))
        metadata = {"apiVersion": "kagent.dev/v1alpha2", "kind": "Agent", "namespace": namespace, "name": "lift-proof"}
    else:
        require(not positionals and options.get("-f", options.get("--filename")) == "-" and 0 < len(data) <= LIMIT)
        try:
            obj = yaml.safe_load(data)
            require(isinstance(obj, dict) and isinstance(obj.get("metadata"), dict))
            source = obj["metadata"]
            metadata = {"apiVersion": obj.get("apiVersion"), "kind": obj.get("kind"),
                        "namespace": source.get("namespace", namespace), "name": source.get("name")}
            require(metadata["apiVersion"] == "kagent.dev/v1alpha2" and metadata["kind"] in {"Agent", "ModelConfig"})
            require(all(isinstance(value, str) and re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9./-]{0,252}", value) for value in metadata.values()))
            require(not namespace or namespace == metadata["namespace"])
        except (yaml.YAMLError, TypeError, KeyError, RecursionError):
            raise ValueError("unsupported kubectl invocation") from None
    return {"verb": verb, "dry_run": strategy, "resources": [metadata]}


def main(real, audit, argv):
    # Read stdin only for the bounded stdin-create form; read commands retain
    # their stdin. The original bytes, not a YAML reserialization, are forwarded.
    data = sys.stdin.buffer.read(LIMIT + 1) if "-" in argv else b""
    try:
        frame = classify(argv, data)
    except ValueError:
        with open(audit, "a") as out:
            out.write(json.dumps({"rejected": "unsupported-kubectl-invocation"}) + "\n")
        print("unsupported kubectl invocation", file=sys.stderr)
        return 1
    if frame is not None:
        with open(audit, "a") as out:
            out.write(json.dumps(frame) + "\n")
    if list(argv) in OWNED_FORWARDS:
        # The driver's Popen must own the real forward, not a wrapper child.
        os.execv(real, [real, *argv])
    return subprocess.call([real, *argv], stdin=None) if not data else subprocess.run([real, *argv], input=data).returncode


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1], sys.argv[2], sys.argv[3:]))
