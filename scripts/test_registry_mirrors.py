#!/usr/bin/env python3
"""Contract tests for the CI-only registry helper; no Docker daemon needed."""
from __future__ import annotations

import importlib.util
import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
HELPER = ROOT / "scripts/ci/registry-mirrors.py"

# Literal containerd debug records: only successful fetch responses prove a route.
FETCHES = '''time="2026-06-01T10:00:00Z" level=debug msg="fetch response received" host=mirror.gcr.io request.method=GET response.status="200 OK" url="https://mirror.gcr.io/v2/library/python/manifests/3.12?ns=docker.io&token=query-secret" response.header.authorization="Bearer header-secret" response.header.content-type=application/vnd.oci.image.manifest.v1+json
time="2026-06-01T10:00:01Z" level=debug msg="fetch response received" host=mirror.gcr.io request.method=GET response.status="200 OK" url="https://mirror.gcr.io/v2/library/python/blobs/sha256:abcdef?signature=blob-secret" response.header.authorization="Bearer header-secret"
time="2026-06-01T10:00:02Z" level=debug msg="fetch response received" host=mirror.gcr.io request.method=GET response.status="200 OK" url="https://mirror.gcr.io/v2/library/python/manifests/sha256:abcdef" response.header.content-length=1234
'''
FALLBACK = '''time="2026-06-01T10:01:00Z" level=debug msg="fetch response received" host=mirror.gcr.io request.method=GET response.status="404 Not Found" url="https://mirror.gcr.io/v2/kindest/node/manifests/v1.31.0"
time="2026-06-01T10:01:01Z" level=debug msg="fetch response received" host=registry-1.docker.io request.method=GET response.status="200 OK" url="https://registry-1.docker.io/v2/kindest/node/manifests/v1.31.0?token=fallback-secret" response.header.authorization="Bearer fallback-header-secret"
'''
NOT_FETCHES = '''time="2026-06-01T10:02:00Z" level=debug msg="do request" host=mirror.gcr.io request.method=GET url="https://mirror.gcr.io/v2/library/python/manifests/3.12" request.header.authorization="Bearer request-secret"
time="2026-06-01T10:02:01Z" level=debug msg="fetch response received" host=mirror.gcr.io response.status="401 Unauthorized" url="https://mirror.gcr.io/v2/library/python/manifests/3.12"
time="2026-06-01T10:02:02Z" level=debug msg="fetch response received" host=mirror.gcr.io response.status="500 Internal Server Error" url="https://mirror.gcr.io/v2/library/python/blobs/sha256:abcdef"
time="2026-06-01T10:02:03Z" level=debug msg="content already exists" host=mirror.gcr.io response.status="200 OK" url="https://mirror.gcr.io/v2/library/python/manifests/3.12"
time="2026-06-01T10:02:04Z" level=debug msg="fetch response received" host=auth.docker.io response.status="200 OK" url="https://auth.docker.io/token?service=registry.docker.io&token=auth-secret" payload="payload-secret"
'''

# A real executable boundary retains argv, inherited output, exit codes and order.
DOCKER_FIXTURE = '''import json, os, sys
with open(os.environ["REGISTRY_TEST_EVENTS"], "a") as stream:
    stream.write(json.dumps(["docker", sys.argv[1:]]) + "\\n")
print(json.dumps(sys.argv[1:]), flush=True)
print("docker stderr sentinel", file=sys.stderr, flush=True)
code = int(os.environ["REGISTRY_TEST_EXIT"])
if code < 0:
    os.kill(os.getpid(), -code)
raise SystemExit(code)
'''
DRIVER = '''import importlib.util, json, os, subprocess, sys
spec = importlib.util.spec_from_file_location("registry_mirrors_driver", sys.argv[1])
helper = importlib.util.module_from_spec(spec)
spec.loader.exec_module(helper)
def setup(name, docker_real):
    with open(os.environ["REGISTRY_TEST_EVENTS"], "a") as stream:
        stream.write(json.dumps(["setup", name, docker_real]) + "\\n")
    if os.environ["REGISTRY_TEST_SETUP_FAIL"] == "1":
        raise subprocess.CalledProcessError(23, [docker_real, "exec", name])
helper.setup_node = setup
raise SystemExit(helper.docker_main(json.loads(sys.argv[3]), sys.argv[2]))
'''


class RegistryMirrorTests(unittest.TestCase):
    def setUp(self):
        self.assertTrue(HELPER.is_file(), "missing CI helper: " + str(HELPER))
        spec = importlib.util.spec_from_file_location("registry_mirrors", HELPER)
        self.helper = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(self.helper)

    def invoke_docker(self, args, exit_code=0, setup_fail=False):
        with tempfile.TemporaryDirectory(prefix="registry-mirrors-test-") as tmp:
            docker = Path(tmp) / "docker-real"
            docker.write_text("#!" + sys.executable + "\n" + DOCKER_FIXTURE)
            docker.chmod(0o755)
            events = Path(tmp) / "events.jsonl"
            environ = dict(os.environ, REGISTRY_TEST_EVENTS=str(events),
                           REGISTRY_TEST_EXIT=str(exit_code),
                           REGISTRY_TEST_SETUP_FAIL="1" if setup_fail else "0")
            result = subprocess.run(
                [sys.executable, "-B", "-c", DRIVER, str(HELPER), str(docker),
                 json.dumps(args)], env=environ, capture_output=True, text=True,
                timeout=10, check=False,
            )
            recorded = [json.loads(line) for line in events.read_text().splitlines()]
            return result, recorded, str(docker)

    def assert_forwarded_output(self, result, args):
        self.assertEqual(result.stdout, json.dumps(args) + "\n")
        self.assertIn("docker stderr sentinel\n", result.stderr)

    def test_daemon_merge_preserves_unrelated_settings_and_overrides_mirror_debug(self):
        config = {"log-driver": "json-file", "features": {"buildkit": True},
                  "registry-mirrors": ["https://old.example"], "debug": False}
        self.assertEqual(self.helper.merge_daemon_config(config), {
            "log-driver": "json-file", "features": {"buildkit": True},
            "registry-mirrors": ["https://mirror.gcr.io"], "debug": True,
        })
        self.assertEqual(self.helper.merge_daemon_config({}), {
            "registry-mirrors": ["https://mirror.gcr.io"], "debug": True,
        })

    def test_kind_node_requires_explicit_two_token_labels_for_both_roles(self):
        for role, name in (("control-plane", "ci-control-plane"), ("worker", "ci-worker")):
            with self.subTest(role=role):
                args = ["run", "--detach", "--label", "io.x-k8s.kind.role=" + role,
                        "--name", name, "--label", "io.x-k8s.kind.cluster=ci",
                        "kindest/node:v1.31.0"]
                self.assertEqual(self.helper.kind_node_name(args), name)

    def test_non_nodes_and_other_docker_commands_are_not_matched(self):
        labels = ["--label", "io.x-k8s.kind.cluster=ci", "--label",
                  "io.x-k8s.kind.role=control-plane"]
        cases = [
            [], ["build", "--name", "node"] + labels,
            ["inspect", "--name", "node"] + labels,
            ["run", "--name", "lb", "--label", "io.x-k8s.kind.cluster=ci",
             "--label", "io.x-k8s.kind.role=external-load-balancer"],
            ["run", "--name", "node", "--label", "io.x-k8s.kind.role=worker"],
            ["run", "--name", "node", "--label", "io.x-k8s.kind.cluster=ci"],
            ["run", "--name", "node", "--label", "io.x-k8s.kind.cluster=",
             "--label", "io.x-k8s.kind.role=worker"],
            ["run", "--name", "node", "--label", "io.x-k8s.kind.cluster=ci",
             "--label", "io.x-k8s.kind.role=other"],
            ["run"] + labels, ["run", "--name", ""] + labels,
            ["exec", "container", "run", "--name", "node"] + labels,
            ["run", "--name", "ordinary", "alpine", "echo"] + labels,
        ]
        for args in cases:
            with self.subTest(args=args):
                self.assertIsNone(self.helper.kind_node_name(args))

    def test_docker_passthrough_preserves_argv_output_and_exit_without_setup(self):
        cases = [(["build", "--tag", "example:test", "path with spaces"], 0),
                 (["inspect", "--format", "{{.Id}}", "ci-worker"], 7),
                 (["run", "--name", "ordinary", "alpine", "echo", "hello"], 0),
                 (["run", "--name", "lb", "--label", "io.x-k8s.kind.cluster=ci",
                   "--label", "io.x-k8s.kind.role=external-load-balancer", "haproxy"], 0)]
        for args, code in cases:
            with self.subTest(args=args):
                result, events, _ = self.invoke_docker(args, exit_code=code)
                self.assertEqual(result.returncode, code, result.stderr)
                self.assert_forwarded_output(result, args)
                self.assertEqual(result.stderr, "docker stderr sentinel\n")
                self.assertEqual(events, [["docker", args]])

    def test_successful_kind_run_sets_up_node_after_real_child_exits(self):
        args = ["run", "--name", "ci-worker", "--label", "io.x-k8s.kind.cluster=ci",
                "--label", "io.x-k8s.kind.role=worker", "kindest/node:v1.31.0"]
        result, events, docker = self.invoke_docker(args)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assert_forwarded_output(result, args)
        self.assertEqual(events, [["docker", args], ["setup", "ci-worker", docker]])

    def test_failed_kind_run_preserves_child_exit_and_does_not_setup(self):
        args = ["run", "--name", "ci-control-plane", "--label",
                "io.x-k8s.kind.cluster=ci", "--label",
                "io.x-k8s.kind.role=control-plane", "kindest/node:v1.31.0"]
        result, events, _ = self.invoke_docker(args, exit_code=19)
        self.assertEqual(result.returncode, 19, result.stderr)
        self.assert_forwarded_output(result, args)
        self.assertEqual(events, [["docker", args]])

    def test_setup_failure_returns_nonzero_instead_of_successful_run_status(self):
        args = ["run", "--name", "ci-worker", "--label", "io.x-k8s.kind.cluster=ci",
                "--label", "io.x-k8s.kind.role=worker", "kindest/node:v1.31.0"]
        result, events, docker = self.invoke_docker(args, setup_fail=True)
        self.assertEqual(result.returncode, 1)
        self.assertIn("CI mirror setup failed for kind node ci-worker", result.stderr)
        self.assertNotIn("Traceback", result.stderr)
        self.assert_forwarded_output(result, args)
        self.assertEqual(events, [["docker", args], ["setup", "ci-worker", docker]])

    def test_direct_cluster_setup_filters_cluster_and_excludes_load_balancer(self):
        commands, configured = [], []
        roles = {"ci-control": "control-plane", "ci-worker": "worker",
                 "ci-lb": "external-load-balancer"}

        def docker(args, **kwargs):
            commands.append(args)
            output = "ci-control\nci-worker\nci-lb\n" if args[1] == "ps" else roles[args[-1]] + "\n"
            return SimpleNamespace(stdout=output)

        with patch.object(self.helper, "run", side_effect=docker), \
                patch.object(self.helper, "setup_node", side_effect=lambda name, engine: configured.append(name)):
            self.helper.setup_cluster("ci-exact", "/docker-real")
        self.assertEqual(commands[0], ["/docker-real", "ps", "--filter",
                         "label=io.x-k8s.kind.cluster=ci-exact", "--format", "{{.Names}}"])
        self.assertEqual(configured, ["ci-control", "ci-worker"])

    def test_direct_cluster_setup_refuses_missing_nodes(self):
        with patch.object(self.helper, "run", return_value=SimpleNamespace(stdout="")), \
                self.assertRaisesRegex(ValueError, "no Kubernetes nodes found"):
            self.helper.setup_cluster("missing", "/docker-real")

    def test_child_sigterm_retains_conventional_shell_status(self):
        args = ["inspect", "example"]
        result, events, _ = self.invoke_docker(args, exit_code=-15)
        self.assertEqual(result.returncode, 143)
        self.assertEqual(events, [["docker", args]])

    def test_routes_return_only_deduplicated_sanitized_fetch_tuples(self):
        result = self.helper.routes(FETCHES)
        self.assertEqual(result, {("mirror.gcr.io", "library/python", "manifests"),
                                  ("mirror.gcr.io", "library/python", "blobs")})
        self.assertIsInstance(result, set)
        for secret in ("header-secret", "query-secret", "blob-secret", "Bearer", "sha256"):
            self.assertNotIn(secret, repr(result))

    def test_routes_distinguish_successful_origin_fallback_from_mirror_fetches(self):
        self.assertEqual(self.helper.routes(FETCHES + FALLBACK), {
            ("mirror.gcr.io", "library/python", "manifests"),
            ("mirror.gcr.io", "library/python", "blobs"),
            ("registry-1.docker.io", "kindest/node", "manifests"),
        })

    def test_routes_ignore_requests_failures_caches_and_auth_payloads(self):
        self.assertEqual(self.helper.routes(NOT_FETCHES), set())
        self.assertEqual(self.helper.routes(""), set())

    def test_hosts_retains_origin_server_and_configures_pull_mirror(self):
        try:
            import tomllib
        except ImportError:  # Python 3.9/3.10: no third-party TOML dependency.
            self.assertRegex(self.helper.HOSTS,
                             r'(?m)^\s*server\s*=\s*"https://registry-1\.docker\.io"\s*$')
            self.assertIn('[host."https://mirror.gcr.io"]', self.helper.HOSTS)
            self.assertRegex(self.helper.HOSTS, r'capabilities\s*=\s*\[[^\]]*"pull"')
        else:
            hosts = tomllib.loads(self.helper.HOSTS)
            self.assertEqual(hosts["server"], "https://registry-1.docker.io")
            self.assertIn("pull", hosts["host"]["https://mirror.gcr.io"]["capabilities"])


if __name__ == "__main__":
    unittest.main()
