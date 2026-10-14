#!/usr/bin/env python3
"""Hermetic CI fixture checks; not runtime/model qualification."""
import importlib.util
import json
import pathlib
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[1]
PATH = ROOT / "scripts/ci/orka-runtime-fixture.py"


class FixtureTests(unittest.TestCase):
    def load(self):
        self.assertTrue(PATH.exists(), "upstream runtime fixture helper is missing")
        spec = importlib.util.spec_from_file_location("orka_fixture", PATH)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        return module

    def test_model_and_tool_fixture_are_not_bundles(self):
        module = self.load()
        for mode, name, model in [("hello", "orka-hello", "qwen2.5:3b"), ("tool", "orka-tool-proof", "tool-fixture")]:
            with self.subTest(mode=mode):
                provider, agent = module.objects(mode)
                self.assertEqual([provider["kind"], agent["kind"]], ["Provider", "Agent"])
                self.assertEqual(provider["spec"]["defaultModel"], model)
                self.assertEqual(agent["metadata"]["name"], name)
                self.assertEqual(agent["spec"]["providerRef"]["name"], name)
                self.assertEqual(provider["spec"]["secretRef"], {"name": "local-provider-key", "key": "api-key"})
                self.assertEqual(agent["spec"].get("tools", []), [{"name": "k8s-get-resources", "enabled": True}] if mode == "tool" else [])
                self.assertNotIn("annotations", agent["metadata"])
                self.assertNotIn("Task", json.dumps([provider, agent]))

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


if __name__ == "__main__":
    unittest.main()
