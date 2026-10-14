#!/usr/bin/env python3
"""Exercise the actual offline CI rendering helper, not live placement."""
import base64
import json
import importlib.util
import pathlib
import subprocess
import unittest
from unittest import mock
import tempfile
import yaml

ROOT = pathlib.Path(__file__).resolve().parents[1]
HELPER = ROOT / "scripts/ci/orka-native-reference.go"


class ReferenceTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.temporary = tempfile.TemporaryDirectory()
        cls.binary = pathlib.Path(cls.temporary.name) / "orka-native-reference"
        subprocess.run(["go", "-C", str(ROOT), "build", "-o", str(cls.binary), str(HELPER)], check=True, capture_output=True, timeout=60)

    @classmethod
    def tearDownClass(cls):
        cls.temporary.cleanup()

    def call(self, raw):
        # The runtime receives no compiler, module cache or ambient environment.
        return subprocess.run([str(self.binary)], input=raw, env={},
                              capture_output=True, timeout=10)

    def test_exact_native_documents_without_task_or_secret_value(self):
        spec = {"Name": "ci-native-suite", "Namespace": "orka-system", "Model": "qwen-3.5-2b",
                "ProviderType": "openai", "BaseURL": "http://model.example:8080/v1", "SecretName": "fixture-key",
                "SecretKey": "api-key", "Instructions": "Private fixture instructions.", "Description": "Fixture"}
        result = self.call(json.dumps(spec).encode())
        self.assertEqual(result.returncode, 0, "native reference helper failed")
        documents = [base64.b64decode(doc, validate=True) for doc in json.loads(result.stdout)]
        parsed = [yaml.safe_load(doc) for doc in documents]
        self.assertEqual([doc["kind"] for doc in parsed], ["Secret", "Provider", "Agent"])
        self.assertNotIn("data", parsed[0])
        self.assertNotIn("stringData", parsed[0])
        self.assertEqual(parsed[1]["spec"]["secretRef"], {"name": "fixture-key", "key": "api-key"})
        self.assertEqual(parsed[2]["spec"]["systemPrompt"]["inline"], spec["Instructions"])
        self.assertEqual(self.call(json.dumps(spec).encode()).stdout, result.stdout)

    def test_promotion_subject_uses_offline_reference_not_removed_cli(self):
        spec = {"Name": "ci-native-suite", "Namespace": "orka-system", "Model": "qwen-3.5-2b",
                "ProviderType": "openai", "BaseURL": "http://source.example:8080/v1", "SecretName": "fixture-key",
                "SecretKey": "api-key", "Instructions": "Private fixture instructions.", "Description": "Fixture"}
        result = self.call(json.dumps(spec).encode())
        self.assertEqual(result.returncode, 0, "reference setup failed")
        documents = [yaml.safe_load(base64.b64decode(doc)) for doc in json.loads(result.stdout)]
        pair = {doc["kind"]: doc for doc in documents[1:]}
        loader = importlib.util.spec_from_file_location("native", ROOT / "scripts/ci/suite-native.py")
        module = importlib.util.module_from_spec(loader)
        loader.loader.exec_module(module)
        with tempfile.TemporaryDirectory() as directory:
            q = module.Qualification.__new__(module.Qualification)
            q.repo, q.work, q.context = ROOT, pathlib.Path(directory), "kind-destination"
            q.audit = type("Audit", (), {"SECRET_METADATA": "metadata-only"})
            q.frames = lambda: []
            q.get_namespace_uid = lambda name: "namespace-uid"
            q.cli = lambda *a, **k: self.fail("removed CLI renderer invoked")
            q.live_subject = lambda *a, **k: None
            metadata = {"name": "native-suite-inference", "namespace": "orka-system", "uid": "provider-uid", "generation": 1, "resourceVersion": "10"}
            bound = {"metadata": metadata, "spec": {"type": "openai", "baseURL": "http://destination.example:8080/v1", "secretRef": {"name": "fixture-key", "key": "api-key"}}}
            q.get = lambda kind, name: bound if kind == "providers.core.orka.ai" else {}
            def kube(*args):
                return json.dumps({"Metadata": {"Name": "fixture-key", "UID": "secret-uid", "ResourceVersion": "11"}, "Terminating": False}).encode() if "metadata-only" in args else b'{"metadata":{"uid":"cluster-uid"}}'
            q.kube = kube
            def command(args, raw=None, **kw):
                self.assertEqual([str(a) for a in args], [str(ROOT / "bin/orka-native-reference")])
                return self.call(raw)
            q.command = command
            with mock.patch.object(module, "observed_runtime", return_value={"profile": "fixture"}):
                subject, specs = q.promotion_subject({"subject": {"Member": spec["Name"], "Model": spec["Model"], "RequiredImages": []}}, pair)
            self.assertEqual(specs["Agent"], pair["Agent"]["spec"])
            self.assertEqual(specs["Provider"]["baseURL"], bound["spec"]["baseURL"])
            self.assertEqual(len(subject["PayloadDigest"]), 64)

    def test_collision_control_uses_validated_native_scaffold(self):
        loader = importlib.util.spec_from_file_location("native_collision", ROOT / "scripts/ci/suite-native.py")
        module = importlib.util.module_from_spec(loader)
        loader.loader.exec_module(module)
        class AuthorshipReached(Exception):
            pass
        with tempfile.TemporaryDirectory() as directory:
            q = module.Qualification.__new__(module.Qualification)
            q.repo, q.work = ROOT, pathlib.Path(directory)
            q.secret_ref = {"name": "native-suite-key", "key": "api-key"}
            instructions = q.work / "instructions.txt"
            instructions.write_text("Answer briefly.")
            writes = []
            q.kube = lambda *args, raw=None: writes.append(json.loads(raw))
            q.command = lambda args, raw=None, **kw: self.call(raw)
            def author(*args):
                raise AuthorshipReached()
            q.author = author
            with self.assertRaises(AuthorshipReached):
                q.refusals(q.work / "workspace", instructions, {"one.yaml": b""}, {"Member": "source"})
            self.assertEqual([doc["kind"] for doc in writes], ["Provider", "Agent"])
            provider, agent = writes
            self.assertEqual(provider["spec"]["secretRef"], q.secret_ref)
            self.assertEqual(provider["spec"]["defaultModel"], "qwen-3.5-2b")
            self.assertEqual(agent["spec"]["systemPrompt"]["inline"], instructions.read_text())
            self.assertNotIn("model", agent["spec"])
            self.assertNotIn("instructions", agent["spec"])
            self.assertNotIn("annotations", agent["metadata"])

    def test_invalid_or_unknown_input_refuses_without_documents(self):
        for raw in (b'{"unknown":true}', b'{} {}', b'{}', b'{"TaskPrompt":"send a Task"}'):
            with self.subTest(raw=raw):
                result = self.call(raw)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(result.stdout, b"")


if __name__ == "__main__":
    unittest.main()
