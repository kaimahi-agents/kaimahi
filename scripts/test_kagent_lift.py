#!/usr/bin/env python3
"""Check pinned bootstrap validation and closed external failure diagnostics."""
import contextlib
import importlib.util
import io
import json
import pathlib
import subprocess
import tempfile
import unittest
from unittest import mock

SOURCE = pathlib.Path(__file__).resolve().parent / "ci/kagent-lift.py"


class BootstrapTests(unittest.TestCase):
    def setUp(self):
        self.assertTrue(SOURCE.exists(), "bootstrap driver is missing")
        spec = importlib.util.spec_from_file_location("kagent_lift", SOURCE)
        self.driver = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(self.driver)

    def test_image_ids_require_locked_repository_and_manifest(self):
        pin = "a" * 64
        for image in ("docker-pullable://example.invalid/runtime@sha256:" + pin,
                      "containerd://sha256:" + pin, "sha256:" + pin):
            self.assertEqual(self.driver.image_digest(image, "example.invalid/runtime", {pin}), pin)
        for image in ("example.invalid/other@sha256:" + pin, "sha256:" + "b" * 64,
                      "example.invalid/runtime:0.10.3", "sha256:" + pin + "@sha256:" + pin):
            with self.subTest(image=image), self.assertRaises(ValueError):
                self.driver.image_digest(image, "example.invalid/runtime", {pin})

    def test_preexisting_or_unreadable_cluster_inventory_is_not_absence(self):
        with mock.patch.object(self.driver, "run", return_value=b"other-cluster\n"):
            self.driver.cluster_absent()
        with mock.patch.object(self.driver, "run", return_value=b"other-cluster\nkmx-kagent-lift\n"):
            with self.assertRaisesRegex(ValueError, "preexisting"):
                self.driver.cluster_absent()
        with mock.patch.object(self.driver, "run", side_effect=ValueError("unreadable")):
            with self.assertRaisesRegex(ValueError, "unreadable"):
                self.driver.cluster_absent()

    def pods(self):
        # Hand-derived release index pins, not expectations from IMAGES.
        roles = [("kagent", "controller", "ghcr.io/kagent-dev/kagent/controller", "b2572a59386b6bac1f5e652e135fdad54c0ee603d3f9ee60eb7c80bbaedf9b76"),
                 ("kagent", "postgresql", "docker.io/library/postgres", "885cf05d376c7cf27afef02073e6bdac3841252537f16e244fd1c1e6a7c99fb1"),
                 ("agents", "fixture", "docker.io/library/python", "2d9aefe2fef018a7eb2c13064c89c71929800fd2e5dccdbf52ea5da5bb8d929a")]
        pods = {ns: {"items": []} for ns in ("kagent", "agents")}
        for ns, role, repository, pin in roles:
            pods[ns]["items"].append({"metadata": {"name": "private-pod-name"},
                "spec": {"containers": [{"name": role}]}, "status": {"phase": "Running",
                "containerStatuses": [{"name": role, "ready": True, "restartCount": 3,
                                       "imageID": repository + "@sha256:" + pin}]}})
        return pods

    def test_ready_bootstrap_images_allow_restarts_but_reject_wrong_missing_or_agent(self):
        for defect in (None, "wrong", "missing", "agent", "not-ready"):
            pods = self.pods()
            if defect == "wrong":
                pods["agents"]["items"][0]["status"]["containerStatuses"][0]["imageID"] = "sha256:" + "f" * 64
            elif defect == "missing":
                pods["agents"]["items"] = []
            elif defect == "agent":
                pods["agents"]["items"][0]["spec"]["containers"][0]["name"] = "kagent"
            elif defect == "not-ready":
                pods["agents"]["items"][0]["status"]["containerStatuses"][0]["ready"] = False
            with self.subTest(defect=defect), mock.patch.object(self.driver, "get", side_effect=lambda kind, namespace: pods[namespace]):
                if defect:
                    with self.assertRaises(ValueError):
                        self.driver.verify_images()
                else:
                    self.assertEqual(set(self.driver.verify_images()), {"controller", "postgresql", "fixture"})

    def chart(self, directory):
        for name, part in (("kagent-crds", "crds"), ("kagent", "runtime")):
            path = directory / part / name
            path.mkdir(parents=True)
            (path / "Chart.yaml").write_text("name: " + name + "\nversion: 0.10.3\n")
        def deployment(name, image):
            return {"kind": "Deployment", "metadata": {"name": name}, "spec": {"replicas": 1,
                "template": {"spec": {"containers": [{"image": image}]}}}}
        return [deployment("kagent-controller", "ghcr.io/kagent-dev/kagent/controller:0.10.3@sha256:b2572a59386b6bac1f5e652e135fdad54c0ee603d3f9ee60eb7c80bbaedf9b76"),
                deployment("kagent-postgresql", "docker.io/library/postgres:18.6-alpine3.23@sha256:885cf05d376c7cf27afef02073e6bdac3841252537f16e244fd1c1e6a7c99fb1"),
                {"kind": "ConfigMap", "metadata": {"name": "kagent-controller"}, "data": {
                    "GO_IMAGE_TAG": "0.10.3@sha256:5ee9592cb7d959538d0f0fe36acf31ba3a06de084f84048b1459c3bdfafcda1f",
                    "IMAGE_TAG": "0.10.3@sha256:9a41ef053b264630ae078d2d2c7c72a85d5d77e6b2ce392169006f84055dacfb"}}]

    def test_chart_rejects_wrong_identity_optional_workloads_and_unlocked_images(self):
        for defect in (None, "version", "name", "optional", "image", "config"):
            with self.subTest(defect=defect), tempfile.TemporaryDirectory() as temp:
                directory = pathlib.Path(temp)
                rendered = self.chart(directory)
                if defect in ("version", "name"):
                    (directory / "runtime/kagent/Chart.yaml").write_text("name: " + ("wrong" if defect == "name" else "kagent") + "\nversion: " + ("0.10.2" if defect == "version" else "0.10.3"))
                elif defect == "optional":
                    extra = json.loads(json.dumps(rendered[0]))
                    extra["metadata"]["name"] = "kagent-ui"
                    rendered.append(extra)
                elif defect == "image":
                    rendered[0]["spec"]["template"]["spec"]["containers"][0]["image"] = "controller:latest"
                elif defect == "config":
                    rendered[2]["data"]["GO_IMAGE_TAG"] = "0.10.3"
                if defect:
                    with self.assertRaises(ValueError):
                        self.driver.check_chart(directory, rendered)
                else:
                    self.driver.check_chart(directory, rendered)

    def test_node_requires_pinned_container_and_index_or_amd64_identity(self):
        pin = "kindest/node:v1.35.0@sha256:4613778f3cfcd10e615029370f5786704559103cf27bef934597ba562b269661"
        for defect in (None, "tag", "id", "digest"):
            container = {"Config": {"Image": pin}, "Image": "local-image-id"}
            image = {"Id": "local-image-id", "RepoDigests": [pin]}
            if defect == "tag":
                container["Config"]["Image"] = "kindest/node:v1.35.0"
            elif defect == "id":
                container["Image"] = "other-image-id"
            elif defect == "digest":
                image["RepoDigests"] = ["kindest/node@sha256:" + "f" * 64]
            with self.subTest(defect=defect), mock.patch.object(self.driver, "run", side_effect=[json.dumps([container]).encode(), json.dumps([image]).encode()]):
                if defect:
                    with self.assertRaises(ValueError):
                        self.driver.node()
                else:
                    self.driver.node()

    def test_values_disable_optional_workloads_ui_routes_and_telemetry(self):
        values = self.driver.values()
        for name in ("kmcp", "substrate", "kagent-tools", "k8s-agent", "kgateway-agent", "istio-agent",
                     "promql-agent", "observability-agent", "argo-rollouts-agent", "helm-agent", "cilium-policy-agent",
                     "cilium-manager-agent", "cilium-debug-agent", "grafana-mcp", "oauth2-proxy"):
            self.assertEqual(values[name], {"enabled": False})
        self.assertEqual(values["ui"], {"replicas": 0, "route": {"enabled": False}})
        self.assertEqual(values["otel"], {"tracing": {"enabled": False}, "logging": {"enabled": False}})

    def test_external_failure_has_closed_phase_and_status_never_argv_or_payload(self):
        for failure, category in ((subprocess.CompletedProcess([], 29, b"private", b"private"), "exitstatus=29"),
                                  (subprocess.TimeoutExpired(["private"], 1, output=b"private"), "timeout"),
                                  (FileNotFoundError("private"), "missing-executable")):
            with self.subTest(category=category), mock.patch.object(self.driver.subprocess, "run") as run:
                if isinstance(failure, subprocess.CompletedProcess):
                    run.return_value = failure
                else:
                    run.side_effect = failure
                with self.assertRaisesRegex(ValueError, "phase=node-container-inspect.*" + category) as error:
                    self.driver.run(["private-executable", "private-arg"], phase="node-container-inspect")
                self.assertNotIn("private", str(error.exception))

    def test_diagnostic_projection_normalizes_untrusted_names_phases_and_counts(self):
        pods = self.pods()
        state = pods["agents"]["items"][0]["status"]
        state["phase"] = "private-phase"
        state["containerStatuses"][0].update(name="private-role", ready="private-ready", restartCount="private-count")
        with mock.patch.object(self.driver, "get", side_effect=lambda kind, namespace: pods[namespace]), contextlib.redirect_stdout(io.StringIO()) as out:
            self.driver.diagnostics()
        self.assertNotIn("private", out.getvalue())
        self.assertIn('"phase": "Unknown"', out.getvalue())
        self.assertIn('"role": "unknown"', out.getvalue())

    def test_bootstrap_discovery_never_calls_actual_tool(self):
        paths = []
        def http(url, data=None):
            paths.append(data["method"] if data else url)
            return {"ready": True} if url.endswith("/healthz") else {"tool_calls": 0} if url.endswith("/evidence") else {"result": {"protocolVersion": "2025-03-26", "tools": [{"name": "read_sentinel"}]}}
        with mock.patch.object(self.driver, "verify_images", return_value={}), mock.patch.object(self.driver, "kube", return_value=b'{"kagent_version":"0.10.3","git_commit":"8878c39"}'), mock.patch.object(self.driver, "forward", return_value=contextlib.nullcontext("http://loopback")), mock.patch.object(self.driver, "http_json", side_effect=http), contextlib.redirect_stdout(io.StringIO()):
            self.driver.bootstrap()
        self.assertEqual(paths, ["http://loopback/healthz", "initialize", "tools/list", "http://loopback/evidence"])

    def test_suite_destination_is_provisioned_before_observation(self):
        with tempfile.TemporaryDirectory() as temp:
            work = pathlib.Path(temp)
            namespaces, secrets = [], []
            def kube(*args, **kwargs):
                if args[:2] == ("create", "namespace"):
                    namespaces.append(args[2])
                if "secret" in args:
                    secrets.append(args[1])
                    path = pathlib.Path(next(a.split("=", 2)[2] for a in args if a.startswith("--from-file=")))
                    self.assertEqual(path.stat().st_mode & 0o777, 0o600)
                return b""
            accepted = {"status": {"conditions": [{"type": "Accepted", "status": "True"}],
                                   "discoveredTools": [{"name": "read_sentinel"}]}}
            with mock.patch.object(self.driver, "kube", side_effect=kube), mock.patch.object(self.driver, "get", return_value=accepted):
                self.driver.provision(work)
            self.assertEqual(namespaces, ["agents", "suite-destination"])
            self.assertEqual(secrets, namespaces)
            self.assertEqual(list(work.iterdir()), [])

    def test_dummy_secret_file_is_private_and_removed_even_on_create_failure(self):
        with tempfile.TemporaryDirectory() as temp:
            work = pathlib.Path(temp)
            def kube(*args, **kwargs):
                if "secret" in args:
                    path = pathlib.Path(next(a.split("=", 2)[2] for a in args if a.startswith("--from-file=")))
                    self.assertEqual(path.stat().st_mode & 0o777, 0o600)
                    self.assertTrue(path.read_text())
                    raise ValueError("failed secret creation")
            with mock.patch.object(self.driver, "kube", side_effect=kube), self.assertRaisesRegex(ValueError, "failed secret"):
                self.driver.provision(work)
            self.assertEqual(list(work.iterdir()), [])


if __name__ == "__main__":
    unittest.main()
