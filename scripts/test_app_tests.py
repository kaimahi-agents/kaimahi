#!/usr/bin/env python3
"""Coverage and failure contracts for the app-test CI shards."""
import importlib.util
import json
import os
from pathlib import Path
import shlex
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import yaml

ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "scripts/ci/app-tests.py"
PACKAGE = "github.com/kaimahi-agents/kaimahi/internal/kmx/app"


class AppShardTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        if SCRIPT.exists():
            spec = importlib.util.spec_from_file_location("app_tests", SCRIPT)
            cls.runner = importlib.util.module_from_spec(spec)
            spec.loader.exec_module(cls.runner)

    def setUp(self):
        self.assertTrue(SCRIPT.exists(), "app shard runner must exist")

    def test_partition_is_stable_disjoint_exhaustive_and_keeps_seed_tests(self):
        names = [f"TestCase{i}" for i in range(100)] + ["ExampleRun", "FuzzDecode"]
        groups = [self.runner.partition(names, i, 4) for i in range(4)]
        self.assertEqual(sorted(sum(groups, [])), sorted(names))
        self.assertEqual(len(set(sum(groups, []))), len(names))
        self.assertTrue(all(groups))
        added = names + ["TestNew"]
        for i, group in enumerate(groups):
            self.assertEqual([n for n in self.runner.partition(added, i, 4) if n != "TestNew"], group)
        # Fixed vector catches Python hash randomization or a changed algorithm.
        self.assertEqual(self.runner.partition(["TestOne"], 3, 4), ["TestOne"])

    def test_invalid_or_empty_shard_refuses(self):
        for index, count in ((-1, 4), (4, 4), (0, 0)):
            with self.subTest(index=index, count=count), self.assertRaises(ValueError):
                self.runner.partition(["TestOne"], index, count)
        with self.assertRaises(ValueError):
            self.runner.partition([], 0, 4)

    def test_compiled_inventory_is_strict_and_includes_examples_and_fuzz(self):
        text = f"TestOne\nExampleRun\nFuzzDecode\nok  \t{PACKAGE}\t0.001s\n"
        self.assertEqual(self.runner.test_names(text), ["ExampleRun", "FuzzDecode", "TestOne"])
        for bad in ("", "TestOne\nTestOne\n", "not a test\n", "TestOne/subcase\n"):
            with self.subTest(text=bad), self.assertRaises(ValueError):
                self.runner.test_names(bad)

    def summarize(self, events, selected=("TestOne", "TestLive")):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "tests.json"
            path.write_text("\n".join(json.dumps(e) if isinstance(e, dict) else e for e in events))
            return self.runner.summarize(path, selected)

    def event(self, action, test=None):
        event = {"Package": PACKAGE, "Action": action, "Elapsed": 1.25}
        if test:
            event["Test"] = test
        return event

    def test_summary_accepts_mixed_skip_and_pass_and_omits_payloads(self):
        events = [self.event("pass", "TestOne/sub"), self.event("pass", "TestOne"),
                  self.event("skip", "TestLive"), self.event("pass")]
        events.insert(0, {**self.event("output", "TestOne"), "Output": "private canary"})
        result = self.summarize(events)
        self.assertEqual(result["package_seconds"], 1.25)
        self.assertEqual(len(result["tests"]), 2)
        self.assertNotIn("private canary", json.dumps(result))

    def test_summary_refuses_noop_missing_unexpected_failed_or_invalid_output(self):
        good = [self.event("pass", "TestOne"), self.event("skip", "TestLive"), self.event("pass")]
        cases = [good[:-1], good[1:], good + [self.event("pass", "TestUnexpected")],
                 good + [self.event("fail", "TestOne/sub")],
                 [self.event("skip", "TestOne"), *good[1:]],
                 [*good[:-1], self.event("fail")], [*good, "compile error"]]
        for events in cases:
            with self.subTest(events=events), self.assertRaises(ValueError):
                self.summarize(events)

    def test_discovery_failure_and_go_test_exit_are_preserved(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "app.json"
            with patch.object(self.runner.subprocess, "check_output", side_effect=subprocess.CalledProcessError(2, ["go"])):
                with self.assertRaises(subprocess.CalledProcessError):
                    self.runner.run(0, 1, output)
            with patch.object(self.runner.subprocess, "check_output", return_value=f"TestOne\nok\t{PACKAGE}\t0s\n"), \
                 patch.object(self.runner.subprocess, "run", return_value=subprocess.CompletedProcess([], 7)) as execute:
                self.assertNotEqual(self.runner.run(0, 1, output), 0)
                command = execute.call_args.args[0]
                self.assertIn("-timeout=600s", command)
                self.assertIn("-count=1", command)
                self.assertIn("^(TestOne)$", command)
                self.assertIn("-json", command)


class LocalAppShardTests(unittest.TestCase):
    def test_make_runs_all_four_isolated_shards_and_propagates_failure(self):
        for failed in (False, True):
            with self.subTest(failed=failed), tempfile.TemporaryDirectory() as directory:
                work = Path(directory)
                capture = work / "calls"
                python = work / "python3"
                python.write_text("#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$CAPTURE\"\n"
                                  "case \"$*\" in *'--shard 1 '*) exit \"$FAIL_SHARD\";; esac\n")
                python.chmod(0o755)
                result = subprocess.run(["make", "--no-print-directory", "test-app"], cwd=ROOT,
                                        env={**os.environ, "PATH": directory + os.pathsep + os.environ["PATH"],
                                             "TMPDIR": directory, "CAPTURE": str(capture),
                                             "FAIL_SHARD": "7" if failed else "0"}, capture_output=True, text=True)
                self.assertEqual(result.returncode == 0, not failed, result.stdout + result.stderr)
                calls = [shlex.split(line) for line in capture.read_text().splitlines()]
                self.assertEqual(len(calls), 4)
                outputs = []
                for index, call in enumerate(calls):
                    self.assertEqual(call[:5], ["scripts/ci/app-tests.py", "--shard", str(index), "--shards", "4"])
                    self.assertEqual(call[5], "--output")
                    output = Path(call[6])
                    self.assertEqual(output.parent.parent, work)
                    self.assertIn(str(output.parent), result.stdout)
                    outputs.append(output)
                self.assertEqual(len(set(outputs)), 4)


class AppShardWorkflowTests(unittest.TestCase):
    def setUp(self):
        self.jobs = yaml.safe_load((ROOT / ".github/workflows/ci.yml").read_text())["jobs"]

    def test_matrix_covers_all_four_shards_on_every_hygiene_event(self):
        self.assertIn("app-tests", self.jobs)
        job = self.jobs["app-tests"]
        self.assertNotIn("if", job)
        self.assertEqual(job["runs-on"], "${{ matrix.os }}")
        self.assertEqual(job["strategy"]["matrix"], {
            "os": ["ubuntu-latest", "macos-latest"], "shard": [0, 1, 2, 3]})
        self.assertIs(job["strategy"]["fail-fast"], False)
        body = "\n".join(s.get("run", "") for s in job["steps"])
        self.assertIn("python3 scripts/ci/app-tests.py --shard", body)
        self.assertIn("--shards 4", body)
        self.assertEqual(self.jobs["hygiene"]["needs"],
                         ["hygiene-checks", "app-tests", "windows-amd64-native", "windows-arm64-cross"])
        self.assertEqual(self.jobs["hygiene"]["if"], "always()")
        windows = self.jobs["windows-amd64-native"]
        windows_body = "\n".join(step.get("run", "") for step in windows["steps"])
        self.assertEqual(windows["runs-on"], "windows-latest")
        self.assertIn("ProcessArchitecture", windows_body)
        self.assertIn("X64", windows_body)
        self.assertIn("$ErrorActionPreference = 'Stop'", windows_body)
        self.assertIn("./scripts/ci/windows-tests", windows_body)
        self.assertIn("./internal/kmx/fileaccess", windows_body)
        self.assertIn("TestWindowsPrivateCreationUsesExplicitProtectedDescriptor", windows_body)
        self.assertIn("TestWindowsStateAccessUsesLocalAppDataAndSeparatesCreation", windows_body)
        self.assertIn("./internal/kmx/config", windows_body)
        self.assertIn("./internal/kmx/localpreview", windows_body)
        self.assertIn("./internal/kmx/app", windows_body)
        self.assertIn("go build ./...", windows_body)
        self.assertIn("go vet ./...", windows_body)
        self.assertIn("GOARCH: amd64", yaml.safe_dump(windows))
        self.assertIn("go build -trimpath -o \"$env:RUNNER_TEMP\\kmx.exe\" ./cmd/kmx", windows_body)
        self.assertIn('$metadataLines -join "`n"', windows_body)
        self.assertIn("windows-cli-day-zero.ps1", windows_body)
        self.assertNotIn("kmx-windows-tests.exe\" powershell", windows_body)
        self.assertIn("TestGuardedWindowsDeferredOperationsRefuseBeforeEffects", windows_body)
        self.assertIn("TestGuardedWindowsFileCommandsRefuseEveryExternalRouteBeforeEffects", windows_body)
        self.assertIn("TestGuardedWindowsExplainRequiresExplicitFileAndAllowsFileScopedClear", windows_body)
        self.assertIn("TestGuardedWindowsVersionHelpAndCompletionRemainInert", windows_body)
        self.assertIn("TestGuardedWindowsSupportedCommandFlagsAreExhaustivelyAllowlisted", windows_body)
        self.assertIn("$previewTests", windows_body)
        self.assertIn("$appTests", windows_body)
        self.assertIn("--- SKIP:", windows_body)
        self.assertEqual(windows_body.count("did not pass"), 5)
        cross = self.jobs["windows-arm64-cross"]
        cross_body = "\n".join(step.get("run", "") for step in cross["steps"])
        self.assertEqual(cross["runs-on"], "windows-latest")
        self.assertEqual(cross["name"], "Windows arm64 cross-build metadata only (not native execution)")
        self.assertIn("GOARCH: arm64", yaml.safe_dump(cross))
        self.assertIn("go build ./...", cross_body)
        self.assertIn("go vet ./...", cross_body)
        self.assertIn("$ErrorActionPreference = 'Stop'", cross_body)
        for binary in ("kmx-windows-arm64.exe", "cmd-kmx-arm64.test.exe", "fileaccess-arm64.test.exe",
                       "config-arm64.test.exe", "localpreview-arm64.test.exe", "app-arm64.test.exe",
                       "windows-tests-arm64.test.exe"):
            self.assertIn(binary, cross_body)
        self.assertNotIn("windows-cli-day-zero.ps1", cross_body)
        self.assertNotIn("& $path", cross_body)

    def test_hygiene_checks_run_independently_without_skip_paths(self):
        worker = self.jobs["hygiene-checks"]
        self.assertEqual(worker["runs-on"], "ubuntu-latest")
        self.assertNotIn("needs", worker)
        self.assertNotIn("if", worker)
        self.assertEqual(len(self.jobs["hygiene"]["steps"]), 1)

    def test_windows_day_zero_helper_is_direct_offline_and_fail_closed(self):
        helper = (ROOT / "scripts/ci/windows-cli-day-zero.ps1").read_text()
        for contract in (
                "[Parameter(Mandatory = $true)][string]$Binary",
                "[Parameter(Mandatory = $true)][string]$Fixture",
                "[string]$ExpectedVersionLine",
                "[Diagnostics.ProcessStartInfo]::new()",
                "$start.FileName = $exe",
                "'KMX_HOME'", "'KMX_TOOLCHAIN'", "'off'", "'PATH'",
                "'COPILOT_GITHUB_TOKEN'", "'AZURE_OPENAI_API_KEY'",
                "'HTTP_PROXY'", "'HTTPS_PROXY'", "'ALL_PROXY'", "'NO_PROXY'",
                "[Net.WebRequest]::DefaultWebProxy",
                "$savedDefaultProxy",
                "ConvertFrom-Json -Depth 20", "-ExpectedExitCodes @(1)",
                "@('completion', 'bash')", "__start_kmx()",
                "retention.status -ceq 'retained'", "Assert-NoCanary",
                "Assert-SnapshotEqual", "'historical'", "'create'",
                "deferred state", "stateHome"):
            self.assertIn(contract, helper)
        self.assertIn("[char]0x96EA", helper)
        self.assertIn("kmx day zero $nonAscii", helper)
        self.assertNotIn("kmx-windows-tests.exe", helper)
        for forbidden in ("Set-ExecutionPolicy", "Unblock-File", "-ExecutionPolicy Bypass",
                          "wsl", "docker", "kubectl", "ollama pull"):
            self.assertNotIn(forbidden.lower(), helper.lower())

    def test_windows_run_answers_without_a_test_verdict(self):
        helper = (ROOT / "scripts/ci/windows-cli-day-zero.ps1").read_text()
        for contract in ("$runJSON.verdict -ceq 'unknown'",
                         "$runJSON.fullPass -eq $false",
                         "$runJSON.reasonCode -ceq 'completion_ready'",
                         "@($runJSON.examples[0].assertions).Count -eq 0"):
            self.assertIn(contract, helper)
        self.assertNotIn("$runJSON.verdict -ceq 'pass'", helper)
        self.assertIn("$failedJSON.verdict -ceq 'fail'", helper)

    def test_final_gate_rejects_every_non_success_and_wrong_dependency_set(self):
        steps = self.jobs["hygiene"]["steps"]
        gate = steps[-1]
        self.assertEqual(gate.get("name"), "Every required hygiene dependency succeeded")
        self.assertEqual(gate.get("if"), "always()")
        self.assertEqual(gate["env"]["SHARDS"], "${{ toJSON(needs) }}")
        success = {"hygiene-checks": {"result": "success"}, "app-tests": {"result": "success"},
                   "windows-amd64-native": {"result": "success"},
                   "windows-arm64-cross": {"result": "success"}}
        cases = [(success, True), ({}, False),
                 ({**success, "unexpected": {"result": "success"}}, False)]
        for dependency in success:
            missing = {name: result for name, result in success.items() if name != dependency}
            cases.append((missing, False))
            cases.append(({**success, dependency: {}}, False))
            # Includes a failed checks worker while every native shard is green.
            for value in ("failure", "cancelled", "skipped", "unknown", ""):
                broken = {name: dict(result) for name, result in success.items()}
                broken[dependency] = {"result": value}
                cases.append((broken, False))
        for needs, good in cases:
            with self.subTest(needs=needs):
                result = subprocess.run(["bash", "-e", "-c", gate["run"]],
                                        env={**os.environ, "SHARDS": json.dumps(needs)}, capture_output=True, text=True)
                self.assertEqual(result.returncode == 0, good, result.stdout + result.stderr)

    def test_timing_artifacts_are_payload_free_and_failure_logs_are_shard_unique(self):
        self.assertIn("app-tests", self.jobs)
        steps = self.jobs["app-tests"]["steps"]
        artifacts = [s for s in steps if s.get("uses", "").startswith("actions/upload-artifact@")]
        self.assertEqual(len(artifacts), 2)
        timings, failure = artifacts
        self.assertEqual(timings["if"], "always()")
        self.assertEqual(timings["with"]["path"], "kmx-app-test.timings.json")
        self.assertIn("failure()", failure["if"])
        self.assertEqual(failure["with"]["path"], "kmx-app-test.json")
        for artifact in artifacts:
            self.assertIn("${{ matrix.shard }}", artifact["with"]["name"])
            self.assertIn("${{ matrix.os }}", artifact["with"]["name"])

    def test_general_suite_excludes_only_app_and_discovery_fails_closed(self):
        step = next(s for s in self.jobs["hygiene-checks"]["steps"] if s.get("id") == "kmx_tests")
        body = step["run"]
        self.assertIn('grep -vx "' + PACKAGE + '"', body)
        self.assertNotIn('"internal/kmx/app":', body)
        self.assertIn('"internal/kmx/guard":', body)
        self.assertIn('"internal/kmx/fileaccess":', body)
        self.assertIn('"internal/kmx/config":', body)
        self.assertIn('go test "${packages[@]}" -json', body)
        prelude = body.split("go_status=0", 1)[0]
        for stdout, status in (("", 0), (PACKAGE + "\n", 0), ("other/package\n", 1)):
            with self.subTest(stdout=stdout, status=status), tempfile.TemporaryDirectory() as directory:
                tool = Path(directory) / "go"
                tool.write_text("#!/bin/sh\nprintf '%s' '" + stdout + "'\nexit " + str(status) + "\n")
                tool.chmod(0o755)
                result = subprocess.run(["bash", "-e", "-c", prelude],
                                        env={**os.environ, "PATH": directory + os.pathsep + os.environ["PATH"]},
                                        capture_output=True, text=True)
                self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)


if __name__ == "__main__":
    unittest.main()
