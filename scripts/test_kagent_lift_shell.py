#!/usr/bin/env python3
"""Run the actual bootstrap shell with hermetic downloaded-tool doubles."""
import json
import os
import pathlib
import shutil
import subprocess
import tempfile
import unittest

SOURCE = pathlib.Path(__file__).resolve().parent / "ci/kagent-lift.sh"
PAYLOAD = "private-bootstrap-payload"

# Downloads and tool calls are external boundaries. The shell itself, its
# traps, file permissions, environment and cleanup are real in every case.
DOUBLE = r'''#!/usr/bin/env python3
import json, os, pathlib, shutil, sys
name = pathlib.Path(sys.argv[0]).name
args = sys.argv[1:]
with open(os.environ["CALLS"], "a") as log:
    log.write(json.dumps({"tool": name, "args": args,
        "kubeconfig": os.environ.get("KUBECONFIG"),
        "work_mode": pathlib.Path(os.environ["KUBECONFIG"]).parent.stat().st_mode & 0o777,
        "log_mode": (pathlib.Path(os.environ["KUBECONFIG"]).parent / "raw.log").stat().st_mode & 0o777,
        "helm": [os.environ.get(k) for k in ("HELM_CONFIG_HOME", "HELM_CACHE_HOME", "HELM_DATA_HOME")],
        "helm_paths": {k: os.environ.get(k) for k in ("HELM_REGISTRY_CONFIG", "HELM_REPOSITORY_CONFIG", "HELM_REPOSITORY_CACHE", "HELM_PLUGINS")},
        "helm_kube": {k: v for k, v in os.environ.items() if k.startswith("HELM_KUBE")},
        "kube_cache": os.environ.get("KUBECACHEDIR"),
        "docker_config": os.environ.get("DOCKER_CONFIG"),
        "docker_access": {k: os.environ.get(k) for k in ("DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_CERT_PATH", "DOCKER_TLS_VERIFY")},
        "registry_auth": json.loads(pathlib.Path(os.environ["HELM_REGISTRY_CONFIG"]).read_text()) if name == "helm" and os.environ.get("HELM_REGISTRY_CONFIG") and pathlib.Path(os.environ["HELM_REGISTRY_CONFIG"]).exists() else None}) + "\n")
key = name
if name == "driver":
    key = args[0]
elif name == "suite-driver":
    key = "suite-evaluation"
elif name == "kind" and args[:2] == ["create", "cluster"]:
    key = "kind-create"
elif name == "kind" and args[:2] == ["delete", "cluster"]:
    key = "cluster-cleanup"
elif name == "helm" and args and args[0] == "pull":
    key = "chart-pull"
elif name == "go":
    key = "suite-helpers"
if key in os.environ.get("FAIL", "").split(","):
    print("private-bootstrap-payload")
    print("private-bootstrap-payload", file=sys.stderr)
    sys.exit(31 if key == "cluster-cleanup" else 23)
if name == "uname":
    print(os.environ.get("TEST_OS", "Linux") if args == ["-s"] else "x86_64")
elif name == "curl":
    target = pathlib.Path(args[args.index("-o") + 1])
    shutil.copyfile(os.environ["DOUBLE"], target)
elif name == "sha256sum":
    line = sys.stdin.read()
    pin, path = line.split()
    expected = {"kind": "aee6151561422756b764a4ae28e7f44cda5af5a9eead3cc9985112b1de8d8e0d",
        "helm.tar.gz": "dbb4c8fc8e19d159d1a63dda8db655f9ffa4aac1b9a6b188b34a40957119b286",
        "kubectl": "a2e984a18a0c063279d692533031c1eff93a262afcc0afdc517375432d060989"}
    if pin != expected[pathlib.Path(path).name] or args != ["--check", "--status"]:
        sys.exit(24)
elif name == "tar":
    target = pathlib.Path(args[args.index("-C") + 1]) / "linux-amd64/helm"
    target.parent.mkdir()
    shutil.copyfile(os.environ["DOUBLE"], target)
elif name == "kind" and args == ["version", "-q"]:
    print("0.33.0")
elif name == "helm" and args[:1] == ["version"]:
    print("v3.20.0")
elif name == "suite-driver":
    work = pathlib.Path(args[1])
    (work / "suite-qualification.json").write_text('{"proof":true}')
elif name == "go":
    assert args == ["test", "./internal/kmx/app", "-run", "^TestLiveSuiteKagentPlacement$", "-count=1", "-timeout=8m"]
    assert os.environ["KMX_SUITE_KAGENT_LIVE"] == "1"
    assert os.environ["KMX_LIVE_KIND_CONTEXT"] == "kind-kmx-kagent-lift"
elif name == "driver" and args[0] == "qualify":
    work = pathlib.Path(args[2])
    assert os.environ["KMX_HOME"] == str(work / "kmx-home")
    assert os.environ["KMX_TOOLCHAIN"] == "off"
    assert pathlib.Path(os.environ["PATH"].split(os.pathsep)[0]) == work / "fail-bin"
    (work / "qualification.json").write_text('{"proof":true}')
else:
    print("private-bootstrap-payload")
    print("private-bootstrap-payload", file=sys.stderr)
'''


class ShellTests(unittest.TestCase):
    def setUp(self):
        self.assertTrue(SOURCE.exists(), "bootstrap shell is missing")
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.base = pathlib.Path(self.temp.name)
        self.root = self.base / "checkout"
        ci = self.root / "scripts/ci"
        ci.mkdir(parents=True)
        shutil.copyfile(SOURCE, ci / SOURCE.name)
        driver = ci / "kagent-lift.py"
        driver.write_text(DOUBLE.replace('name = pathlib.Path(sys.argv[0]).name', 'name = "driver"'))
        (ci / "suite-kagent.py").write_text(DOUBLE.replace('name = pathlib.Path(sys.argv[0]).name', 'name = "suite-driver"'))
        self.double = self.base / "double"
        self.double.write_text(DOUBLE)
        self.double.chmod(0o700)
        tools = self.base / "tools"
        tools.mkdir()
        for name in ("curl", "sha256sum", "tar", "uname", "go"):
            (tools / name).symlink_to(self.double)
        self.runner = self.base / "runner"
        self.runner.mkdir()
        self.home = self.base / "home"
        self.home.mkdir()
        self.kubeconfig = self.home / "config"
        self.kubeconfig.write_text("caller-kubeconfig")
        self.calls = self.base / "calls"
        self.env = dict(os.environ, PATH=str(tools) + os.pathsep + os.environ["PATH"],
                        RUNNER_TEMP=str(self.runner), HOME=str(self.home),
                        KUBECONFIG=str(self.kubeconfig), CALLS=str(self.calls), DOUBLE=str(self.double))
        for key in ("HELM_CONFIG_HOME", "HELM_CACHE_HOME", "HELM_DATA_HOME"):
            self.env[key] = str(self.home / key)

    def run_shell(self, args=("bootstrap",), fail=""):
        result = subprocess.run(["bash", str(self.root / "scripts/ci/kagent-lift.sh"), *args],
                                env=dict(self.env, FAIL=fail), cwd=self.root,
                                capture_output=True, text=True, timeout=15)
        self.assertNotIn(PAYLOAD, result.stdout + result.stderr)
        self.assertEqual(self.kubeconfig.read_text(), "caller-kubeconfig")
        self.assertEqual(sorted(p.name for p in self.home.iterdir()), ["config"])
        self.assertEqual([p.name for p in self.runner.iterdir()], ["kmx-kagent-lift"] if result.returncode == 0 and args != ("bootstrap",) else [], "owned work was not removed")
        records = [json.loads(line) for line in self.calls.read_text().splitlines()] if self.calls.exists() else []
        return result, records

    def assert_owned_cleanup(self, records, expected):
        deletes = [r for r in records if r["tool"] == "kind" and r["args"][:2] == ["delete", "cluster"]]
        self.assertEqual(len(deletes), expected)
        for record in deletes:
            self.assertEqual(record["args"], ["delete", "cluster", "--name", "kmx-kagent-lift"])

    def test_explicit_bootstrap_succeeds_without_installed_cli(self):
        for args in (("bootstrap",),):
            with self.subTest(args=args):
                self.calls.unlink(missing_ok=True)
                result, records = self.run_shell(args)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIn("runtime MCP/native A2A NOT qualified", result.stdout)
                self.assert_owned_cleanup(records, 1)
                modes = [r["args"][0] for r in records if r["tool"] == "driver"]
                self.assertEqual(modes, ["cluster-absent", "node", "values", "chart", "provision", "bootstrap"])
                for record in records:
                    self.assertEqual(record["work_mode"], 0o700)
                    self.assertEqual(record["log_mode"], 0o600)
                    if record["tool"] in ("kind", "helm", "kubectl", "driver"):
                        work = pathlib.Path(record["kubeconfig"]).parent
                        self.assertEqual(work.parent, self.runner)
                        self.assertEqual([pathlib.Path(p).parent for p in record["helm"]], [work] * 3)

    def test_inherited_helm_paths_and_connection_overrides_are_private(self):
        for key in ("HELM_REGISTRY_CONFIG", "HELM_REPOSITORY_CONFIG", "HELM_REPOSITORY_CACHE", "HELM_PLUGINS"):
            self.env[key] = str(self.home / key)
        for key in ("HELM_KUBEAPISERVER", "HELM_KUBECONTEXT", "HELM_KUBETOKEN", "HELM_KUBECAFILE",
                    "HELM_KUBEASUSER", "HELM_KUBEASGROUPS", "HELM_KUBETLS_SERVER_NAME",
                    "HELM_KUBEINSECURE_SKIP_TLS_VERIFY", "HELM_KUBE_FUTURE_OVERRIDE"):
            self.env[key] = "caller-override"
        result, records = self.run_shell()
        self.assertEqual(result.returncode, 0, result.stderr)
        for record in records:
            if record["tool"] == "helm":
                work = pathlib.Path(record["kubeconfig"]).parent
                for key, value in record["helm_paths"].items():
                    self.assertIsNotNone(value, key)
                    self.assertTrue(pathlib.Path(value).is_relative_to(work), key)
                self.assertEqual(record["helm_kube"], {})
                if record["args"][0] == "install":
                    self.assertEqual(record["args"][record["args"].index("--kube-context") + 1], "kind-kmx-kagent-lift")
        self.assert_owned_cleanup(records, 1)

    def test_helm_chart_credentials_are_keyless_without_changing_docker_access(self):
        self.env.update(DOCKER_CONFIG=str(self.home / "docker"), DOCKER_HOST="unix:///caller/docker.sock",
                        DOCKER_CONTEXT="caller-context", DOCKER_CERT_PATH=str(self.home / "certs"), DOCKER_TLS_VERIFY="1")
        result, records = self.run_shell()
        self.assertEqual(result.returncode, 0, result.stderr)
        for record in records:
            self.assertEqual(record["docker_access"], {key: self.env[key] for key in
                             ("DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_CERT_PATH", "DOCKER_TLS_VERIFY")})
            if record["tool"] == "helm":
                work = pathlib.Path(record["kubeconfig"]).parent
                self.assertTrue(pathlib.Path(record["docker_config"]).is_relative_to(work))
                # A configured empty GHCR entry suppresses native-helper detection
                # while supplying no credential to either Helm or its fallback.
                self.assertEqual(record["registry_auth"], {"auths": {"ghcr.io": {}}})
                self.assertEqual(pathlib.Path(record["docker_config"]) / "config.json",
                                 pathlib.Path(record["helm_paths"]["HELM_REGISTRY_CONFIG"]))
            else:
                self.assertEqual(record["docker_config"], self.env["DOCKER_CONFIG"])
        self.assert_owned_cleanup(records, 1)

    def test_kubernetes_discovery_cache_is_private_with_or_without_caller_override(self):
        for inherited in (None, str(self.home / "caller-cache")):
            with self.subTest(inherited=inherited):
                self.calls.unlink(missing_ok=True)
                if inherited is None:
                    self.env.pop("KUBECACHEDIR", None)
                else:
                    self.env["KUBECACHEDIR"] = inherited
                result, records = self.run_shell()
                self.assertEqual(result.returncode, 0, result.stderr)
                for record in records:
                    work = pathlib.Path(record["kubeconfig"]).parent
                    self.assertEqual(record["kube_cache"], str(work / "kube-cache"))
                self.assert_owned_cleanup(records, 1)

    def test_chart_pull_and_bootstrap_failures_keep_phase_status_private_and_cleanup(self):
        for fail, phase in (("chart-pull", "chart-crds-pull"), ("bootstrap", "bootstrap")):
            with self.subTest(fail=fail):
                self.calls.unlink(missing_ok=True)
                result, records = self.run_shell(("bootstrap",), fail)
                self.assertEqual(result.returncode, 23)
                self.assertIn("phase=" + phase + " exitstatus=23", result.stderr)
                self.assertNotIn("characterized", result.stdout)
                self.assert_owned_cleanup(records, 1)

    def test_unowned_inventory_and_ambiguous_create_are_never_deleted_or_diagnosed(self):
        for fail in ("cluster-absent", "kind-create"):
            with self.subTest(fail=fail):
                self.calls.unlink(missing_ok=True)
                result, records = self.run_shell(fail=fail)
                self.assertEqual(result.returncode, 23)
                self.assertIn("phase=" + fail + " exitstatus=23", result.stderr)
                self.assert_owned_cleanup(records, 0)
                self.assertNotIn("diagnostics", [r["args"][0] for r in records if r["tool"] == "driver"])

    def test_cleanup_failure_fails_success_but_preserves_original_failure(self):
        for fail in ("cluster-cleanup", "bootstrap,cluster-cleanup"):
            with self.subTest(fail=fail):
                self.calls.unlink(missing_ok=True)
                result, records = self.run_shell(fail=fail)
                self.assertEqual(result.returncode, 23 if fail.startswith("bootstrap,") else 31)
                self.assertIn("phase=cluster-cleanup exitstatus=31", result.stderr)
                self.assertNotIn("characterized", result.stdout)
                self.assert_owned_cleanup(records, 1)

    def test_download_checksum_and_extract_failures_remove_only_owned_work(self):
        for fail, phase in (("curl", "kind-download"), ("sha256sum", "kind-checksum"), ("tar", "helm-extract")):
            with self.subTest(fail=fail):
                self.calls.unlink(missing_ok=True)
                result, records = self.run_shell(fail=fail)
                self.assertEqual(result.returncode, 23)
                self.assertIn("phase=" + phase + " exitstatus=23", result.stderr)
                self.assert_owned_cleanup(records, 0)

    def test_unsupported_platform_fails_before_downloading_or_creating_cluster(self):
        self.env["TEST_OS"] = "Darwin"
        result, records = self.run_shell()
        self.assertEqual(result.returncode, 1)
        self.assertIn("phase=platform exitstatus=1", result.stderr)
        self.assertEqual([r["tool"] for r in records], ["uname", "uname"])

    def test_default_and_explicit_qualification_reach_driver_with_private_state(self):
        for args in ((), ("qualify",)):
            with self.subTest(args=args):
                self.calls.unlink(missing_ok=True)
                result, records = self.run_shell(args)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIn("native A2A MCP execution qualified", result.stdout)
                self.assertNotIn("NOT qualified", result.stdout)
                self.assert_owned_cleanup(records, 1)
                self.assertEqual([r["args"][0] for r in records if r["tool"] == "driver"][-1], "qualify")
                self.assertEqual(json.loads((self.runner / "kmx-kagent-lift/qualification.json").read_text()), {"proof": True})
                shutil.rmtree(self.runner / "kmx-kagent-lift")

    def test_qualification_failure_does_not_leave_success_artifact(self):
        for phase in ("qualify", "suite-evaluation", "suite-helpers"):
            with self.subTest(phase=phase):
                self.calls.unlink(missing_ok=True)
                result, records = self.run_shell((), phase)
                self.assertEqual(result.returncode, 23)
                self.assertIn("phase=" + phase + " exitstatus=23", result.stderr)
                self.assert_owned_cleanup(records, 1)
                self.assertFalse((self.runner / "kmx-kagent-lift/qualification.json").exists())

    def test_real_python_boundary_reports_only_closed_qualifier_phase_and_reason(self):
        driver = self.root / "scripts/ci/kagent-lift.py"
        bootstrap_double = driver.with_name("bootstrap-double.py")
        bootstrap_double.write_text(driver.read_text())
        actual = SOURCE.with_name("kagent-lift.py").read_text()
        injection = '''
original_main = main
def main(argv):
    if argv[0] != "qualify":
        subprocess.run([sys.executable, %r, *argv], check=True)
    else:
        original_main(argv)
def qualify(binary, work):
    qualification_phase(os.environ["TEST_QUALIFIER_PHASE"])
    print("private-bootstrap-payload")
    print("private-bootstrap-payload", file=sys.stderr)
    defect = os.environ["TEST_QUALIFIER_DEFECT"]
    if defect == "tool":
        raise RuntimeError("private-bootstrap-payload")
    if defect == "unlisted":
        raise QualificationError("private-bootstrap-payload", reason="private-bootstrap-payload")
    raise QualificationError("private-bootstrap-payload")

''' % str(bootstrap_double)
        driver.write_text(actual.replace('\nif __name__ == "__main__":', '\n' + injection + 'if __name__ == "__main__":'))
        cases = (("runtime-fixture", "cli", "qualification-check-failed"),
                 ("native-a2a-one-shot", "tool", "unclassified-qualifier-error"),
                 ("setup", "unlisted", "unclassified-qualifier-error"),
                 (PAYLOAD, "cli", "unclassified-qualifier-error"))
        for phase, defect, reason in cases:
            with self.subTest(phase=phase, defect=defect):
                self.calls.unlink(missing_ok=True)
                self.env.update(TEST_QUALIFIER_PHASE=phase, TEST_QUALIFIER_DEFECT=defect)
                result, records = self.run_shell(())
                self.assertEqual(result.returncode, 1)
                safe_phase = "setup" if phase == PAYLOAD else phase
                expected = [] if phase == PAYLOAD else ["kagent qualification: phase=" + phase]
                expected += ["kagent qualification failed: phase=" + safe_phase + " reason=" + reason,
                             "kagent bootstrap failed: phase=qualify exitstatus=1; runtime MCP/native A2A NOT qualified"]
                self.assertEqual(result.stderr.splitlines(), expected)
                self.assertEqual(result.stdout, "")
                self.assert_owned_cleanup(records, 1)

    def test_invalid_modes_and_extra_arguments_do_no_work(self):
        for args in (("invalid",), ("bootstrap", "extra")):
            with self.subTest(args=args):
                result, records = self.run_shell(args)
                self.assertEqual(result.returncode, 2)
                self.assertEqual(records, [])


if __name__ == "__main__":
    unittest.main()
