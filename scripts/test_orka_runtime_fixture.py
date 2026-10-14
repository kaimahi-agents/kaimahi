#!/usr/bin/env python3
"""Validate actual scaffold-backed smoke fixtures; not live qualification."""
import base64
import importlib.util
import json
import pathlib
import subprocess
import tempfile
import unittest

import yaml

ROOT = pathlib.Path(__file__).resolve().parents[1]
PATH = ROOT / "scripts/ci/orka-runtime-fixture.py"


class FixtureTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.temporary = tempfile.TemporaryDirectory()
        cls.renderer = pathlib.Path(cls.temporary.name) / "orka-native-reference"
        subprocess.run(["go", "-C", str(ROOT), "build", "-o", str(cls.renderer),
                        str(ROOT / "scripts/ci/orka-native-reference.go")], check=True, capture_output=True, timeout=60)

    @classmethod
    def tearDownClass(cls):
        cls.temporary.cleanup()

    def load(self):
        spec = importlib.util.spec_from_file_location("orka_fixture", PATH)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        module.RENDERER = self.renderer
        return module

    def test_model_and_tool_fixture_use_validated_native_scaffold(self):
        module = self.load()
        for mode, name, model in [("hello", "orka-hello", "qwen2.5:3b"), ("tool", "orka-tool-proof", "tool-fixture")]:
            with self.subTest(mode=mode):
                provider, agent = module.objects(mode)
                self.assertEqual([provider["kind"], agent["kind"]], ["Provider", "Agent"])
                self.assertEqual(provider["spec"]["defaultModel"], model)
                self.assertEqual(agent["metadata"]["name"], name)
                self.assertNotIn("model", agent["spec"])  # Native scaffold inherits the bound Provider model.
                self.assertNotIn("instructions", agent["spec"])
                self.assertTrue(agent["spec"]["systemPrompt"]["inline"])
                self.assertEqual(agent["spec"]["providerRef"]["name"], name)
                self.assertEqual(provider["spec"]["secretRef"], {"name": "local-provider-key", "key": "api-key"})
                self.assertEqual(agent["spec"].get("tools", []), [{"name": "k8s-get-resources"}] if mode == "tool" else [])
                self.assertNotIn("annotations", agent["metadata"])
                self.assertNotIn("Task", json.dumps([provider, agent]))

    def test_hello_preserves_green_main_scaffold_default(self):
        # Main's create --live hello smoke supplied no instructions, tools or
        # description. Compare against that same native scaffold input rather
        # than inventing a stronger prompt to make an answer assertion pass.
        module = self.load()
        reference = {"Name": "orka-hello", "Namespace": "orka-system", "Model": "qwen2.5:3b",
                     "ProviderType": "openai", "BaseURL": "http://ollama.ollama.svc.cluster.local:11434/v1",
                     "SecretName": "local-provider-key", "SecretKey": "api-key"}
        result = subprocess.run([str(self.renderer)], input=json.dumps(reference).encode(),
                                capture_output=True, timeout=60)
        self.assertEqual(result.returncode, 0)
        baseline = [yaml.safe_load(base64.b64decode(raw)) for raw in json.loads(result.stdout)]
        self.assertEqual(module.objects("hello"), tuple(baseline[1:]))

    def test_occupied_fixture_refuses_before_any_create(self):
        module = self.load()
        calls = []
        def kube(*args, raw=None):
            calls.append((args, raw))
            return b"occupied"
        with self.assertRaisesRegex(ValueError, "fixture already exists"):
            module.prepare("hello", kube)
        self.assertTrue(all("create" not in args for args, _ in calls))

    def test_preflight_both_then_single_ordered_create(self):
        module = self.load()
        calls = []
        def kube(*args, raw=None):
            calls.append((args, raw))
            return b""
        module.prepare("tool", kube)
        writes = [(args, json.loads(raw)) for args, raw in calls if raw is not None]
        self.assertEqual([doc["kind"] for _, doc in writes], ["Provider", "Agent"])
        self.assertEqual(sum("get" in args for args, _ in calls), 2)
        self.assertTrue(all(args[:3] == ("create", "-f", "-") for args, _ in writes))
        self.assertEqual([calls.index((args, raw)) for args, raw in calls if raw is not None], [2, 4])

    def test_invalid_tool_refuses_before_kubernetes(self):
        result = subprocess.run([str(self.renderer)], input=json.dumps({
            "Name": "fixture", "Namespace": "orka-system", "Model": "fixture", "ProviderType": "openai",
            "BaseURL": "http://fixture.example/v1", "SecretName": "local-provider-key", "Tools": ["invalid tool"]
        }).encode(), capture_output=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(result.stdout, b"")


if __name__ == "__main__":
    unittest.main()
