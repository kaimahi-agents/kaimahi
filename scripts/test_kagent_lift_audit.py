#!/usr/bin/env python3
"""Bounded kubectl invocation, metadata privacy and fail-closed wrapper tests."""
import importlib.util
import json
import os
import pathlib
import subprocess
import tempfile
import unittest

SOURCE = pathlib.Path(__file__).resolve().parent / "ci/kagent-lift-audit.py"
DOCUMENT = b'{"apiVersion":"kagent.dev/v1alpha2","kind":"Agent","metadata":{"name":"lift-proof","namespace":"agents","annotations":{"secret":"private"}},"spec":{"instructions":"private"}}'
METADATA = {"apiVersion": "kagent.dev/v1alpha2", "kind": "Agent", "name": "lift-proof", "namespace": "agents"}


class AuditTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        spec = importlib.util.spec_from_file_location("audit", SOURCE)
        cls.audit = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(cls.audit)

    def test_leading_options_and_readonly_subcommands_are_not_word_searches(self):
        for args in (["--context", "create", "get", "secret", "delete"],
                     ["--namespace=agents", "auth", "can-i", "create", "agents", "--as=controller"],
                     ["auth", "can-i", "delete", "agents"], ["config", "view", "-o", "json"],
                     ["rollout", "status", "deployment/lift-proof", "--timeout=10s"],
                     ["version", "--client"], ["get", "--raw", "/version"]):
            with self.subTest(args=args):
                self.assertIsNone(self.audit.classify(args, b""))

    def test_only_bound_a2a_service_proxy_post_is_not_object_create(self):
        route = "/api/v1/namespaces/kagent/services/http:kagent-controller:8083/proxy/api/a2a/agents/lift-proof"
        body = b'{"jsonrpc":"2.0","method":"message/send","params":{"private":"payload"}}'
        self.assertIsNone(self.audit.classify(["create", "--raw", route.replace("lift-proof", "suite-eval-proof"), "-f", "-"], body))
        with tempfile.TemporaryDirectory() as temp:
            work = pathlib.Path(temp)
            real = work / "real"
            real.write_text('#!/usr/bin/env python3\nimport pathlib,sys\npathlib.Path(' + repr(str(work / "bytes")) + ').write_bytes(sys.stdin.buffer.read())\n')
            real.chmod(0o700)
            audit = work / "frames"
            argv = ["--context", "kind-kmx-kagent-lift", "--request-timeout=20s", "create", "--raw", route, "-f", "-"]
            result = subprocess.run(["python3", str(SOURCE), str(real), str(audit), *argv], input=body, capture_output=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual((work / "bytes").read_bytes(), body)
            self.assertFalse(audit.exists())
            for changed in (route.replace("lift-proof", "other"), route + "?dryRun=All", "/api/v1/namespaces/agents/secrets"):
                with self.subTest(route=changed), self.assertRaisesRegex(ValueError, "unsupported kubectl invocation"):
                    self.audit.classify(["create", "--raw", changed, "-f", "-"], body)
            for extra in (["--dry-run=server"], ["-o", "json"], ["agent"]):
                with self.subTest(extra=extra), self.assertRaisesRegex(ValueError, "unsupported kubectl invocation"):
                    self.audit.classify(["create", "--raw", route, "-f", "-", *extra], body)

    def test_effective_dryrun_final_duplicate_wins_and_metadata_only(self):
        for flags, strategy in (([], "none"), (["--dry-run=none"], "none"),
                                (["--dry-run=server"], "server"), (["--dry-run=client"], "client"),
                                (["--dry-run"], "client"),
                                (["--dry-run=server", "--dry-run=none"], "none"),
                                (["--dry-run=none", "--dry-run=server"], "server")):
            with self.subTest(flags=flags):
                frame = self.audit.classify(["-n", "agents", "create", *flags, "-f", "-"], DOCUMENT)
                self.assertEqual(frame, {"verb": "create", "dry_run": strategy, "resources": [METADATA]})
                self.assertNotIn("private", json.dumps(frame))
        frame = self.audit.classify(["-n", "agents", "patch", "agents.kagent.dev", "lift-proof", "--type=merge", "-p", '{"private":"patch"}'], b"")
        self.assertEqual(frame, {"verb": "patch", "dry_run": "none", "resources": [METADATA]})

    def test_unsupported_forms_unknown_options_and_malformed_stdin_refuse(self):
        cases = [(["--unknown", "create", "-f", "-"], DOCUMENT),
                 (["--context"], DOCUMENT), (["rollout", "restart", "deployment/x"], b""),
                 (["config", "set-context", "x"], b""), (["delete", "agents", "x"], b""),
                 (["mystery", "create"], b""), (["create", "--raw", "/private", "-f", "-"], DOCUMENT),
                 (["create", "--dry-run", "client", "-f", "-"], DOCUMENT),
                 (["create", "--dry-run=garbage", "-f", "-"], DOCUMENT),
                 (["create", "--dry-run-extra=server", "-f", "-"], DOCUMENT),
                 (["create", "-f", "private.yaml"], DOCUMENT), (["create", "-f", "-"], b"private"),
                 (["create", "-f", "-"], DOCUMENT + b"\n---\n" + DOCUMENT),
                 (["create", "-f", "-"], b"x" * (1024 * 1024 + 1))]
        for args, data in cases:
            with self.subTest(args=args), self.assertRaisesRegex(ValueError, "unsupported kubectl invocation") as error:
                self.audit.classify(args, data)
            self.assertNotIn("private", str(error.exception))

    def test_only_exact_owned_loopback_forwards_are_readonly(self):
        fixture = ["--context", "kind-kmx-kagent-lift", "-n", "agents", "port-forward",
                   "--address", "127.0.0.1", "service/lift-fixture", ":9000"]
        controller = ["--context", "kind-kmx-kagent-lift", "-n", "kagent", "port-forward",
                      "--address", "127.0.0.1", "service/kagent-controller", ":8083"]
        for args in (fixture, controller):
            with self.subTest(args=args):
                self.assertIsNone(self.audit.classify(args, b""))
        for index, value in ((1, "other-context"), (3, "kagent"), (6, "0.0.0.0"),
                             (7, "service/other"), (7, "pod/lift-fixture"), (8, "9000:9000"), (8, ":8083")):
            args = fixture.copy()
            args[index] = value
            with self.subTest(args=args), self.assertRaisesRegex(ValueError, "unsupported kubectl invocation"):
                self.audit.classify(args, b"")
        for args in (fixture + ["--pod-running-timeout=30s"], fixture + ["--address", "0.0.0.0"],
                     fixture + ["--context", "other-context"]):
            with self.subTest(args=args), self.assertRaisesRegex(ValueError, "unsupported kubectl invocation"):
                self.audit.classify(args, b"")

    def test_repeated_filename_aliases_refuse_before_executable_and_record_rejection(self):
        with tempfile.TemporaryDirectory() as temp:
            work = pathlib.Path(temp)
            real = work / "real"
            real.write_text('#!/usr/bin/env python3\nimport pathlib\npathlib.Path(' + repr(str(work / "forwarded")) + ').touch()\n')
            real.chmod(0o700)
            audit = work / "frames"
            for first in ("-f", "--filename"):
                for second in ("-f", "--filename"):
                    for values in (("private.yaml", "-"), ("-", "private.yaml")):
                        for equal in (False, True):
                            flags = ([first + "=" + values[0], second + "=" + values[1]] if equal else
                                     [first, values[0], second, values[1]])
                            with self.subTest(flags=flags):
                                audit.unlink(missing_ok=True)
                                (work / "forwarded").unlink(missing_ok=True)
                                result = subprocess.run(["python3", str(SOURCE), str(real), str(audit), "create", *flags],
                                                        input=DOCUMENT, capture_output=True)
                                self.assertEqual(result.returncode, 1)
                                self.assertFalse((work / "forwarded").exists(), "repeated filenames were forwarded")
                                self.assertEqual(audit.read_text(), '{"rejected": "unsupported-kubectl-invocation"}\n')
                                self.assertEqual(result.stderr, b"unsupported kubectl invocation\n")
                                self.assertEqual(result.stdout, b"")
                                with self.assertRaisesRegex(ValueError, "unsupported kubectl invocation"):
                                    self.audit.classify(["create", *flags], DOCUMENT)

    def test_wrapper_records_before_failed_forward_preserves_bytes_and_closed_rejection(self):
        with tempfile.TemporaryDirectory() as temp:
            work = pathlib.Path(temp)
            real = work / "real"
            real.write_text('#!/usr/bin/env python3\nimport os,pathlib,sys\nassert pathlib.Path(os.environ["AUDIT"]).read_text()\npathlib.Path(os.environ["BYTES"]).write_bytes(sys.stdin.buffer.read())\nraise SystemExit(29)\n')
            real.chmod(0o700)
            audit = work / "frames"
            env = dict(os.environ, AUDIT=str(audit), BYTES=str(work / "bytes"))
            result = subprocess.run(["python3", str(SOURCE), str(real), str(audit), "create", "-f", "-"], input=DOCUMENT, capture_output=True, env=env)
            self.assertEqual(result.returncode, 29)
            self.assertEqual((work / "bytes").read_bytes(), DOCUMENT)
            result = subprocess.run(["python3", str(SOURCE), str(real), str(audit), "--private", "delete", "x"], input=b"private", capture_output=True, env=env)
            self.assertEqual(result.returncode, 1)
            self.assertNotIn(b"private", result.stdout + result.stderr)
            self.assertEqual(json.loads(audit.read_text().splitlines()[-1]), {"rejected": "unsupported-kubectl-invocation"})
            self.assertNotIn("private", audit.read_text())
            for data in (b'apiVersion: kagent.dev/v1alpha2\nkind: Agent\nmetadata: &self {name: private, namespace: *self}',
                         b'apiVersion: kagent.dev/v1alpha2\nkind: Agent\nmetadata: {name: private, namespace: agents}\nspec: ' + b'[' * 2000 + b'private' + b']' * 2000):
                with self.subTest(shape=len(data)):
                    count = len(audit.read_text().splitlines())
                    result = subprocess.run(["python3", str(SOURCE), str(real), str(audit), "create", "-f", "-"], input=data, capture_output=True, env=env)
                    self.assertEqual(result.returncode, 1)
                    self.assertNotIn(b"private", result.stdout + result.stderr)
                    self.assertEqual(len(audit.read_text().splitlines()), count + 1)
                    self.assertEqual(json.loads(audit.read_text().splitlines()[-1]), {"rejected": "unsupported-kubectl-invocation"})


if __name__ == "__main__":
    unittest.main()
