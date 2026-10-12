#!/usr/bin/env python3
"""Bounded, payload-free CI quickstart snapshots and opt-in kubelet observations."""
from __future__ import annotations

import argparse
from datetime import datetime, timezone
import json
import math
import os
import re
import subprocess
import time
from pathlib import Path

NAMESPACES = ("kube-system", "ollama", "orka-system")
SUBPROCESS_TIMEOUT = 8
TOTAL_BUDGET = 50
PROCESS_METRICS = ("process_cpu_seconds_total", "process_resident_memory_bytes")
NUMBER = re.compile(r"[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?")
DURATION = re.compile(r"(?:[0-9]+(?:\.[0-9]+)?(?:ns|us|ms|s|m|h))+")


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


def read_kubectl(command, timeout):
    """Keep raw output private; return only a body or a fixed external-failure code."""
    if timeout <= 0:
        return None, "budget-exhausted"
    try:
        response = subprocess.run(command, capture_output=True, timeout=timeout, check=False)
    except subprocess.TimeoutExpired:
        return None, "timeout"
    except OSError:
        return None, "kubectl-unavailable"
    if response.returncode != 0:
        return None, "kubectl-failed"
    return response.stdout, None


def collect(kubectl, context, namespace, resource, timeout):
    result = {"namespace": namespace, "resource": resource}
    body, reason = read_kubectl(
        [kubectl, "--context", context, "-n", namespace, "get", resource,
         "--request-timeout=5s", "-o", "json"], timeout)
    if reason:
        return dict(result, reason=reason)
    try:
        data = json.loads(body)
    except (ValueError, UnicodeError, RecursionError):
        return dict(result, reason="invalid-json")
    if (not isinstance(data, dict) or not isinstance(data.get("items"), list)
            or not all(isinstance(item, dict) for item in data["items"])):
        return dict(result, reason="invalid-data")
    project = pod_snapshot if resource == "pods" else event_snapshot
    result["items"] = [project(item) for item in data["items"]]
    return result


def collect_kubelet(kubectl, context, node, resource):
    result = {"node": node, "resource": resource}
    body, reason = read_kubectl(
        [kubectl, "--context", context, "get", "--raw",
         f"/api/v1/nodes/{node}/proxy/{resource}", "--request-timeout=5s"], SUBPROCESS_TIMEOUT)
    if reason:
        return dict(result, reason=reason)
    if resource == "configz":
        try:
            data = json.loads(body)
        except (ValueError, UnicodeError, RecursionError):
            return dict(result, reason="invalid-json")
        if not isinstance(data, dict):
            return dict(result, reason="invalid-data")
        config = data.get("kubeletconfig", {})
        if not isinstance(config, dict):
            return dict(result, reason="invalid-data")
        if "syncFrequency" not in config:
            return dict(result, reason="missing-values")
        frequency = config["syncFrequency"]
        if not isinstance(frequency, str) or len(frequency) > 64 or not DURATION.fullmatch(frequency):
            return dict(result, reason="invalid-data")
        result["kubeletconfig"] = {"syncFrequency": frequency}
    else:
        metrics = {}
        try:
            for line in body.decode("utf-8").splitlines():
                fields = line.split()
                if len(fields) == 2 and fields[0] in PROCESS_METRICS:
                    if not NUMBER.fullmatch(fields[1]):
                        return dict(result, reason="invalid-metrics")
                    value = float(fields[1])
                    if not math.isfinite(value) or value < 0:
                        return dict(result, reason="invalid-metrics")
                    metrics[fields[0]] = value
        except (ValueError, UnicodeError):
            return dict(result, reason="invalid-metrics")
        result["metrics"] = metrics
        if len(metrics) != len(PROCESS_METRICS):
            result["reason"] = "missing-values"
    return result


def kubelet_window(value):
    message = "must be a finite number greater than 0 and at most 120 seconds"
    try:
        window = float(value)
    except ValueError:
        raise argparse.ArgumentTypeError(message) from None
    if not math.isfinite(window) or not 0 < window <= 120:
        raise argparse.ArgumentTypeError(message)
    return window


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
    parser.add_argument("--node", help="explicit node for opt-in kubelet observations")
    parser.add_argument("--kubelet-window", type=kubelet_window,
                        help="opt-in three kubelet samples over this many seconds (up to 120)")
    args = parser.parse_args(argv)
    if not args.context.strip():
        parser.error("--context must not be empty")
    if args.kubelet_window is not None:
        if (not args.node or len(args.node) > 253
                or not all(re.fullmatch(r"[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?", label)
                           for label in args.node.split("."))):
            parser.error("--node must be a safe DNS name when --kubelet-window is enabled")
    deadline = time.monotonic() + TOTAL_BUDGET
    for namespace in NAMESPACES:
        for resource in ("pods", "events"):
            snapshot = collect(args.kubectl, args.context, namespace, resource,
                               min(SUBPROCESS_TIMEOUT, deadline - time.monotonic()))
            print("[quickstart-timing] " + json.dumps(snapshot, sort_keys=True), flush=True)
    if args.kubelet_window is not None:
        snapshot = collect_kubelet(args.kubectl, args.context, args.node, "configz")
        print("[quickstart-timing] " + json.dumps(snapshot, sort_keys=True), flush=True)
        start = time.monotonic()
        # Absolute targets avoid adding a fresh wait after slow or failed reads.
        # Four extra bounded reads add at most window + 4 * 8 seconds of I/O/wait.
        for index in range(3):
            target = start + index * args.kubelet_window / 2
            time.sleep(max(0, target - time.monotonic()))
            elapsed = time.monotonic() - start
            timestamp = datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")
            snapshot = collect_kubelet(args.kubectl, args.context, args.node, "metrics")
            snapshot.update(sample=index, elapsedSeconds=elapsed, timestamp=timestamp)
            print("[quickstart-timing] " + json.dumps(snapshot, sort_keys=True), flush=True)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
