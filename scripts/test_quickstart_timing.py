#!/usr/bin/env python3
"""Hermetic executable-boundary tests for the CI-only quickstart snapshot."""
from __future__ import annotations

import copy
from datetime import datetime
import json
import os
import subprocess
import sys
import tempfile
import time
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
HELPER = ROOT / "scripts/ci/quickstart-timing.py"
PREFIX = "[quickstart-timing] "
SENSITIVE = "private-prompt-answer-secret-sentinel"

POD = {
    "apiVersion": "v1", "kind": "Pod",
    "metadata": {"name": "controller-abc", "namespace": "orka-system",
                 "uid": "pod-uid", "creationTimestamp": "2026-06-01T10:00:00Z",
                 "annotations": {"last-applied": SENSITIVE}, "labels": {"private": SENSITIVE}},
    "spec": {"nodeName": "ci-control-plane", "serviceAccountName": SENSITIVE,
             "containers": [{"name": "controller", "image": "example/controller:v1",
                             "env": [{"name": "API_KEY", "value": SENSITIVE}],
                             "command": [SENSITIVE], "args": [SENSITIVE]}],
             "initContainers": [{"name": "init", "image": "example/init:v1"}]},
    "status": {
        "phase": "Running", "reason": "Started", "message": SENSITIVE,
        "startTime": "2026-06-01T10:00:01Z", "podIP": SENSITIVE,
        "conditions": [
            {"type": "PodScheduled", "status": "True", "reason": "Scheduled",
             "lastTransitionTime": "2026-06-01T10:00:02Z", "message": SENSITIVE},
            {"type": "Ready", "status": "False", "reason": "ContainersNotReady",
             "lastProbeTime": "2026-06-01T10:00:04Z",
             "lastTransitionTime": "2026-06-01T10:00:03Z", "message": SENSITIVE,
             "unexpected": {"answer": SENSITIVE}},
            {"type": "PrivateCondition", "status": SENSITIVE}],
        "containerStatuses": [
            {"name": "controller", "image": "example/controller:v1",
             "imageID": "docker-pullable://example/controller@sha256:abc",
             "ready": False, "started": True, "restartCount": 2,
             "state": {"waiting": {"reason": "CrashLoopBackOff", "message": SENSITIVE}},
             "lastState": {"terminated": {"reason": "Error", "exitCode": 1,
                           "startedAt": "2026-06-01T10:00:05Z",
                           "finishedAt": "2026-06-01T10:00:06Z", "message": SENSITIVE}},
             "unexpected": SENSITIVE}],
        "initContainerStatuses": [
            {"name": "init", "image": "example/init:v1", "restartCount": 0,
             "state": {"running": {"startedAt": "2026-06-01T10:00:01Z", "message": SENSITIVE}}}],
        "ephemeralContainerStatuses": [{"name": SENSITIVE}],
    },
    "logs": SENSITIVE, "answers": SENSITIVE,
}
EVENT = {
    "apiVersion": "v1", "kind": "Event",
    "metadata": {"name": "controller-pull", "namespace": "orka-system",
                 "creationTimestamp": "2026-06-01T10:00:02Z", "annotations": {"private": SENSITIVE}},
    "involvedObject": {"kind": "Pod", "name": "controller-abc",
                       "namespace": "orka-system", "uid": "pod-uid", "private": SENSITIVE},
    "reason": "Pulled", "count": 2, "firstTimestamp": "2026-06-01T10:00:02Z",
    "lastTimestamp": "2026-06-01T10:00:04Z", "eventTime": "2026-06-01T10:00:03Z",
    "series": {"count": 2, "lastObservedTime": "2026-06-01T10:00:04Z", "private": SENSITIVE},
    "message": SENSITIVE, "note": SENSITIVE, "source": {"host": SENSITIVE},
}

# Only the external kubectl boundary is substituted: parsing and projection run for real.
KUBECTL = '''import json, os, sys, time
args = sys.argv[1:]
with open(os.environ["TIMING_CALLS"], "a") as stream:
    stream.write(json.dumps(args) + "\\n")
mode = os.environ["TIMING_MODE"]
if "--raw" in args:
    endpoint = args[args.index("--raw") + 1]
    assert args == ["--context", os.environ.get("TIMING_CONTEXT", "kind-kmx-quickstart"),
                    "get", "--raw", endpoint,
                    "--request-timeout=5s"], args
    assert endpoint in ("/api/v1/nodes/kmx-quickstart-control-plane/proxy/configz",
                        "/api/v1/nodes/kmx-quickstart-control-plane/proxy/metrics"), endpoint
    print(os.environ["TIMING_SENSITIVE"], file=sys.stderr, flush=True)
    if mode == "failure" or (mode == "config-failure" and endpoint.endswith("/configz")):
        print(os.environ["TIMING_SENSITIVE"], flush=True)
        raise SystemExit(23)
    if mode == "timeout":
        print(os.environ["TIMING_SENSITIVE"], flush=True)
        time.sleep(30)
    if mode == "encoding":
        sys.stdout.buffer.write(b"\\xff" + os.environ["TIMING_SENSITIVE"].encode())
        raise SystemExit(0)
    with open(os.environ["TIMING_DATA"]) as stream:
        data = json.load(stream)
    print(data[endpoint.rsplit("/", 1)[1]], flush=True)
    raise SystemExit(0)
namespace = args[args.index("-n") + 1]
resource = args[args.index("get") + 1]
if mode == "failure" or (mode == "partial" and namespace == "kube-system" and resource == "pods"):
    print(os.environ["TIMING_SENSITIVE"], flush=True)
    print(os.environ["TIMING_SENSITIVE"], file=sys.stderr, flush=True)
    raise SystemExit(23)
if mode == "timeout":
    print(os.environ["TIMING_SENSITIVE"], flush=True)
    print(os.environ["TIMING_SENSITIVE"], file=sys.stderr, flush=True)
    time.sleep(30)
if mode == "malformed":
    print(os.environ["TIMING_SENSITIVE"] + " not JSON")
    raise SystemExit(0)
if mode == "encoding":
    sys.stdout.buffer.write(b"\\xff" + os.environ["TIMING_SENSITIVE"].encode())
    raise SystemExit(0)
with open(os.environ["TIMING_DATA"]) as stream:
    data = json.load(stream)
print(os.environ["TIMING_SENSITIVE"], file=sys.stderr)
print(json.dumps(data[resource]))
'''

# Preserve real subprocess execution while recording the timeouts supplied by the helper.
DRIVER = '''import importlib.util, json, os, sys
spec = importlib.util.spec_from_file_location("quickstart_timing", sys.argv[1])
helper = importlib.util.module_from_spec(spec)
spec.loader.exec_module(helper)
run = helper.subprocess.run
def observed_run(*args, **kwargs):
    with open(os.environ["TIMING_TIMEOUTS"], "a") as stream:
        stream.write(json.dumps(kwargs.get("timeout")) + "\\n")
    return run(*args, **kwargs)
helper.subprocess.run = observed_run
if os.environ["TIMING_MODE"] == "timeout":
    helper.SUBPROCESS_TIMEOUT = 0.15
raise SystemExit(helper.main(sys.argv[2:]))
'''


class QuickstartTimingTests(unittest.TestCase):
    def setUp(self):
        self.assertTrue(HELPER.is_file(), "missing CI helper: " + str(HELPER))
        self.temp = tempfile.TemporaryDirectory(prefix="quickstart-timing-test-")
        self.addCleanup(self.temp.cleanup)
        self.directory = Path(self.temp.name)
        self.kubectl = self.directory / "cached-kubectl"
        self.kubectl.write_text("#!" + sys.executable + "\n" + KUBECTL)
        self.kubectl.chmod(0o755)
        self.calls = self.directory / "calls.jsonl"
        self.timeouts = self.directory / "timeouts.jsonl"
        self.data = self.directory / "data.json"
        self.data.write_text(json.dumps({"pods": {"items": [POD]}, "events": {"items": [EVENT]}}))
        self.environ = dict(os.environ, TIMING_CALLS=str(self.calls),
                            TIMING_TIMEOUTS=str(self.timeouts), TIMING_DATA=str(self.data),
                            TIMING_SENSITIVE=SENSITIVE, TIMING_MODE="success",
                            HOME=str(self.directory), XDG_CONFIG_HOME="", KMX_HOME="",
                            KUBE_CTX="wrong-inherited-context", KIND_CLUSTER="wrong-cluster")

    def invoke(self, mode="success", args=None, instrument=False):
        self.environ["TIMING_MODE"] = mode
        args = ["--kubectl", str(self.kubectl)] if args is None else args
        command = ([sys.executable, "-B", "-c", DRIVER, str(HELPER)] if instrument
                   else [sys.executable, "-B", str(HELPER)])
        return subprocess.run(command + args, env=self.environ, text=True,
                              capture_output=True, timeout=5, check=False)

    def kubelet_args(self, window="0.1", node="kmx-quickstart-control-plane"):
        return ["--kubectl", str(self.kubectl), "--node", node,
                "--kubelet-window", window]

    def kubelet_data(self, config=None, metrics=None):
        data = json.loads(self.data.read_text())
        data["configz"] = json.dumps({"kubeletconfig": {
            "syncFrequency": "1m0s", "authentication": {"token": SENSITIVE},
            "staticPodPath": SENSITIVE}, "private": SENSITIVE}) if config is None else config
        data["metrics"] = (
            "# HELP process_cpu_seconds_total " + SENSITIVE + "\n"
            "process_cpu_seconds_total 12.5\n"
            "process_resident_memory_bytes 1.048576e+08\n"
            'private_metric{credential="' + SENSITIVE + '"} 999\n'
        ) if metrics is None else metrics
        self.data.write_text(json.dumps(data))

    def observations(self, result):
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stderr, "")
        self.assertNotIn(SENSITIVE, result.stdout)
        lines = result.stdout.splitlines()
        self.assertEqual(len(lines), 10)
        self.assertTrue(all(line.startswith(PREFIX) for line in lines), result.stdout)
        records = [json.loads(line[len(PREFIX):]) for line in lines]
        self.assertEqual([(item["namespace"], item["resource"]) for item in records[:6]],
                         [(namespace, resource)
                          for namespace in ("kube-system", "ollama", "orka-system")
                          for resource in ("pods", "events")])
        return records[6:]

    def test_opt_in_reads_effective_config_and_three_numeric_resource_samples(self):
        # Removing the node-proxy reads or printing raw bodies must break this test.
        self.kubelet_data()
        records = self.observations(self.invoke(args=self.kubelet_args(), instrument=True))
        self.assertEqual(records[0], {
            "node": "kmx-quickstart-control-plane", "resource": "configz",
            "kubeletconfig": {"syncFrequency": "1m0s"}})
        for index, sample in enumerate(records[1:]):
            self.assertEqual(set(sample), {"node", "resource", "sample", "elapsedSeconds",
                                          "timestamp", "metrics"})
            self.assertEqual(sample["node"], "kmx-quickstart-control-plane")
            self.assertEqual(sample["resource"], "metrics")
            self.assertEqual(sample["sample"], index)
            self.assertEqual(sample["metrics"], {"process_cpu_seconds_total": 12.5,
                                               "process_resident_memory_bytes": 104857600.0})
            self.assertGreaterEqual(sample["elapsedSeconds"], index * 0.05)
            self.assertTrue(sample["timestamp"].endswith("Z"))
            datetime.fromisoformat(sample["timestamp"].replace("Z", "+00:00"))
        elapsed = [sample["elapsedSeconds"] for sample in records[1:]]
        self.assertEqual(elapsed, sorted(elapsed))
        self.assertLess(elapsed[-1], 1)
        calls = self.recorded_calls()
        self.assertEqual(len(calls), 10)
        self.assertTrue(all("--raw" not in args for args in calls[:6]))
        self.assertEqual([args[args.index("--raw") + 1] for args in calls[6:]],
                         ["/api/v1/nodes/kmx-quickstart-control-plane/proxy/configz"] +
                         ["/api/v1/nodes/kmx-quickstart-control-plane/proxy/metrics"] * 3)
        timeouts = [json.loads(line) for line in self.timeouts.read_text().splitlines()]
        self.assertEqual(len(timeouts), 10)
        self.assertTrue(all(0 < timeout <= 8 for timeout in timeouts))

    def test_opt_in_rejects_missing_or_unsafe_node_before_reads(self):
        # A path traversal or option-like node must never reach the proxy URL.
        nodes = (None, "", "../private", "node/proxy/metrics", "node?token=" + SENSITIVE,
                 "node;" + SENSITIVE, "-node", "node_1", "Uppercase", "node\nprivate",
                 "a..b", "a.", "a" * 64, ".".join(["a" * 63] * 4))
        for node in nodes:
            with self.subTest(node=node):
                args = ["--kubectl", str(self.kubectl), "--kubelet-window=0.1"]
                if node is not None:
                    args += ["--node=" + node]
                result = self.invoke(args=args)
                self.assertEqual(result.returncode, 2, result.stdout)
                self.assertEqual(result.stdout, "")
                self.assertNotIn(SENSITIVE, result.stderr)
                self.assertFalse(self.calls.exists())

    def test_opt_in_rejects_nonfinite_nonpositive_or_excessive_window_before_reads(self):
        # Unbounded/invalid windows must be argument errors, not delayed diagnostics.
        for window in ("-1", "0", "nan", "inf", "-inf", "120.001", SENSITIVE, ""):
            with self.subTest(window=window):
                args = ["--kubectl", str(self.kubectl), "--node=kmx-quickstart-control-plane",
                        "--kubelet-window=" + window]
                try:
                    result = self.invoke(args=args)
                except subprocess.TimeoutExpired:
                    self.fail("invalid window delayed instead of rejecting before reads")
                self.assertEqual(result.returncode, 2, result.stdout)
                self.assertEqual(result.stdout, "")
                self.assertNotIn(SENSITIVE, result.stderr)
                self.assertFalse(self.calls.exists())

    def test_configz_rejects_raw_json_html_wrong_shapes_and_private_duration(self):
        # Field selection alone must not leak a private string as syncFrequency.
        cases = ((SENSITIVE + " not JSON", "invalid-json"),
                 ("<html>" + SENSITIVE + "</html>", "invalid-json"),
                 (json.dumps([SENSITIVE]), "invalid-data"),
                 (json.dumps({"kubeletconfig": {"syncFrequency": SENSITIVE}}), "invalid-data"),
                 (json.dumps({"kubeletconfig": {"syncFrequency": {"private": SENSITIVE}}}),
                  "invalid-data"))
        for config, reason in cases:
            with self.subTest(reason=reason, config=config):
                self.kubelet_data(config=config)
                records = self.observations(self.invoke(args=self.kubelet_args()))
                self.assertEqual(records[0], {"node": "kmx-quickstart-control-plane",
                                             "resource": "configz", "reason": reason})
                self.assertTrue(all("metrics" in sample for sample in records[1:]))

    def test_configz_missing_sync_frequency_is_incomplete_not_success(self):
        # Missing effective configuration must not look like successful qualification.
        for config in ({}, {"kubeletconfig": {}}, {"kubeletconfig": {"private": SENSITIVE}}):
            with self.subTest(config=config):
                self.kubelet_data(config=json.dumps(config))
                record = self.observations(self.invoke(args=self.kubelet_args()))[0]
                self.assertEqual(record, {"node": "kmx-quickstart-control-plane",
                                          "resource": "configz", "reason": "missing-values"})

    def test_metrics_reject_nonfinite_or_non_numeric_values_without_raw_output(self):
        # NaN/Inf, overflow and Python-only float syntax must never enter JSON evidence.
        for value in ("NaN", "+Inf", "-Inf", "1e9999", "1_0", SENSITIVE):
            with self.subTest(value=value):
                self.kubelet_data(metrics="process_cpu_seconds_total " + value + "\n"
                                  "process_resident_memory_bytes 123\n")
                samples = self.observations(self.invoke(args=self.kubelet_args()))[1:]
                for sample in samples:
                    self.assertEqual(sample.get("reason"), "invalid-metrics")
                    self.assertNotIn("metrics", sample)

    def test_metrics_reject_negative_cpu_or_resident_memory(self):
        # Finite but physically invalid process observations must not become evidence.
        for metrics in ("process_cpu_seconds_total -0.5\nprocess_resident_memory_bytes 123\n",
                        "process_cpu_seconds_total 0\nprocess_resident_memory_bytes -1\n"):
            with self.subTest(metrics=metrics):
                self.kubelet_data(metrics=metrics)
                for sample in self.observations(self.invoke(args=self.kubelet_args()))[1:]:
                    self.assertEqual(sample.get("reason"), "invalid-metrics")
                    self.assertNotIn("metrics", sample)

    def test_metrics_missing_values_and_labeled_or_textual_lines_are_incomplete(self):
        # No label values, credentials or unrelated metrics may become evidence.
        for metrics in ("", "<html>" + SENSITIVE + "</html>",
                        'process_cpu_seconds_total{token="' + SENSITIVE + '"} 7\n',
                        "process_cpu_seconds_total 7 " + SENSITIVE + "\n",
                        "private_metric 7\ncredentials=" + SENSITIVE):
            with self.subTest(metrics=metrics):
                self.kubelet_data(metrics=metrics)
                for sample in self.observations(self.invoke(args=self.kubelet_args()))[1:]:
                    self.assertEqual(sample.get("reason"), "missing-values")
                    self.assertEqual(sample["metrics"], {})
        self.kubelet_data(metrics="process_cpu_seconds_total +1.25e2\n")
        for sample in self.observations(self.invoke(args=self.kubelet_args()))[1:]:
            self.assertEqual(sample.get("reason"), "missing-values")
            self.assertEqual(sample["metrics"], {"process_cpu_seconds_total": 125.0})

    def test_proxy_external_failures_are_nonfatal_and_never_echo_partial_output(self):
        # A failed/expired kubectl call must not leak its body, stderr or exception.
        for mode, reason in (("failure", "kubectl-failed"), ("timeout", "timeout"),
                             ("encoding", "invalid-json")):
            with self.subTest(mode=mode):
                self.kubelet_data()
                self.calls.unlink(missing_ok=True)
                start = time.monotonic()
                records = self.observations(self.invoke(mode=mode, args=self.kubelet_args(),
                                                       instrument=True))
                self.assertEqual(records[0], {"node": "kmx-quickstart-control-plane",
                                             "resource": "configz", "reason": reason})
                for sample in records[1:]:
                    self.assertEqual(sample["reason"],
                                     "invalid-metrics" if mode == "encoding" else reason)
                    self.assertEqual(set(sample), {"node", "resource", "reason", "sample",
                                                  "elapsedSeconds", "timestamp"})
                self.assertEqual(len(self.recorded_calls()), 10)
                self.assertLess(time.monotonic() - start, 5)
                elapsed = [sample["elapsedSeconds"] for sample in records[1:]]
                self.assertEqual(elapsed, sorted(elapsed))
                if mode == "timeout":
                    # Failed samples overrun the short window, without another full-window wait.
                    self.assertGreaterEqual(elapsed[-1], 0.3)
                    self.assertLess(elapsed[-1], 1)

    def test_proxy_config_failure_does_not_skip_resource_measurements(self):
        self.kubelet_data()
        records = self.observations(self.invoke(mode="config-failure", args=self.kubelet_args()))
        self.assertEqual(records[0]["reason"], "kubectl-failed")
        self.assertTrue(all(sample["metrics"]["process_cpu_seconds_total"] == 12.5
                            for sample in records[1:]))
        self.assertEqual(len(self.recorded_calls()), 10)

    def test_proxy_missing_executable_remains_payload_free(self):
        args = self.kubelet_args()
        args[1] = str(self.directory / SENSITIVE)
        for record in self.observations(self.invoke(args=args)):
            self.assertEqual(record["reason"], "kubectl-unavailable")
        self.assertFalse(self.calls.exists())

    def test_proxy_uses_cached_kubectl_and_explicit_context_without_shell(self):
        self.kubelet_data()
        cached = self.directory / ".config/kmx/path/kubectl"
        cached.parent.mkdir(parents=True)
        cached.symlink_to(self.kubectl)
        self.environ["PATH"] = ""
        context = "kind-explicit; no-shell"
        self.environ["TIMING_CONTEXT"] = context
        self.observations(self.invoke(args=["--context", context, "--node",
                                           "kmx-quickstart-control-plane",
                                           "--kubelet-window", "0.1"]))
        self.assertEqual(len(self.recorded_calls()), 10)
        self.assertTrue(all(args[:2] == ["--context", context] for args in self.recorded_calls()))

    def test_node_without_window_does_not_enable_extra_calls(self):
        self.snapshots(self.invoke(args=["--kubectl", str(self.kubectl), "--node", "unused-node"]))
        self.assertEqual(len(self.recorded_calls()), 6)

    def snapshots(self, result):
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stderr, "")
        self.assertNotIn(SENSITIVE, result.stdout)
        lines = result.stdout.splitlines()
        self.assertEqual(len(lines), 6)
        self.assertTrue(all(line.startswith(PREFIX) for line in lines), result.stdout)
        return [json.loads(line[len(PREFIX):]) for line in lines]

    def recorded_calls(self):
        return [json.loads(line) for line in self.calls.read_text().splitlines()]

    def test_explicit_scoped_reads_have_request_and_process_timeout_within_budget(self):
        self.snapshots(self.invoke(instrument=True))
        calls = self.recorded_calls()
        expected = [(namespace, resource) for namespace in ("kube-system", "ollama", "orka-system")
                    for resource in ("pods", "events")]
        self.assertEqual(len(calls), 6)
        self.assertEqual([(args[args.index("-n") + 1], args[args.index("get") + 1])
                          for args in calls], expected)
        for args in calls:
            self.assertEqual(args[:2], ["--context", "kind-kmx-quickstart"])
            self.assertEqual(args[-2:], ["-o", "json"])
            request_timeout = next(arg.split("=", 1)[1] for arg in args
                                   if arg.startswith("--request-timeout="))
            self.assertTrue(request_timeout.endswith("s"))
            self.assertGreater(float(request_timeout[:-1]), 0)
            self.assertLessEqual(float(request_timeout[:-1]), 8)
            self.assertEqual(len(args), 9, args)
        timeouts = [json.loads(line) for line in self.timeouts.read_text().splitlines()]
        self.assertEqual(len(timeouts), 6)
        self.assertTrue(all(0 < timeout <= 10 for timeout in timeouts))
        self.assertLessEqual(sum(timeouts), 60)

    def test_default_uses_cached_binary_not_path_or_inherited_context(self):
        cached = self.directory / ".config/kmx/path/kubectl"
        cached.parent.mkdir(parents=True)
        cached.symlink_to(self.kubectl)
        self.environ["PATH"] = ""
        snapshots = self.snapshots(self.invoke(args=[]))
        self.assertEqual(snapshots[0]["namespace"], "kube-system")
        self.assertEqual(self.recorded_calls()[0][:2], ["--context", "kind-kmx-quickstart"])

    def test_cached_binary_respects_explicit_state_and_xdg_directories(self):
        for variable, suffix in (("KMX_HOME", "path/kubectl"),
                                 ("XDG_CONFIG_HOME", "kmx/path/kubectl")):
            with self.subTest(variable=variable):
                location = self.directory / variable
                cached = location / suffix
                cached.parent.mkdir(parents=True)
                cached.symlink_to(self.kubectl)
                self.environ[variable] = str(location)
                self.snapshots(self.invoke(args=[]))
                self.environ[variable] = ""

    def test_explicit_context_is_forwarded_without_shell_interpretation(self):
        context = "kind-explicit; no-shell"
        self.snapshots(self.invoke(args=["--kubectl", str(self.kubectl), "--context", context]))
        self.assertEqual(self.recorded_calls()[0][:2], ["--context", context])

    def test_pod_snapshot_keeps_only_selected_scalar_fields_and_normalized_conditions(self):
        pod = self.snapshots(self.invoke())[4]["items"][0]
        self.assertEqual(pod, {
            "metadata": {"name": "controller-abc", "namespace": "orka-system", "uid": "pod-uid",
                         "creationTimestamp": "2026-06-01T10:00:00Z"},
            "spec": {"containers": [{"name": "controller", "image": "example/controller:v1"}],
                     "initContainers": [{"name": "init", "image": "example/init:v1"}]},
            "status": {
                "phase": "Running", "reason": "Started", "startTime": "2026-06-01T10:00:01Z",
                "conditions": [
                    {"type": "PodScheduled", "status": "True", "reason": "Scheduled",
                     "lastTransitionTime": "2026-06-01T10:00:02Z"},
                    {"type": "Ready", "status": "False", "reason": "ContainersNotReady",
                     "lastProbeTime": "2026-06-01T10:00:04Z",
                     "lastTransitionTime": "2026-06-01T10:00:03Z"}],
                "containerStatuses": [
                    {"name": "controller", "image": "example/controller:v1",
                     "imageID": "docker-pullable://example/controller@sha256:abc",
                     "ready": False, "started": True, "restartCount": 2,
                     "state": {"waiting": {"reason": "CrashLoopBackOff"}},
                     "lastState": {"terminated": {"reason": "Error", "exitCode": 1,
                                    "startedAt": "2026-06-01T10:00:05Z",
                                    "finishedAt": "2026-06-01T10:00:06Z"}}}],
                "initContainerStatuses": [
                    {"name": "init", "image": "example/init:v1", "restartCount": 0,
                     "state": {"running": {"startedAt": "2026-06-01T10:00:01Z"}}}],
            },
        })

    def test_event_snapshot_drops_message_note_source_and_full_object(self):
        event = self.snapshots(self.invoke())[5]["items"][0]
        self.assertEqual(event, {
            "metadata": {"name": "controller-pull", "namespace": "orka-system",
                         "creationTimestamp": "2026-06-01T10:00:02Z"},
            "involvedObject": {"kind": "Pod", "name": "controller-abc",
                               "namespace": "orka-system", "uid": "pod-uid"},
            "reason": "Pulled", "count": 2, "firstTimestamp": "2026-06-01T10:00:02Z",
            "lastTimestamp": "2026-06-01T10:00:04Z", "eventTime": "2026-06-01T10:00:03Z",
            "series": {"count": 2, "lastObservedTime": "2026-06-01T10:00:04Z"},
        })

    def test_empty_lists_are_valid_snapshots(self):
        self.data.write_text(json.dumps({"pods": {"items": []}, "events": {"items": []}}))
        for snapshot in self.snapshots(self.invoke()):
            self.assertEqual(snapshot["items"], [])
            self.assertNotIn("reason", snapshot)

    def test_wrong_field_types_cannot_smuggle_nested_sensitive_content(self):
        pod = copy.deepcopy(POD)
        pod["metadata"]["name"] = {"answer": SENSITIVE}
        pod["status"]["conditions"][0]["reason"] = [SENSITIVE]
        pod["status"]["containerStatuses"][0]["restartCount"] = {"answer": SENSITIVE}
        pod["status"]["containerStatuses"][0]["state"]["waiting"]["reason"] = {"answer": SENSITIVE}
        event = copy.deepcopy(EVENT)
        event["count"] = True
        event["involvedObject"]["name"] = [SENSITIVE]
        self.data.write_text(json.dumps({"pods": {"items": [pod]}, "events": {"items": [event]}}))
        snapshots = self.snapshots(self.invoke())
        self.assertNotIn("name", snapshots[0]["items"][0]["metadata"])
        self.assertNotIn("reason", snapshots[0]["items"][0]["status"]["conditions"][0])
        self.assertNotIn("restartCount", snapshots[0]["items"][0]["status"]["containerStatuses"][0])
        self.assertNotIn("count", snapshots[1]["items"][0])

    def test_failed_kubectl_suppresses_stdout_stderr_and_continues_other_resources(self):
        snapshots = self.snapshots(self.invoke(mode="partial"))
        self.assertEqual(snapshots[0], {"namespace": "kube-system", "resource": "pods",
                                       "reason": "kubectl-failed"})
        self.assertIn("items", snapshots[1])
        self.assertIn("items", snapshots[-1])
        self.assertEqual(len(self.recorded_calls()), 6)

    def test_missing_executable_path_and_exception_details_are_not_printed(self):
        result = self.invoke(args=["--kubectl", str(self.directory / SENSITIVE)])
        for snapshot in self.snapshots(result):
            self.assertEqual(snapshot["reason"], "kubectl-unavailable")
            self.assertEqual(set(snapshot), {"namespace", "resource", "reason"})

    def test_malformed_json_and_invalid_encoding_are_nonfatal_without_raw_data(self):
        for mode, reason in (("malformed", "invalid-json"), ("encoding", "invalid-json"),
                             ("failure", "kubectl-failed")):
            with self.subTest(mode=mode):
                for snapshot in self.snapshots(self.invoke(mode=mode)):
                    self.assertEqual(snapshot["reason"], reason)
                    self.assertEqual(set(snapshot), {"namespace", "resource", "reason"})

    def test_invalid_list_shapes_are_nonfatal_without_echoing_data(self):
        for document in ([SENSITIVE], {"items": SENSITIVE}, {"items": [SENSITIVE]}, {}):
            with self.subTest(document=document):
                self.data.write_text(json.dumps({"pods": document, "events": document}))
                for snapshot in self.snapshots(self.invoke()):
                    self.assertEqual(snapshot["reason"], "invalid-data")

    def test_timeout_kills_real_children_without_echoing_partial_output(self):
        start = time.monotonic()
        for snapshot in self.snapshots(self.invoke(mode="timeout", instrument=True)):
            self.assertEqual(snapshot["reason"], "timeout")
        self.assertLess(time.monotonic() - start, 5)
        self.assertEqual(len(self.recorded_calls()), 6)

    def test_argparse_errors_fail_before_any_kubectl_call(self):
        for args in (["--unsupported"], ["--context"], ["--kubectl"]):
            with self.subTest(args=args):
                result = self.invoke(args=args)
                self.assertEqual(result.returncode, 2)
                self.assertIn("usage:", result.stderr)
                self.assertEqual(result.stdout, "")
                self.assertFalse(self.calls.exists())

    def test_workflow_adds_resource_window_only_for_manual_runs(self):
        import yaml

        workflow = yaml.safe_load((ROOT / ".github/workflows/ci.yml").read_text())
        step = next(s for s in workflow["jobs"]["e2e-quickstart"]["steps"]
                    if s.get("name") == "Report first-answer pod and kubelet timings")
        recorded = self.directory / "workflow-args.json"
        python = self.directory / "python3"
        python.write_text("#!" + sys.executable + "\nimport json, os, sys\n"
                          "with open(os.environ['TIMING_WORKFLOW_ARGS'], 'w') as stream:\n"
                          "    json.dump(sys.argv[1:], stream)\n")
        python.chmod(0o755)
        for event in ("workflow_dispatch", "pull_request", "push", "schedule"):
            with self.subTest(event=event):
                result = subprocess.run(
                    ["bash", "-e", "-c", step["run"]], cwd=ROOT,
                    env=dict(self.environ, EVENT=event, TIMING_WORKFLOW_ARGS=str(recorded),
                             PATH=str(self.directory) + os.pathsep + os.environ["PATH"]),
                    capture_output=True, text=True, timeout=5, check=False,
                )
                self.assertEqual(result.returncode, 0, result.stderr)
                args = json.loads(recorded.read_text())
                self.assertEqual(args[0], "scripts/ci/quickstart-timing.py")
                self.assertIn("kind-kmx-quickstart", args)
                if event == "workflow_dispatch":
                    self.assertEqual(args[args.index("--kubelet-window") + 1], "120")
                    self.assertEqual(args[args.index("--node") + 1], "kmx-quickstart-control-plane")
                else:
                    self.assertNotIn("--kubelet-window", args)
                    self.assertNotIn("--node", args)

    def test_empty_context_cannot_fall_back_to_current_kubeconfig_context(self):
        for context in ("", "   "):
            with self.subTest(context=context):
                result = self.invoke(args=["--kubectl", str(self.kubectl), "--context", context])
                self.assertEqual(result.returncode, 2)
                self.assertIn("usage:", result.stderr)
                self.assertEqual(result.stdout, "")
                self.assertFalse(self.calls.exists())


if __name__ == "__main__":
    unittest.main()
