#!/usr/bin/env python3
"""Pin retained CI qualification and the deliberate removed-product boundary."""
import copy
import json
import os
import pathlib
import subprocess
import sys
import tempfile
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

    def test_terminal_and_state_gates_require_retained_tests_without_skips(self):
        terminal = next(step["run"] for step in self.jobs["hygiene"]["steps"]
                        if step.get("name") == "agent-create wizard — terminal lifecycle")
        suiteview = next(step["run"] for step in self.jobs["state-paths-macos"]["steps"]
                         if step.get("name") == "Native macOS suite view safety")
        state = next(step["run"] for step in self.jobs["state-paths-macos"]["steps"]
                     if step.get("name") == "Native macOS state paths honor KMX_HOME")
        self.assertIn("go test ./pkg/suiteview ./internal/kmx/agentsuite/localartifact", suiteview)
        self.assertIn("suiteview-macos.out", suiteview)
        self.assertIn("--- PASS:", suiteview)
        self.assertIn("if grep -q -- '--- SKIP:' suiteview-macos.out; then", suiteview)
        groups = [(terminal, "! grep -q -- '--- SKIP:'", ["TestQuickstartWizardPTYCtrlCRestoresTerminal", "TestAgentTUIDemoPTYCommandsAndTerminalRestore",
                              "TestCreateSuiteMemberTerminalConfirmation", "TestTimelinePTYEditsAndCancelsRunningResponse",
                              "TestOrkaGuardPromptCancellationAndInputOwnership", "TestQuickstartCancellationAcrossScreens",
                              "TestQuickstartNormalCompletionIsNotCancellation"]),
                  (state, "if grep -q -- '--- SKIP:'", ["TestQuickstartWizardSuiteAuthorsOnlyPrivateSource", "TestAgentTUIStartupPrefersRememberedRemoteAndHonorsFlags",
                           "TestStateDirUsesPortableOverrideAndNativeDefault", "TestContextResolutionOrder"])]
        sources = "\n".join(path.read_text() for folder in (ROOT / "internal/kmx/app", ROOT / "internal/kmx/config")
                            for path in folder.glob("*test.go"))
        for body, skip_guard, names in groups:
            self.assertIn("--- PASS:", body)
            self.assertIn(skip_guard, body)
            for name in names:
                self.assertGreaterEqual(body.count(name), 2, name + " must be selected and required to pass")
                self.assertIn("func " + name + "(", sources)
        self.assertNotIn("TestCreateWizardPTY", terminal)
        self.assertNotIn("TestBundleLiftSelectionPathUsesKMXHome", state)
        self.assertNotIn("TestEvaluateUsesRememberedTarget", state)

    def test_raw_fixture_shards_build_the_shared_renderer(self):
        for job in ("e2e-orka-runtime", "e2e-orka-helm"):
            body = "\n".join(step.get("run", "") for step in self.jobs[job]["steps"])
            self.assertIn("go build -o bin/orka-native-reference ./scripts/ci/orka-native-reference.go", body)


    def test_real_clonefree_hygiene_requires_authored_exactness_absence_and_promotion(self):
        program = next(step["run"] for step in self.jobs["hygiene"]["steps"]
                       if step.get("name", "").startswith("The clone-free journey sets up"))
        program = program.split("<<'EOF'\n", 1)[1].rsplit("\nEOF", 1)[0]
        def check(workflow):
            with tempfile.TemporaryDirectory() as directory:
                root = pathlib.Path(directory)
                (root / ".github/workflows").mkdir(parents=True)
                (root / ".github/workflows/ci.yml").write_text(yaml.safe_dump(workflow))
                return subprocess.run([sys.executable, "-c", program], cwd=root, capture_output=True, timeout=10)
        workflow = {"jobs": self.jobs}
        self.assertEqual(check(workflow).returncode, 0)
        clone = self.jobs["kmx-clone-free"]["steps"]
        index = next(i for i, s in enumerate(clone) if s.get("name", "").startswith("create — native Orka"))
        for fragment in ("(?s)^Orka says hello[.]\\n*$", 'receipt["candidateCleanup"] == "complete"',
                         '--ignore-not-found', "--promote"):
            changed = copy.deepcopy(workflow)
            body = changed["jobs"]["kmx-clone-free"]["steps"][index]["run"]
            self.assertIn(fragment, body, "journey must prove " + fragment)
            changed["jobs"]["kmx-clone-free"]["steps"][index]["run"] = body.replace(fragment, "REMOVED_PROOF")
            result = check(changed)
            self.assertNotEqual(result.returncode, 0, "embedded hygiene failed to enforce " + fragment)
        changed = copy.deepcopy(workflow)
        changed["jobs"]["kmx-clone-free"]["steps"][index]["run"] = clone[index]["run"].replace("--promote", "") + "\nfalse --promote\n"
        self.assertNotEqual(check(changed).returncode, 0, "promotion flag must be attached to Deploy, not merely mentioned")

    def test_clonefree_authored_regex_preserves_only_trailing_lf_bash_semantics(self):
        body = next(s["run"] for s in self.jobs["kmx-clone-free"]["steps"] if s.get("name", "").startswith("create — native Orka"))
        pattern = "(?s)^Orka says hello[.]\\n*$"
        self.assertIn(pattern, body, "exact answer must be authored inside evaluation")
        import re
        for answer, passes in (("Orka says hello.", True), ("Orka says hello.\n\n", True),
                               ("prefix Orka says hello.", False), ("Orka says hello.\r\n", False),
                               ("Orka says hello. extra", False)):
            self.assertEqual(re.search(pattern, answer) is not None, passes)


    def test_actual_clonefree_shell_refuses_incomplete_cleanup_or_surviving_task_before_push(self):
        body = next(s["run"] for s in self.jobs["kmx-clone-free"]["steps"] if s.get("name", "").startswith("create — native Orka"))
        fake = '''#!/usr/bin/env python3
import json, os, pathlib, sys
args = sys.argv[1:]
with open("calls.jsonl", "a") as stream:
    stream.write(json.dumps([pathlib.Path(sys.argv[0]).name, *args]) + "\\n")
if pathlib.Path(sys.argv[0]).name == "kubectl":
    print(os.environ.get("SURVIVING", ""), end="")
elif args[0] == "create":
    pathlib.Path("workspace/eval").mkdir()
elif args[0] == "test":
    pathlib.Path("evidence/passing.json").write_text(json.dumps({
        "candidateCleanup": os.environ.get("CLEANUP", "complete"), "result": "pass",
        "cases": [{"taskName": "candidate-task", "assertions": [{"verdict": "pass"}]}]}))
elif args[0] == "push":
    pathlib.Path("layout").mkdir()
    pathlib.Path("layout/index.json").write_text('{"manifests":[{"digest":"sha256:fixture"}]}')
'''
        for cleanup, survivor, success in (("complete", "", True), ("pending", "", False), ("complete", "tasks.core.orka.ai/candidate-task", False)):
            with self.subTest(cleanup=cleanup, survivor=survivor), tempfile.TemporaryDirectory() as directory:
                root = pathlib.Path(directory)
                (root / "bin").mkdir()
                for name in ("kubectl", "kmx"):
                    path = root / "bin" / name
                    path.write_text(fake.replace("#!/usr/bin/env python3", "#!" + sys.executable, 1))
                    path.chmod(0o700)
                # Real Bash/Python/YAML, only the external product/API are fixtures.
                result = subprocess.run(["bash", "-e", "-o", "pipefail", "-c", body], cwd=root, capture_output=True,
                                        env={**os.environ, "PATH": str(root / "bin") + os.pathsep + os.environ["PATH"],
                                             "CLEANUP": cleanup, "SURVIVING": survivor}, timeout=10)
                self.assertEqual(result.returncode == 0, success, result.stderr)
                authored = yaml.safe_load((root / "workspace/eval/example.yaml").read_bytes())
                self.assertEqual(authored["assertions"], [{"id": "exact-answer", "type": "regex", "pattern": "(?s)^Orka says hello[.]\\n*$"}])
                calls = [json.loads(line) for line in (root / "calls.jsonl").read_text().splitlines()]
                lifted = [c for c in calls if c[:2] == ["kmx", "deploy"]]
                pushed = [c for c in calls if c[:2] == ["kmx", "push"]]
                self.assertEqual(bool(lifted), success)
                self.assertEqual(bool(pushed), success)
                if success:
                    self.assertIn("--promote", lifted[0])
                    self.assertEqual(calls[2], ["kubectl", "--context", "kind-kmx-clone-free", "-n", "orka-system", "get",
                                               "tasks.core.orka.ai", "candidate-task", "--ignore-not-found", "-o", "name"])

    def test_quickstart_ceiling_stays_315(self):
        body = "\n".join(s.get("run", "") for s in self.jobs["e2e-quickstart"]["steps"])
        self.assertIn("315", body)
        self.assertIn("resourceVersion", body)


if __name__ == "__main__":
    unittest.main()
