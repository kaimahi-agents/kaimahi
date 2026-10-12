#!/usr/bin/env python3
"""Bounded, payload-free pod/event snapshots for CI's first-answer quickstart."""
from __future__ import annotations

import argparse
import json
import os
import subprocess
import time
from pathlib import Path

NAMESPACES = ("kube-system", "ollama", "orka-system")
SUBPROCESS_TIMEOUT = 8
TOTAL_BUDGET = 50


def scalars(data, strings=(), integers=(), booleans=()):
    """Project named fields with exact scalar types, never arbitrary nested data."""
    if not isinstance(data, dict):
        return {}
    result = {}
    for fields, expected in ((strings, str), (integers, int), (booleans, bool)):
        for field in fields:
            if type(data.get(field)) is expected:
                result[field] = data[field]
    return result


def objects(data):
    return [item for item in data if isinstance(item, dict)] if isinstance(data, list) else []


def container_status(data):
    result = scalars(data, ("name", "image", "imageID"), ("restartCount",), ("ready", "started"))
    for field in ("state", "lastState"):
        states = data.get(field)
        if not isinstance(states, dict):
            continue
        result[field] = {}
        for state, strings, integers in (
            ("running", ("startedAt",), ()),
            ("waiting", ("reason",), ()),
            ("terminated", ("reason", "startedAt", "finishedAt"), ("exitCode", "signal")),
        ):
            if isinstance(states.get(state), dict):
                result[field][state] = scalars(states[state], strings, integers)
    return result


def pod_snapshot(data):
    result = {"metadata": scalars(data.get("metadata"),
                                  ("name", "namespace", "uid", "creationTimestamp"))}
    spec = data.get("spec")
    if isinstance(spec, dict):
        result["spec"] = {
            field: [scalars(item, ("name", "image")) for item in objects(spec[field])]
            for field in ("containers", "initContainers") if field in spec
        }
    status = data.get("status")
    if isinstance(status, dict):
        result["status"] = scalars(status, ("phase", "reason", "startTime"))
        if "conditions" in status:
            result["status"]["conditions"] = [
                scalars(condition, ("type", "status", "reason", "lastProbeTime", "lastTransitionTime"))
                for condition in objects(status["conditions"])
                if condition.get("type") in ("PodScheduled", "Ready")
            ]
        for field in ("containerStatuses", "initContainerStatuses"):
            if field in status:
                result["status"][field] = [container_status(item) for item in objects(status[field])]
    return result


def event_snapshot(data):
    result = scalars(data, ("reason", "firstTimestamp", "lastTimestamp", "eventTime"), ("count",))
    result["metadata"] = scalars(data.get("metadata"),
                                 ("name", "namespace", "uid", "creationTimestamp"))
    result["involvedObject"] = scalars(data.get("involvedObject"), ("kind", "name", "namespace", "uid"))
    if isinstance(data.get("series"), dict):
        result["series"] = scalars(data["series"], ("lastObservedTime",), ("count",))
    return result


def collect(kubectl, context, namespace, resource, timeout):
    result = {"namespace": namespace, "resource": resource}
    if timeout <= 0:
        return dict(result, reason="budget-exhausted")
    try:
        response = subprocess.run(
            [kubectl, "--context", context, "-n", namespace, "get", resource,
             "--request-timeout=5s", "-o", "json"],
            capture_output=True, timeout=timeout, check=False,
        )
    except subprocess.TimeoutExpired:
        return dict(result, reason="timeout")
    except OSError:
        return dict(result, reason="kubectl-unavailable")
    if response.returncode != 0:
        return dict(result, reason="kubectl-failed")
    try:
        data = json.loads(response.stdout)
    except (ValueError, UnicodeError, RecursionError):
        return dict(result, reason="invalid-json")
    if (not isinstance(data, dict) or not isinstance(data.get("items"), list)
            or not all(isinstance(item, dict) for item in data["items"])):
        return dict(result, reason="invalid-data")
    project = pod_snapshot if resource == "pods" else event_snapshot
    result["items"] = [project(item) for item in data["items"]]
    return result


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    state = os.environ.get("KMX_HOME", "").strip()
    if not state:
        config = os.environ.get("XDG_CONFIG_HOME") or str(Path.home() / ".config")
        state = str(Path(config) / "kmx")
    parser.add_argument("--kubectl", default=str(Path(state) / "path/kubectl"),
                        help="cached kubectl executable (never searched on PATH)")
    parser.add_argument("--context", default="kind-kmx-quickstart",
                        help="explicit Kubernetes context")
    args = parser.parse_args(argv)
    if not args.context.strip():
        parser.error("--context must not be empty")
    deadline = time.monotonic() + TOTAL_BUDGET
    for namespace in NAMESPACES:
        for resource in ("pods", "events"):
            snapshot = collect(args.kubectl, args.context, namespace, resource,
                               min(SUBPROCESS_TIMEOUT, deadline - time.monotonic()))
            print("[quickstart-timing] " + json.dumps(snapshot, sort_keys=True), flush=True)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
