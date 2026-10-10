#!/usr/bin/env python3
"""Exercise the native Orka Tool fixture's actual HTTP health responses."""
import importlib.util
import json
import os
import pathlib
import secrets
import threading
import unittest
import unittest.mock
import urllib.error
import urllib.request
from http.server import HTTPServer


class OrkaHealthToolFixtureTests(unittest.TestCase):
    def setUp(self):
        self.marker = "health-" + secrets.token_hex(8)
        marker_env = unittest.mock.patch.dict(os.environ, {"ORKA_HEALTH_DEPLOYMENT_MARKER": self.marker})
        marker_env.start()
        self.addCleanup(marker_env.stop)
        source = pathlib.Path(__file__).parent / "ci" / "orka-tool-model.py"
        spec = importlib.util.spec_from_file_location("orka_health_fixture", source)
        fixture_module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(fixture_module)
        self.server = HTTPServer(("127.0.0.1", 0), fixture_module.Handler)
        worker = threading.Thread(target=self.server.serve_forever, daemon=True)
        worker.start()
        self.addCleanup(worker.join)
        self.addCleanup(self.server.server_close)
        self.addCleanup(self.server.shutdown)

    def post(self, inputs):
        request = urllib.request.Request(
            f"http://127.0.0.1:{self.server.server_port}/v1/responses",
            data=json.dumps({"model": "tool-fixture", "input": inputs}).encode(),
            headers={"Content-Type": "application/json"},
        )
        with urllib.request.urlopen(request, timeout=3) as response:
            return json.load(response)

    def test_calls_only_read_only_deployment_health(self):
        call = self.post([])["output"][0]
        self.assertEqual(call["name"], "k8s-get-resources")
        self.assertEqual(json.loads(call["arguments"]),
                         {"resource": "deployments", "namespace": "orka-system"})
        payload = json.dumps({"resource": "deployments", "items": [
            {"name": self.marker, "namespace": "orka-system"},
        ]})
        result = self.post([{"type": "function_call_output", "output": "Tool result: " + payload}])
        self.assertEqual(result["status"], "completed")
        self.assertEqual(result["output"][0]["content"][0]["text"],
                         "Health inventory checked.")

    def test_rejects_missing_live_deployment(self):
        cases = (
            ("empty", []),
            ("unrelated", [{"name": "some-other-deployment", "namespace": "orka-system"}]),
            ("known_static", [{"name": "orka-tool-model", "namespace": "orka-system"}]),
            ("wrong_marker", [{"name": "health-other", "namespace": "orka-system"}]),
            ("wrong_namespace", [{"name": self.marker, "namespace": "other"}]),
        )
        for name, items in cases:
            with self.subTest(case=name):
                payload = json.dumps({"resource": "deployments", "items": items})
                with self.assertRaises(urllib.error.HTTPError) as caught:
                    self.post([{"type": "function_call_output", "output": "Tool result: " + payload}])
                self.assertEqual(caught.exception.code, 500)
                caught.exception.close()

    def test_rejects_unset_live_marker(self):
        with unittest.mock.patch.dict(os.environ, {"ORKA_HEALTH_DEPLOYMENT_MARKER": ""}):
            payload = json.dumps({"resource": "deployments", "items": [
                {"name": "orka-tool-model", "namespace": "orka-system"}]})
            with self.assertRaises(urllib.error.HTTPError) as caught:
                self.post([{"type": "function_call_output", "output": "Tool result: " + payload}])
            self.assertEqual(caught.exception.code, 500)
            caught.exception.close()

    def test_rejects_unexpected_tool_result(self):
        with self.assertRaises(urllib.error.HTTPError) as caught:
            self.post([{"type": "function_call_output",
                        "output": 'Tool result: {"resource":"secrets","items":[]}'}])
        self.assertEqual(caught.exception.code, 500)
        caught.exception.close()


if __name__ == "__main__":
    unittest.main()
