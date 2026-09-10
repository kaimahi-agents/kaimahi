"""Keyless checks of the spike's native-Task and version preflight."""
import copy
import unittest

from check import check_crds, check_task


class PreflightTest(unittest.TestCase):
    def setUp(self):
        self.agent = {"metadata": {"name": "reader", "namespace": "test"}}
        self.task = {"apiVersion": "core.orka.ai/v1alpha1", "kind": "Task",
                     "metadata": {"name": "run", "namespace": "test"},
                     "spec": {"type": "ai", "agentRef": {"name": "reader"},
                              "prompt": "Read it", "retryPolicy": {"maxRetries": 0}}}

    def test_native_task(self):
        check_task(self.task, self.agent)

    def test_task_cannot_expand_or_override(self):
        for key, value in [("ai", {"tools": ["bash"]}), ("env", []),
                           ("agentRuntime", {}), ("execution", {}),
                           ("sessionRef", {}), ("skills", []), ("surprise", None)]:
            with self.subTest(key=key):
                task = copy.deepcopy(self.task)
                task["spec"][key] = value
                with self.assertRaisesRegex(ValueError, "spec." + key):
                    check_task(task, self.agent)

    def test_namespace_and_agent_are_pinned(self):
        for changes in [{"name": "other"}, {"name": "reader", "namespace": "other"}]:
            task = copy.deepcopy(self.task)
            task["spec"]["agentRef"] = changes
            with self.assertRaises(ValueError):
                check_task(task, self.agent)

    def test_no_task_retries_for_potentially_consequential_tools(self):
        self.task["spec"]["retryPolicy"]["maxRetries"] = 1
        with self.assertRaises(ValueError):
            check_task(self.task, self.agent)

    def test_schema_drift_refused(self):
        schema = {"openAPIV3Schema": {"type": "object"}}
        crd = {"metadata": {"name": "agents.core.orka.ai"}, "spec": {"versions": [
            {"name": "v1alpha1", "served": True, "schema": schema}]}}
        import hashlib
        import json
        digest = hashlib.sha256(json.dumps(schema, sort_keys=True, separators=(",", ":")).encode()).hexdigest()
        pins = {"agents.core.orka.ai": digest}
        check_crds({"items": [crd]}, pins)
        crd["spec"]["versions"][0]["schema"]["openAPIV3Schema"]["properties"] = {"rateLimit": {}}
        with self.assertRaisesRegex(ValueError, "schema mismatch"):
            check_crds({"items": [crd]}, pins)
        with self.assertRaisesRegex(ValueError, "missing CRD"):
            check_crds({"items": []}, pins)


if __name__ == "__main__":
    unittest.main()
