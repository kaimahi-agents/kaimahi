#!/usr/bin/env python3
"""Pin retained CI qualification and the deliberate removed-product boundary."""
import copy
import json
import os
import pathlib
import subprocess
import sys
import unittest
import yaml

ROOT = pathlib.Path(__file__).resolve().parents[1]


class RetirementCITests(unittest.TestCase):
    def setUp(self):
        self.jobs = yaml.safe_load((ROOT / ".github/workflows/ci.yml").read_text())["jobs"]

    def test_required_aggregator_retains_native_shards_not_removed_products(self):
        expected = {"classify-change", "e2e-kagent-lift", "e2e-orka-runtime", "e2e-orka-helm", "e2e-quickstart"}
        self.assertEqual(set(self.jobs["e2e-hello-world"]["needs"]), expected)
        self.assertNotIn("e2e-kagent-create", self.jobs)
        self.assertNotIn("e2e-eval-loop", self.jobs)
        check = "\n".join(s.get("run", "") for s in self.jobs["e2e-hello-world"]["steps"])
        self.assertIn('expected = {"e2e-kagent-lift", "e2e-orka-runtime", "e2e-orka-helm", "e2e-quickstart"}', check)
        self.assertIn('if result != "success" and not (result == "skipped"', check)

    def test_aggregator_executes_fail_closed(self):
        body = self.jobs["e2e-hello-world"]["steps"][0]["run"]
        program = body.split("<<'EOF'\n", 1)[1].rsplit("\nEOF", 1)[0]
        good = {name: {"result": "success"} for name in self.jobs["e2e-hello-world"]["needs"]}
        good["classify-change"]["outputs"] = {"kagent_changed": "true"}
        for failure in (None, "failure", "cancelled", "unknown", "skipped"):
            sample = copy.deepcopy(good)
            if failure:
                sample["e2e-orka-runtime"]["result"] = failure
            result = subprocess.run([sys.executable, "-c", program], capture_output=True,
                                    env={**os.environ, "SHARDS": json.dumps(sample), "EVENT": "pull_request"})
            self.assertEqual(result.returncode, 0 if failure is None else 1)

    def test_live_shards_do_not_invoke_removed_lifecycle(self):
        for name in ("e2e-kagent-lift", "e2e-orka-runtime", "e2e-orka-helm", "e2e-quickstart", "kmx-clone-free"):
            body = "\n".join(step.get("run", "") for step in self.jobs[name]["steps"])
            self.assertNotRegex(body, r"\bcreate\s+--live\b|\bbundle\s+(?:lift|status|evaluate|retire|verify)\b", name)
        runtime = "\n".join(s.get("run", "") for s in self.jobs["e2e-orka-runtime"]["steps"])
        self.assertIn("suite-native.py", runtime)
        self.assertIn("TestLiveInspectQuickstartToolEvents", runtime)
        self.assertIn("orka-runtime-fixture.py", runtime)
        self.assertIn("Orka says hello.", runtime)

    def test_quickstart_ceiling_stays_315(self):
        body = "\n".join(s.get("run", "") for s in self.jobs["e2e-quickstart"]["steps"])
        self.assertIn("315", body)
        self.assertIn("resourceVersion", body)


if __name__ == "__main__":
    unittest.main()
