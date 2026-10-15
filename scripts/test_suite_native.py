#!/usr/bin/env python3
"""Hermetic checks for independent native qualification evidence, not live proof."""
import copy
import hashlib
import importlib.util
import io
import json
import pathlib
import os
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

ROOT = pathlib.Path(__file__).resolve().parents[1]


def load(name):
    path = ROOT / "scripts/ci" / (name + ".py")
    if not path.exists():
        return None
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


Q, A = load("suite-native"), load("suite-native-audit")


def constructor_fixture(root):
    repo = root / "fixture-repo"
    (repo / "bin").mkdir(parents=True)
    (repo / "bin/kmx").write_text("constructor prerequisite only; never executed")
    scripts = repo / "scripts/ci"
    scripts.mkdir(parents=True)
    (scripts / "suite-native-audit.py").write_bytes((ROOT / "scripts/ci/suite-native-audit.py").read_bytes())
    return scripts / "suite-native.py"


def task():
    return {"apiVersion": "core.orka.ai/v1alpha1", "kind": "Task",
            "metadata": {"name": "member-eval-one", "namespace": "orka-system", "uid": "task-uid", "generation": 1},
            "spec": {"type": "ai", "prompt": "What is 2 + 2?", "agentRef": {"name": "member", "namespace": "orka-system"},
                     "resources": {}, "priority": 500, "concurrencyPolicy": "Forbid", "failedRunsHistoryLimit": 1,
                     "successfulRunsHistoryLimit": 3, "startingDeadlineSeconds": 100},
            "status": {"phase": "Succeeded", "resultRef": {"available": True}}}


class PromotionWorkflowTests(unittest.TestCase):
    """Execute CI shell ownership decisions without Docker or Kubernetes."""

    def setUp(self):
        import yaml
        workflow = yaml.safe_load((ROOT / ".github/workflows/ci.yml").read_text())
        self.job = workflow["jobs"]["e2e-orka-runtime"]
        self.steps = self.job["steps"]
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = pathlib.Path(self.temp.name)
        (self.root / "bin").mkdir()
        self.env_file = self.root / "github-env"
        self.env_file.touch()
        self.record = self.root / "calls.jsonl"
        # Fake only external commands. Bash, its failure propagation, redirects,
        # GITHUB_ENV writes and cleanup guards are the real workflow operations.
        fake = '''#!/usr/bin/env python3
import json, os, pathlib, sys
name = pathlib.Path(sys.argv[0]).name
args = sys.argv[1:]
with open(os.environ["CALLS"], "a") as stream:
    stream.write(json.dumps({"tool": name, "args": args,
        "cluster": os.environ.get("KIND_CLUSTER"),
        "context": os.environ.get("KUBE_CTX"),
        "owned": pathlib.Path(os.environ["GITHUB_ENV"]).read_text()}) + "\\n")
if name == "kind" and args == ["get", "clusters"]:
    print(os.environ.get("CLUSTERS", ""))
    sys.exit(int(os.environ.get("LIST_EXIT", "0")))
if name == "kmx" and args == ["local", "up", "--step", "cluster"]:
    sys.exit(int(os.environ.get("CREATE_EXIT", "0")))
if name == "kmx" and "install" in args:
    print("PRIVATE_INSTALL_CANARY")
    if os.environ.get("VERIFIED", "1") == "1":
        print("Installer verified: pinned")
    sys.exit(int(os.environ.get("INSTALL_EXIT", "0")))
if name == "python3" and "nodes" in args:
    sys.exit(int(os.environ.get("MIRROR_EXIT", "0")))
'''
        for name in ("kind", "kmx", "python3"):
            path = self.root / "bin" / name
            path.write_text(fake.replace("#!/usr/bin/env python3", "#!" + sys.executable, 1))
            path.chmod(0o700)
        self.env = dict(os.environ, PATH=str(self.root / "bin") + os.pathsep + os.defpath,
                        CALLS=str(self.record), GITHUB_ENV=str(self.env_file),
                        GITHUB_WORKSPACE=str(self.root), RUNNER_TEMP=str(self.root),
                        KIND_CLUSTER="kmx-orka", KUBE_CTX="kind-kmx-orka")
        for key in ("KMX_ORKA_RUNTIME_CREATED", "KMX_ORKA_PROMOTION_CREATED"):
            self.env.pop(key, None)

    def step(self, name):
        found = [step for step in self.steps if step.get("name") == name]
        self.assertEqual(len(found), 1, "missing dedicated workflow step: " + name)
        return found[0]

    def run_step(self, name, **env):
        return subprocess.run(["bash", "-e", "-o", "pipefail", "-c", self.step(name)["run"]],
                              cwd=self.root, env=dict(self.env, **env), capture_output=True, timeout=10)

    def calls(self):
        return [json.loads(line) for line in self.record.read_text().splitlines()] if self.record.exists() else []

    def setup_promotion(self, **env):
        return self.run_step("Native promotion — create only the dedicated destination cluster", **env)

    def cleanup(self):
        inherited = dict(line.split("=", 1) for line in self.env_file.read_text().splitlines())
        return self.run_step("down", **inherited)

    def test_success_records_ownership_after_create_and_routes_only_destination(self):
        result = self.setup_promotion()
        self.assertEqual(result.returncode, 0, result.stderr)
        calls = self.calls()
        self.assertEqual([(c["tool"], c["args"]) for c in calls], [
            ("kind", ["get", "clusters"]),
            ("kmx", ["local", "up", "--step", "cluster"]),
            ("python3", [str(self.root / "scripts/ci/registry-mirrors.py"), "nodes", "--cluster", "kmx-orka-promote"])])
        self.assertEqual((calls[1]["cluster"], calls[1]["context"]), ("kmx-orka-promote", "kind-kmx-orka-promote"))
        self.assertNotIn("KMX_ORKA_PROMOTION_CREATED=1", calls[1]["owned"])
        self.assertIn("KMX_ORKA_PROMOTION_CREATED=1", calls[2]["owned"])
        self.assertEqual(self.cleanup().returncode, 0)
        self.assertEqual(self.calls()[-1]["args"], ["delete", "cluster", "--name", "kmx-orka-promote"])

    def test_preexisting_or_unknown_cluster_list_refuses_without_delete(self):
        for env in ({"CLUSTERS": "unrelated\nkmx-orka-promote"}, {"LIST_EXIT": "1"}):
            with self.subTest(env=env):
                self.record.unlink(missing_ok=True)
                result = self.setup_promotion(**env)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(self.env_file.read_text(), "")
                self.assertEqual(self.cleanup().returncode, 0)
                self.assertEqual([(c["tool"], c["args"]) for c in self.calls()], [("kind", ["get", "clusters"])])

    def test_failed_ambiguous_create_is_not_owned_or_deleted(self):
        self.assertNotEqual(self.setup_promotion(CREATE_EXIT="1").returncode, 0)
        self.assertEqual(self.env_file.read_text(), "")
        self.assertEqual(self.cleanup().returncode, 0)
        self.assertEqual(len(self.calls()), 2)

    def test_failure_after_acknowledged_create_still_cleans_owned_destination(self):
        self.assertNotEqual(self.setup_promotion(MIRROR_EXIT="1").returncode, 0)
        self.assertEqual(self.env_file.read_text(), "KMX_ORKA_PROMOTION_CREATED=1\n")
        self.assertEqual(self.cleanup().returncode, 0)
        self.assertEqual(self.calls()[-1]["args"], ["delete", "cluster", "--name", "kmx-orka-promote"])

    def test_cleanup_requires_each_exact_ownership_flag_and_preserves_source(self):
        for source, destination in (("", ""), ("1", ""), ("", "1"), ("1", "1"), ("0", "true")):
            with self.subTest(source=source, destination=destination):
                self.record.unlink(missing_ok=True)
                result = self.run_step("down", KMX_ORKA_RUNTIME_CREATED=source, KMX_ORKA_PROMOTION_CREATED=destination)
                self.assertEqual(result.returncode, 0)
                expected = []
                if source == "1":
                    expected.append(["delete", "cluster", "--name", "kmx-orka"])
                if destination == "1":
                    expected.append(["delete", "cluster", "--name", "kmx-orka-promote"])
                self.assertCountEqual([c["args"] for c in self.calls()], expected)

    def test_destination_install_is_private_verified_and_provider_free(self):
        name = "Native promotion — install pinned Orka without inference"
        for env, success in (({}, True), ({"VERIFIED": "0"}, False), ({"INSTALL_EXIT": "1"}, False)):
            with self.subTest(env=env):
                self.record.unlink(missing_ok=True)
                result = self.run_step(name, **env)
                self.assertEqual(result.returncode == 0, success)
                self.assertNotIn(b"PRIVATE_INSTALL_CANARY", result.stdout + result.stderr)
                self.assertEqual(self.calls()[0]["args"], ["--context", "kind-kmx-orka-promote", "orka", "install", "--provider", "-"])
        captures = list(self.root.glob("*orka*install*"))
        self.assertTrue(captures, "installer output must be captured privately")
        self.assertTrue(all(path.stat().st_mode & 0o077 == 0 for path in captures))

    def test_qualifier_receives_both_contexts_with_existing_ci_guards(self):
        name = "Native suite — qualify evaluation, exact transfer and CLI lift"
        result = self.run_step(name)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.calls()[0]["args"], ["scripts/ci/suite-native.py", "kind-kmx-orka", "kind-kmx-orka-promote", str(self.root / "kmx-suite-native")])
        order = [step.get("name") for step in self.steps]
        setup = "Native promotion — create only the dedicated destination cluster"
        install = "Native promotion — install pinned Orka without inference"
        self.assertLess(order.index("Orka — Agent and Task return an exact local-model answer"), order.index(setup))
        self.assertLess(order.index(setup), order.index(install))
        self.assertLess(order.index(install), order.index(name))
        for guarded in (setup, install, name):
            self.assertEqual(self.step(guarded)["if"], "steps.changes.outputs.docs_only != 'true'")
        self.assertEqual(self.step("down")["if"], "always() && steps.changes.outputs.docs_only != 'true'")
        self.assertEqual(self.job["timeout-minutes"], 45)
        self.assertEqual(self.step("Upload only passing native metadata")["with"]["path"].splitlines(), [
            "${{ runner.temp }}/kmx-suite-native/artifacts/passing.json",
            "${{ runner.temp }}/kmx-suite-native/artifacts/qualification.json"])


class QualificationTests(unittest.TestCase):
    def setUp(self):
        self.assertIsNotNone(Q, "native qualification helper is not implemented")

    def test_constructor_retains_original_home_kubeconfig_when_unset_or_empty(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            driver = constructor_fixture(root)
            original_home = root / "original-home"
            config = original_home / ".kube/config"
            config.parent.mkdir(parents=True)
            config.write_text("PRIVATE_CONFIG_CANARY")
            for value in (None, ""):
                with self.subTest(kubeconfig=value):
                    env = {"HOME": str(original_home), "PATH": os.defpath}
                    if value is not None:
                        env["KUBECONFIG"] = value
                    work = root / ("unset-work" if value is None else "empty-work")
                    with mock.patch.dict(os.environ, env, clear=True), mock.patch.object(Q.shutil, "which", return_value="/fixture/kubectl"), mock.patch.object(Q, "__file__", str(driver)):
                        qualifier = Q.Qualification("kind-owned", work)
                    self.assertEqual(qualifier.env.get("KUBECONFIG"), str(config))
                    self.assertEqual(qualifier.product_env.get("KUBECONFIG"), str(config))
                    self.assertNotEqual(qualifier.env["HOME"], str(original_home))
                    self.assertEqual(qualifier.product_env["HOME"], qualifier.env["HOME"])
                    self.assertFalse((work / "home/.kube/config").exists())
                    self.assertEqual(config.read_text(), "PRIVATE_CONFIG_CANARY")

    def test_constructor_preserves_explicit_file_selection_as_absolute(self):
        with tempfile.TemporaryDirectory(dir=ROOT) as directory:
            root = pathlib.Path(directory)
            driver = constructor_fixture(root)
            config = root / " selected-config "
            config.write_text("PRIVATE_EXPLICIT_CONFIG_CANARY")
            # A relative explicit path must keep selecting the same file after
            # command() changes cwd to private work; whitespace is part of its name.
            relative = os.path.relpath(config)
            env = {"HOME": str(root / "other-home"), "PATH": os.defpath, "KUBECONFIG": relative}
            with mock.patch.dict(os.environ, env, clear=True), mock.patch.object(Q.shutil, "which", return_value="/fixture/kubectl"), mock.patch.object(Q, "__file__", str(driver)):
                qualifier = Q.Qualification("kind-owned", root / "work")
            self.assertEqual(qualifier.env["KUBECONFIG"], str(config))
            self.assertEqual(qualifier.product_env["KUBECONFIG"], str(config))

    def test_constructor_refuses_missing_or_directory_kubeconfig(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            for config in (root / "missing", root):
                with self.subTest(config=config):
                    env = {"HOME": str(root), "PATH": os.defpath, "KUBECONFIG": str(config)}
                    with mock.patch.dict(os.environ, env, clear=True), mock.patch.object(Q.shutil, "which", return_value="/fixture/kubectl"):
                        with self.assertRaisesRegex(ValueError, "^kubeconfig$"):
                            Q.Qualification("kind-owned", root / ("missing-work" if config.name == "missing" else "directory-work"))

    def test_forward_has_own_thirty_second_window_after_slow_rollout(self):
        for binds in (True, False):
            with self.subTest(binds=binds), tempfile.TemporaryDirectory() as directory:
                qualifier = Q.Qualification.__new__(Q.Qualification)
                qualifier.work, qualifier.repo, qualifier.context = pathlib.Path(directory), ROOT, "kind-owned"
                qualifier.env, qualifier.kubectl = {}, "/fixture/kubectl"
                clock = [100]
                writes = []
                def kube(*args, **kwargs):
                    if "rollout" in args:
                        self.assertEqual(kwargs["timeout"], 480)
                        clock[0] += 479
                    if kwargs.get("raw"):
                        writes.append(json.loads(kwargs["raw"]))
                    return b""
                qualifier.kube = kube
                qualifier.get = lambda resource, name: {"spec": {"secretRef": {"name": "local-provider-key"}}, "status": {"ready": True}}
                forward = mock.MagicMock()
                forward.__enter__.return_value = forward
                def poll():
                    if binds and clock[0] >= 589:
                        (qualifier.work / "model-forward.log").write_bytes(b"Forwarding from 127.0.0.1:18091 -> 8080\n")
                    return None
                forward.poll.side_effect = poll
                def response(request, timeout):
                    url = request if isinstance(request, str) else request.full_url
                    self.assertIn(url, ("http://127.0.0.1:18091/readyz", "http://127.0.0.1:18091/v1/chat/completions"))
                    result = io.BytesIO(b'{"choices":[{}]}')
                    result.status = 200
                    return result
                with mock.patch.object(Q.subprocess, "Popen", return_value=forward), \
                     mock.patch.object(Q.time, "monotonic", side_effect=lambda: clock[0]), \
                     mock.patch.object(Q.time, "sleep", side_effect=lambda seconds: clock.__setitem__(0, clock[0] + seconds)), \
                     mock.patch.object(Q.urllib.request, "urlopen", side_effect=response):
                    if binds:
                        qualifier.provision()
                        self.assertEqual(clock[0], 589)
                        self.assertEqual([doc["metadata"]["name"] for doc in writes], ["native-suite-inference"])
                    else:
                        with self.assertRaisesRegex(ValueError, "^model-ready$"):
                            qualifier.provision()
                        self.assertEqual(clock[0], 609)
                        self.assertEqual(writes, [])

    def test_fingerprint_uses_original_bytes_and_filenames(self):
        files = {"workspace.yaml": b"x\n", "eval/a.yaml": b"y\n"}
        expected = b'{"files":{"eval/a.yaml":"sha256:3bb2abb69ebb27fbfe63c7639624c6ec5e331b841a5bc8c3ebc10b9285e90877","workspace.yaml":"sha256:73cb3858a687a8494ca3323053016282f3dad39d42cf62ca4e79dda2aac7d9ac"},"layout":"kmx.native-suite-evals/v2","renderer":"kmx.native-orka/v2"}'
        self.assertEqual(Q.fingerprint(files), "sha256:" + hashlib.sha256(expected).hexdigest())
        self.assertNotEqual(Q.fingerprint(files), Q.fingerprint({"workspace.yaml": b"x\n", "eval/a.yaml": b"y\n\n"}))

    def test_task_result_hash_removes_only_cli_added_newline(self):
        self.assertEqual(Q.result_digest(b"4\n\n"), hashlib.sha256(b"4\n").hexdigest())
        with self.assertRaises(ValueError):
            Q.result_digest(b"4")

    def test_live_task_checks_identity_exact_spec_and_terminal_result(self):
        row = {"taskName": "member-eval-one", "taskUID": "task-uid"}
        self.assertEqual(Q.inspect_task(task(), row, "member", "What is 2 + 2?")["generation"], 1)
        for edit in (lambda t: t["metadata"].update(uid="replaced"),
                     lambda t: t["metadata"].update(generation=2),
                     lambda t: t["spec"].update(prompt="other"),
                     lambda t: t["spec"].update(ai={"model": "override"}),
                     lambda t: t["status"]["resultRef"].update(available=False)):
            changed = task()
            edit(changed)
            with self.subTest(changed=changed), self.assertRaises(ValueError):
                Q.inspect_task(changed, row, "member", "What is 2 + 2?")

    def test_owned_pair_spec_uid_generation_and_current_markers_are_independent(self):
        subject = {"Suite": "suite", "SuiteDigest": "sha256:s", "Member": "member", "Renderer": "kmx.native-orka/v1",
                   "BindingDigest": "b", "RenderedDigest": "d"}
        doc = {"kind": "Provider", "metadata": {"name": "member", "namespace": "orka-system", "uid": "p", "generation": 1,
               "annotations": dict(zip(Q.MARKERS, ["suite", "sha256:s", "member", "kmx.native-orka/v1", "b", "d"]))},
               "spec": {"type": "openai"}, "status": {"ready": True, "conditions": [{"type": "Ready", "status": "True", "observedGeneration": 1}]}}
        # Independent length-framed digest, not the qualifier's hash helper.
        canonical = b'{"type":"openai"}'
        framed = b'rendered/000.yaml 23\nkmx.native-orka.spec/v1\nrendered/001.yaml 17\n' + canonical + b'\n'
        owned = {"UID": "p", "Generation": 1, "SpecDigest": hashlib.sha256(framed).hexdigest()}
        Q.inspect_pair(doc, "Provider", owned, subject)
        for field, value in (("uid", "other"), ("generation", 2)):
            changed = copy.deepcopy(doc)
            changed["metadata"][field] = value
            with self.assertRaises(ValueError):
                Q.inspect_pair(changed, "Provider", owned, subject)
        changed = copy.deepcopy(doc)
        changed["spec"]["type"] = "azure-openai"
        with self.assertRaises(ValueError):
            Q.inspect_pair(changed, "Provider", owned, subject)
        changed = copy.deepcopy(doc)
        changed["metadata"]["annotations"][Q.MARKERS[-1]] = "foreign"
        with self.assertRaises(ValueError):
            Q.inspect_pair(changed, "Provider", owned, subject)

    def test_packaged_case_matches_go_packaging(self):
        # Go pins the same digest.
        raw = b'id: arithmetic\ninput: What is 2 + 2?\nexpectContains:\n  - "4"\n'
        self.assertEqual(hashlib.sha256(Q.packaged_case(raw)).hexdigest(), "ff20948597abcfbda7dfbf295cc4af52635ab2e639bf7e899fe0eb6fe8e5b33f")
        self.assertEqual(Q.packaged_case_path("greeter", raw), "evals/greeter/cases/arithmetic.json")
        self.assertEqual(Q.packaged_case(raw + b"# comment\n"), Q.packaged_case(raw))
        repeated = json.loads(Q.packaged_case(raw + b"runs: 3\nminPasses: 2\n"))
        self.assertEqual((repeated["runs"], repeated["minPasses"]), (3, 2))

    def test_case_metadata_comes_from_authored_bytes_not_receipt_claims(self):
        raw = b'id: arithmetic\ninput: What is 2 + 2?\nexpectContains:\n  - "4"\n'
        row = {"id": "arithmetic", "path": "evals/greeter/cases/arithmetic.json", "caseDigest": hashlib.sha256(Q.packaged_case(raw)).hexdigest(),
               "inputDigest": hashlib.sha256(b"What is 2 + 2?").hexdigest(), "verdict": "pass",
               "assertions": [{"ID": "unused"}]}
        definition = hashlib.sha256(b"type 8\ncontains\noperand 1\n4\n").hexdigest()
        row["assertions"] = [{"id": "expectContains.0", "type": "contains", "definitionDigest": definition,
                              "evaluatorVersion": "kmx.text-assertions/v1", "verdict": "pass"}]
        self.assertTrue(hasattr(Q, "verify_cases"), "authored-case cross-check is not implemented")
        Q.verify_cases([row], {"one.yaml": raw}, "greeter")
        for key, value in (("caseDigest", "0" * 64), ("inputDigest", "0" * 64), ("path", "metadata/kmx/eval/one.yaml"),
                           ("caseDigest", hashlib.sha256(raw).hexdigest())):
            changed = copy.deepcopy(row)
            changed[key] = value
            with self.assertRaises(ValueError):
                Q.verify_cases([changed], {"one.yaml": raw}, "greeter")
        with self.assertRaises(ValueError):
            Q.verify_cases([row], {"one.yaml": raw}, "other")
        with self.assertRaises(ValueError):
            Q.verify_cases([], {"one.yaml": raw}, "greeter")

    def test_safe_refusal_requires_specific_reason_and_no_persistent_writes(self):
        self.assertTrue(hasattr(Q, "verify_refusal"), "specific no-write refusal check is not implemented")
        Q.verify_refusal(1, b"native evaluation refused before native changes", [], "native evaluation refused before native changes")
        for code, output, frames in ((0, b"native evaluation refused before native changes", []),
                                     (1, b"arbitrary failure", []),
                                     (1, b"native evaluation refused before native changes", [{"persistent": True}])):
            with self.assertRaises(ValueError):
                Q.verify_refusal(code, output, frames, "native evaluation refused before native changes")

    def test_refusal_tail_checks_unsupported_before_any_cluster_or_model_call(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            q = Q.Qualification.__new__(Q.Qualification)
            q.work, q.repo = root, ROOT
            q.secret_ref = {"name": "fixture-key", "key": "api-key"}
            (root / "instructions").write_text("Fixture instructions")
            (root / "workspace/eval").mkdir(parents=True)
            cases = {"one.yaml": b'id: one\ninput: question\nexpectContains: ["answer"]\n'}
            case_path = root / "workspace/eval/one.yaml"
            case_path.write_bytes(cases["one.yaml"])
            q.frames = lambda: []
            q.command = q.kube = q.get = lambda *a, **k: self.fail("unsupported case performed cluster/render I/O")
            def evaluation(workspace, output, success=True):
                self.assertFalse(success)
                self.assertEqual(Q.yaml.safe_load(case_path.read_bytes())["assertions"][0]["type"], "toolCalled")
                return subprocess.CompletedProcess([], 1, b"", b"unsupported native suite evaluator")
            q.evaluation = evaluation
            q.refusals(root / "workspace", root / "instructions", cases, {"Member": "member"})
            self.assertFalse((root / "receipt/unsupported.json").exists())

    def test_private_root_refuses_adoption_and_symlink_ancestors(self):
        self.assertTrue(hasattr(Q, "private_root"), "private scenario root is missing")
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory) / "native"
            Q.private_root(root)
            self.assertEqual(root.stat().st_mode & 0o777, 0o700)
            with self.assertRaises((ValueError, FileExistsError)):
                Q.private_root(root)
            link = pathlib.Path(directory) / "link"
            link.symlink_to(root, target_is_directory=True)
            with self.assertRaises(ValueError):
                Q.private_root(link / "child")

    def test_wrong_context_fails_privately_before_creating_work(self):
        with tempfile.TemporaryDirectory() as directory:
            work = pathlib.Path(directory) / "work"
            env = dict(os.environ, KIND_CLUSTER="dedicated", KMX_ORKA_RUNTIME_CREATED="1")
            result = subprocess.run([sys.executable, str(ROOT / "scripts/ci/suite-native.py"), "kind-other", str(work)],
                                    env=env, capture_output=True, timeout=10)
            self.assertEqual(result.returncode, 1, "wrong context must refuse before operational tools")
            self.assertEqual(result.stdout, b"")
            self.assertEqual(result.stderr, b"native qualification: phase=preflight reason=failed\n")
            self.assertFalse(work.exists())

    def test_audit_allows_only_pinned_controller_read_only_rollout(self):
        args = ["--context", "kind-owned", "-n", "orka-system", "rollout", "status", "deploy/orka-api-controller", "--timeout=10s"]
        A.check_scope(args, "kind-owned")
        frame = A.classify(args)
        self.assertEqual((frame["resource"], frame["name"], frame["persistent"]), ("deployment", "orka-api-controller", False))
        for index, value in ((5, "restart"), (6, "deploy/other"), (3, "other")):
            refused = list(args)
            refused[index] = value
            with self.assertRaises(ValueError):
                A.classify(refused)

    def test_closed_audit_scope_rejects_other_context_and_override(self):
        self.assertTrue(hasattr(A, "check_scope"), "audit context binding is missing")
        A.check_scope(["--context", "kind-owned", "-n", "orka-system", "get", "pods"], "kind-owned")
        for args in (["--context", "kind-other", "get", "pods"],
                     ["--context", "kind-owned", "--context=kind-other", "get", "pods"],
                     ["--context", "kind-owned", "--server=https://other", "get", "pods"]):
            with self.assertRaises(ValueError):
                A.check_scope(args, "kind-owned")

    def test_oci_blob_digest_is_checked_before_consuming_json(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            (root / "blobs/sha256").mkdir(parents=True)
            raw = b'{"schemaVersion":2}'
            digest = "sha256:" + hashlib.sha256(raw).hexdigest()
            blob = root / "blobs/sha256" / digest.split(":")[1]
            blob.write_bytes(raw)
            self.assertEqual(Q.read_blob(root, {"digest": digest, "size": len(raw)}), raw)
            blob.write_bytes(b'{"schemaVersion":3}')
            with self.assertRaises(ValueError):
                Q.read_blob(root, {"digest": digest, "size": len(raw)})


class CandidateProofTests(unittest.TestCase):
    def fixture(self, cases=None, subject_changes=None):
        run = "a" * 32
        subject = {"Suite": "suite", "SuiteDigest": "sha256:" + "1" * 64, "Member": "member", "PhysicalMember": "kmx-eval-" + "1" * 16 + "-" + run,
                   "EvaluationRun": run, "Renderer": "kmx.native-orka/v1", "BindingDigest": "b" * 64, "RenderedDigest": "d" * 64}
        subject.update(subject_changes or {})
        member = subject["PhysicalMember"]
        markers = dict(zip(Q.MARKERS, [subject[k] for k in ("Suite", "SuiteDigest", "Member", "Renderer", "BindingDigest", "RenderedDigest")]))
        markers["kaimahi.dev/suite-evaluation-run"] = run
        specs = {"Provider": {"type": "openai"}, "Agent": {"systemPrompt": {"inline": "PRIVATE_INSTRUCTIONS"}}}
        cases = cases or {"one.yaml": b'id: arithmetic\ninput: What is 2 + 2?\nexpectContains: ["4"]\n'}
        rows = [{"taskName": "member-eval-" + suffix, "taskUID": "task-uid" if i == 0 else "second-task-uid"}
                for i, suffix in enumerate(("one", "two")[:len(cases)])]
        receipt = {"runID": run, "candidateCleanup": "complete", "subject": subject, "cases": rows}
        frames = []
        for i, kind in enumerate(("Provider", "Agent") + ("Task",) * len(rows)):
            row = rows[i - 2] if kind == "Task" else None
            doc = task() if kind == "Task" else {"apiVersion": "core.orka.ai/v1alpha1", "kind": kind, "metadata": {"name": member}, "spec": specs[kind]}
            meta = doc["metadata"]
            meta.update(namespace="orka-system", uid=kind.lower() + "-uid" if kind != "Task" else row["taskUID"], generation=1,
                        resourceVersion="19", annotations=markers)
            if kind == "Task":
                meta["name"] = row["taskName"]
                doc["spec"]["agentRef"]["name"] = member
                doc["spec"]["prompt"] = Q.yaml.safe_load(list(cases.values())[i - 2])["input"]
            else:
                doc["status"] = {"ready": True, "conditions": [{"type": "Ready", "status": "True", "observedGeneration": 1}]}
                receipt[kind.lower()] = {"UID": meta["uid"], "Generation": 1, "SpecDigest": A.spec_digest(doc["spec"])}
            request = copy.deepcopy(doc)
            request.pop("status", None)
            for field in ("uid", "generation", "resourceVersion"):
                request["metadata"].pop(field)
            create = A.classify(["create", "-f", "-", "-o", "json"], json.dumps(request).encode())
            pending = copy.deepcopy(doc); pending["status"] = None
            frames.append({**create, "returnCode": 0, "response": A.response_snapshot(json.dumps(pending).encode())})
            meta["resourceVersion"] = "20"
            frames.append({**A.classify(["get", kind.lower() + "s.core.orka.ai", meta["name"], "-o", "json"]),
                           "returnCode": 0, "response": A.response_snapshot(json.dumps(doc).encode())})
        frames += [{**A.classify(["create", "token", "orka-result-reader"]), "returnCode": 0}]
        deletes = [("Task", row["taskName"], row["taskUID"]) for row in rows]
        deletes += [(kind, member, receipt[kind.lower()]["UID"]) for kind in ("Agent", "Provider")]
        for kind, name, uid in deletes:
            body = {"apiVersion": "v1", "kind": "DeleteOptions", "preconditions": {"uid": uid, "resourceVersion": "20"}, "propagationPolicy": "Foreground"}
            path = "/apis/core.orka.ai/v1alpha1/namespaces/orka-system/" + kind.lower() + "s/" + name
            frames.append({**A.classify(["delete", "--raw", path, "-f", "-"], json.dumps(body).encode()), "returnCode": 0,
                           "response": {"apiVersion": "v1", "kind": "Status", "status": "Success"}})
        return receipt, frames, cases, specs


    def test_candidate_protocol_requires_all_three_run_markers_and_historical_reads(self):
        self.assertTrue(hasattr(Q, "verify_candidate"), "historical candidate verifier missing")
        receipt, frames, cases, specs = self.fixture()
        tasks = Q.verify_candidate(receipt, frames, cases, specs)
        self.assertEqual(tasks[0]["uid"], "task-uid")
        self.assertNotIn("PRIVATE_INSTRUCTIONS", json.dumps(tasks))
        for kind in ("Provider", "Agent", "Task"):
            changed = copy.deepcopy(frames)
            for f in changed:
                if f.get("response", {}).get("kind") == kind:
                    f["response"]["metadata"]["annotations"].pop("kaimahi.dev/suite-evaluation-run", None)
            with self.subTest(kind=kind), self.assertRaisesRegex(ValueError, "candidate-.*identity"):
                Q.verify_candidate(receipt, changed, cases, specs)
        for edit in (lambda fs: fs[-1]["preconditions"].update(resourceVersion="stale"),
                     lambda fs: fs[-1]["preconditions"].update(uid="foreign"),
                     lambda fs: fs[-1]["response"].update(status="Failure"),
                     lambda fs: fs.pop(5), lambda fs: fs[5]["response"].update(phase="Running"),
                     lambda fs: fs[4]["response"].update(inputDigest="0" * 64),
                     lambda fs: fs.append(dict(fs[4]))):
            changed = copy.deepcopy(frames); edit(changed)
            with self.subTest(edit=edit), self.assertRaises(ValueError):
                Q.verify_candidate(receipt, changed, cases, specs)
        receipt["candidateCleanup"] = "pending"
        with self.assertRaisesRegex(ValueError, "candidate-cleanup"):
            Q.verify_candidate(receipt, frames, cases, specs)

    def test_independent_closure_links_task_job_pod_without_inherited_run_markers(self):
        self.assertTrue(hasattr(Q, "verify_child_absence"), "child closure verifier missing")
        def child(name, uid, kind, parent, parent_uid):
            return {"name": name, "uid": uid, "namespace": "orka-system", "resourceVersion": "20",
                    "ownerReferences": [{"apiVersion": "batch/v1" if kind == "Job" else "core.orka.ai/v1alpha1",
                                         "kind": kind, "name": parent, "uid": parent_uid, "controller": True}]}
        job = child("job", "job-uid", "Task", "task", "task-uid")
        pod = child("pod", "pod-uid", "Job", "job", "job-uid")
        cm = child("result", "cm-uid", "Task", "task", "task-uid")
        observed = {"Job": [job], "Pod": [pod], "ConfigMap": [cm]}
        empty = {k: [] for k in observed}
        self.assertEqual(Q.verify_child_absence(observed, empty, {"task": "task-uid"})["Pod"], {"pod": "pod-uid"})
        for kind in observed:
            current = copy.deepcopy(empty); current[kind] = observed[kind]
            with self.subTest(kind=kind), self.assertRaisesRegex(ValueError, "retained-child"):
                Q.verify_child_absence(observed, current, {"task": "task-uid"})
        wrong = copy.deepcopy(observed)
        wrong["Job"][0]["ownerReferences"][0]["uid"] = "foreign"
        self.assertEqual(Q.owned_children(wrong, {"task": "task-uid"})["Pod"], {})



    def test_cleanup_proof_independently_checks_roots_jobs_and_two_child_snapshots(self):
        self.assertTrue(hasattr(Q.Qualification, "cleanup_proof"), "independent cleanup proof missing")
        receipt, frames, cases, specs = self.fixture()
        task_name = receipt["cases"][0]["taskName"]
        job = {"name": "job", "namespace": "orka-system", "uid": "job-uid", "resourceVersion": "20",
               "ownerReferences": [{"apiVersion": "core.orka.ai/v1alpha1", "kind": "Task", "name": task_name,
                                    "uid": "task-uid", "controller": True}]}
        first_delete = next(i for i, f in enumerate(frames) if f["verb"] == "delete")
        frames.insert(first_delete, {"verb": "get", "response": {"kind": "JobList", "items": [job]}})
        q = Q.Qualification.__new__(Q.Qualification)
        q.audit = A
        q.live_subject = lambda s: None
        calls = []
        def kube(*args):
            calls.append(args)
            return b""
        q.kube = kube
        q.child_inventory = lambda kind: []
        counts = q.cleanup_proof(receipt["subject"], frames)
        self.assertEqual(counts, {"Job": 1, "Pod": 0, "ConfigMap": 0})
        self.assertEqual(len(calls), 3)
        q.kube = lambda *a: b'{"metadata":{"uid":"replacement"}}'
        with self.assertRaisesRegex(ValueError, "retained-candidate-root"):
            q.cleanup_proof(receipt["subject"], frames)
        q.kube = kube
        q.child_inventory = lambda kind: [job] if kind == "Job" else []
        with self.assertRaisesRegex(ValueError, "retained-child"):
            q.cleanup_proof(receipt["subject"], frames)
        q.child_inventory = lambda kind: []
        with self.assertRaisesRegex(ValueError, "pre-delete-job-uid-capture"):
            q.cleanup_proof(receipt["subject"], [f for f in frames if f.get("response", {}).get("kind") != "JobList"])

    def test_child_inventory_escapes_tokens_and_refuses_unbounded_or_changed_snapshots(self):
        self.assertTrue(hasattr(Q.Qualification, "child_inventory"), "bounded independent child inventory missing")
        q = Q.Qualification.__new__(Q.Qualification)
        q.audit = A
        calls = []
        def page(token="", rv="20"):
            return json.dumps({"apiVersion": "batch/v1", "kind": "JobList", "metadata": {"resourceVersion": rv, "continue": token},
                               "items": []}).encode()
        responses = iter([page("opaque/?&=+ token"), page()])
        def kube(*args):
            calls.append(args)
            return next(responses)
        q.kube = kube
        self.assertEqual(q.child_inventory("Job"), [])
        self.assertEqual(calls, [("get", "--raw", "/apis/batch/v1/namespaces/orka-system/jobs?limit=128"),
                                 ("get", "--raw", "/apis/batch/v1/namespaces/orka-system/jobs?limit=128&continue=opaque%2F%3F%26%3D%2B+token")])
        for pages in ([page("next"), page(rv="21")], [page("repeated"), page("repeated")],
                      [page(str(i)) for i in range(4)]):
            responses = iter(pages)
            with self.subTest(pages=pages), self.assertRaises(ValueError):
                q.child_inventory("Job")


class NativeJudgeTests(unittest.TestCase):
    def test_judge_receipt_matches_go_identity_contract_and_refuses_tampering(self):
        import yaml
        version = "aikit-qwen3.5-2b@sha256:d838c5eebf533b5b73f67cc7f4984921f87d64d28e4a52d482740b667f46b8b4"
        rubric = ("Score 100 for a scientifically correct explanation that warmer air is less dense than cooler surrounding air and rises through buoyancy. "
                  "Score 0 if the answer claims warmer air is denser or gravity pulls it upward. Equivalent wording is acceptable; do not require a particular sentence.")
        case = {"id": "explanation", "input": "Briefly explain why hot air rises.", "assertions": [
            {"id": "science", "type": "llmJudge", "rubric": rubric, "minScore": 70,
             "judgeModel": "qwen-3.5-2b", "judgeVersion": version}]}
        raw = yaml.safe_dump(case).encode()
        evidence = {"requestStyle": "openai-chat-completions-v1", "model": "qwen-3.5-2b", "modelVersion": version, "provenance": "user-declared", "minScore": 70,
                    "evaluatorDigest": "2866b5c14b0efa8dd13964a40030ffd79877bba4dc00405c1121000113adf793",
                    "configurationDigest": "b7ce49e0b23890067e384d903c5deb6425e2909c300411898f2773fa2bd16579", "scores": [90, 80], "modelMatches": ["requested", "version-qualified"]}
        row = {"id": "explanation", "path": "evals/greeter/cases/explanation.json", "caseDigest": hashlib.sha256(Q.packaged_case(raw)).hexdigest(),
               "inputDigest": hashlib.sha256(case["input"].encode()).hexdigest(), "verdict": "pass", "assertions": [
                   {"id": "science", "type": "llmJudge", "definitionDigest": "d58d7753601ecd06b6ace92c80c60fc5078f5eaab749f74fbf81cbcc417c5578",
                    "evaluatorVersion": "kmx.llm-judge/v2", "verdict": "pass", "judge": evidence}]}
        Q.verify_cases([row], {"03-explanation.yaml": raw}, "greeter")
        for field, value in (("modelMatches", None), ("modelMatches", []), ("modelMatches", ["prefix", "requested"]), ("requestStyle", "legacy"), ("configurationDigest", "0" * 64), ("modelVersion", "changed"), ("minScore", 60),
                             ("scores", [90, 69]), ("scores", [90, True]), ("reasoning", "PRIVATE_CASE_CANARY")):
            with self.subTest(field=field, value=value):
                changed = copy.deepcopy(row)
                changed["assertions"][0]["judge"][field] = value
                with self.assertRaises(ValueError):
                    Q.verify_cases([changed], {"03-explanation.yaml": raw}, "greeter")

    def test_native_inventory_keeps_text_probe_first_and_declares_pinned_judge(self):
        import yaml
        cases = Q.native_cases(ROOT / "internal/kmx/app/testdata/live-eval-loop")
        self.assertEqual(list(cases), ["01-capital.yaml", "02-arithmetic.yaml", "03-explanation.yaml"])
        self.assertEqual(yaml.safe_load(cases["01-capital.yaml"])["id"], "capital-france")
        definition = yaml.safe_load(cases["03-explanation.yaml"])["assertions"][0]
        self.assertEqual((definition["type"], definition["minScore"], definition["judgeModel"]), ("llmJudge", 70, "qwen-3.5-2b"))
        self.assertTrue(definition["judgeVersion"].endswith("d838c5eebf533b5b73f67cc7f4984921f87d64d28e4a52d482740b667f46b8b4"))

    def test_forward_owns_only_its_child_and_propagates_eval_timeout_without_retry(self):
        for timeout in (False, True):
            with self.subTest(timeout=timeout), tempfile.TemporaryDirectory() as directory:
                q = Q.Qualification.__new__(Q.Qualification)
                q.work, q.context, q.kubectl, q.kmx = pathlib.Path(directory), "kind-owned", "/fixture/kubectl", "/fixture/kmx"
                q.env, q.product_env = {}, {}
                forward, child = mock.MagicMock(), mock.MagicMock()
                forward.__enter__.return_value, child.__enter__.return_value = forward, child
                forward.poll.return_value, child.returncode, child.pid = None, 0, 987123
                child.communicate.return_value = (b"ok", b"")
                if timeout:
                    child.communicate.side_effect = subprocess.TimeoutExpired("synthetic-eval", 660)
                response = io.BytesIO(b"{}"); response.status = 200
                with mock.patch.object(Q.subprocess, "Popen", side_effect=[forward, child]) as spawn, \
                     mock.patch.object(Q.pathlib.Path, "read_bytes", return_value=b"Forwarding from 127.0.0.1:18091 -> 8080\n"), \
                     mock.patch.object(Q.urllib.request, "urlopen", return_value=response), \
                     mock.patch.object(Q.os, "killpg") as kill:
                    def execute():
                        with q.model_forward():
                            return q.evaluation(q.work / "workspace", q.work / "passing.json")
                    if timeout:
                        with self.assertRaises(subprocess.TimeoutExpired): execute()
                        kill.assert_called_once_with(987123, Q.signal.SIGKILL)
                    else:
                        self.assertEqual(execute().returncode, 0)
                        kill.assert_not_called()
                    self.assertEqual(spawn.call_count, 2)
                    forward.terminate.assert_called_once_with()
                    forward.wait.assert_called_once_with(timeout=10)
                    child.communicate.assert_called_once_with(None, timeout=660)
                    invocation = spawn.call_args_list[1].args[0]
                    self.assertEqual(invocation[invocation.index("--judge-base-url") + 1], "http://127.0.0.1:18091/v1")
                    self.assertEqual(invocation[invocation.index("--case-timeout") + 1], "5m")


class AuditTests(unittest.TestCase):
    def setUp(self):
        self.assertIsNotNone(A, "native product audit is not implemented")


    def test_bounded_root_and_child_raw_lists_have_closed_paths(self):
        paths = ["/apis/core.orka.ai/v1alpha1/namespaces/orka-system/" + p for p in ("providers", "agents", "tasks")]
        paths += ["/apis/batch/v1/namespaces/orka-system/jobs", "/api/v1/namespaces/orka-system/pods",
                  "/api/v1/namespaces/orka-system/configmaps"]
        for path in paths:
            for suffix in ("?limit=128", "?limit=128&continue=opaque%2F%3F%26%3D%2B+token"):
                f = A.classify(["--context", "kind-owned", "get", "--raw", path + suffix])
                self.assertFalse(f["persistent"])
        for path in (paths[0] + "?limit=129", paths[0] + "?limit=128&watch=true",
                     paths[0].replace("orka-system", "foreign") + "?limit=128",
                     paths[0] + "?limit=128&continue=", paths[0] + "?limit=128&limit=128",
                     "/api/v1/namespaces/orka-system/secrets?limit=128"):
            with self.subTest(path=path), self.assertRaises(ValueError):
                A.classify(["get", "--raw", path])

    def test_actual_raw_delete_options_not_fictitious_flags(self):
        path = "/apis/core.orka.ai/v1alpha1/namespaces/orka-system/tasks/task-one"
        doc = {"apiVersion": "v1", "kind": "DeleteOptions", "propagationPolicy": "Foreground",
               "preconditions": {"uid": "task-uid", "resourceVersion": "19"}}
        f = A.classify(["delete", "--raw", path, "-f", "-"], json.dumps(doc).encode())
        self.assertEqual(f["preconditions"], doc["preconditions"])
        self.assertTrue(f["persistent"])
        self.assertFalse(f["admission"] or f["taskAttempt"])
        for edit in (lambda d: d["preconditions"].pop("resourceVersion"),
                     lambda d: d.update(propagationPolicy="Background"), lambda d: d.update(gracePeriodSeconds=0)):
            changed = copy.deepcopy(doc); edit(changed)
            with self.assertRaises(ValueError):
                A.classify(["delete", "--raw", path, "-f", "-"], json.dumps(changed).encode())
        with self.assertRaises(ValueError):
            A.classify(["delete", "tasks.core.orka.ai", "task-one", "--preconditions=uid=task-uid"])
        with self.assertRaises(ValueError):
            A.classify(["delete", "--raw", path.replace("/tasks/", "/secrets/"), "-f", "-"], json.dumps(doc).encode())

    def test_real_wrapper_projects_private_responses_and_forwards_original_bytes(self):
        doc = task()
        doc["metadata"].update(resourceVersion="19", annotations={"kaimahi.dev/suite-evaluation-run": "a" * 32,
                                                                "foreign/payload": "PRIVATE_MARKER"})
        doc["status"]["answer"] = "PRIVATE_ANSWER"
        raw = json.dumps(doc).encode()
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            wrapper, real, audit = root / "kubectl", root / "real-kubectl", root / "audit.jsonl"
            wrapper.symlink_to(ROOT / "scripts/ci/suite-native-audit.py")
            real.write_text("#!" + sys.executable + "\nimport sys\nsys.stdout.buffer.write(" + repr(raw) + ")\n")
            real.chmod(0o700)
            env = dict(os.environ, KMX_NATIVE_KUBECTL=str(real), KMX_NATIVE_CONTEXT="kind-owned", KMX_NATIVE_AUDIT=str(audit))
            result = subprocess.run([str(wrapper), "--context", "kind-owned", "get", "tasks.core.orka.ai",
                                     "member-eval-one", "-o", "json"], env=env, capture_output=True, timeout=10)
            self.assertEqual((result.returncode, result.stdout), (0, raw))
            frames = A.completed([json.loads(line) for line in audit.read_text().splitlines()])
            self.assertIn("response", frames[0], "historical server snapshot missing")
            self.assertEqual(frames[0]["response"]["metadata"]["annotations"], {"kaimahi.dev/suite-evaluation-run": "a" * 32})
            self.assertNotIn("PRIVATE_", audit.read_text())
            self.assertNotIn("What is", audit.read_text())
            frames[0]["response"]["metadata"]["annotations"]["kaimahi.dev/suite-evaluation-run"] = "PRIVATE_PAYLOAD"
            with self.assertRaises(ValueError):
                A.validate_response(frames[0]["response"])


    def test_response_projection_accepts_root_lists_and_delete_ack_without_payload(self):
        self.assertTrue(hasattr(A, "response_snapshot"), "metadata response projection missing")
        doc = task()
        doc["metadata"].update(resourceVersion="19")
        root_list = {"apiVersion": "core.orka.ai/v1alpha1", "kind": "TaskList", "metadata": {"resourceVersion": "20"},
                     "items": [doc]}
        observed = A.response_snapshot(json.dumps(root_list).encode())
        A.validate_response(observed)
        self.assertEqual(observed["items"][0]["uid"], "task-uid")
        self.assertNotIn("What is", json.dumps(observed))
        status = A.response_snapshot(b'{"apiVersion":"v1","kind":"Status","status":"Success","message":"PRIVATE_ERROR"}')
        self.assertEqual(status, {"apiVersion": "v1", "kind": "Status", "status": "Success"})
        A.validate_response(status)
        for edit in (lambda d: d.update(apiVersion="PRIVATE_API_PAYLOAD"),
                     lambda d: d.update(agentRef={"name": "PRIVATE PROMPT", "namespace": "orka-system"}),
                     lambda d: d["metadata"].update(taskLabel={"data": "PRIVATE"})):
            changed = A.response_snapshot(json.dumps(doc).encode()); edit(changed)
            with self.assertRaises(ValueError):
                A.validate_response(changed)
        for key, value in (("continueBytes", 5000), ("resourceVersion", "PRIVATE PAYLOAD")):
            bad = copy.deepcopy(observed); bad[key] = value
            with self.subTest(key=key), self.assertRaises(ValueError):
                A.validate_response(bad)

    def test_delete_completions_are_serial_and_retained_response_cannot_change_intent(self):
        self.assertTrue(hasattr(A, "verify_cleanup_order"), "serial raw deletion verifier missing")
        path = "/apis/core.orka.ai/v1alpha1/namespaces/orka-system/tasks/task-one"
        raw = b'{"apiVersion":"v1","kind":"DeleteOptions","propagationPolicy":"Foreground","preconditions":{"uid":"uid","resourceVersion":"19"}}'
        f = A.classify(["delete", "--raw", path, "-f", "-"], raw)
        ack = {"apiVersion": "v1", "kind": "Status", "status": "Success"}
        def request(identity):
            return [{**f, "requestID": identity, "stage": "intent"},
                    {**f, "requestID": identity, "stage": "complete", "returnCode": 0, "response": ack}]
        one, two = request("one"), request("two")
        A.verify_cleanup_order([*one, *two])
        for frames in ([one[0], two[0], one[1], two[1]], one[:1],
                       [one[0], {**one[1], "returnCode": 1}], [one[0], {**one[1], "responseError": True}]):
            with self.subTest(frames=frames), self.assertRaises(ValueError):
                A.verify_cleanup_order(frames)

    def test_persistent_create_frame_has_no_payload(self):
        document = task()
        raw = json.dumps(document).encode()
        frame = A.classify(["--context", "kind-kmx-orka", "-n", "orka-system", "create", "-f", "-", "-o", "json"], raw)
        self.assertEqual((frame["verb"], frame["resource"], frame["name"], frame["persistent"]),
                         ("create", "Task", "member-eval-one", True))
        self.assertNotIn("What is", json.dumps(frame))
        with self.assertRaises(ValueError):
            A.classify(["create", "-f", "-"], b'{"kind":"Secret","metadata":{"name":"key"},"data":{"key":"private"}}')

    def test_executable_wrapper_records_success_not_private_output(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            wrapper = root / "kubectl"
            wrapper.symlink_to(ROOT / "scripts/ci/suite-native-audit.py")
            real = root / "real-kubectl"
            real.write_text('#!/usr/bin/env python3\nprint("PRIVATE_API_CANARY")\n')
            real.chmod(0o700)
            audit = root / "audit.jsonl"
            env = dict(os.environ, KMX_NATIVE_KUBECTL=str(real), KMX_NATIVE_CONTEXT="kind-owned", KMX_NATIVE_AUDIT=str(audit))
            result = subprocess.run([str(wrapper), "--context", "kind-owned", "get", "pods"], env=env, capture_output=True, timeout=10)
            self.assertEqual(result.returncode, 0)
            self.assertEqual(result.stdout, b"PRIVATE_API_CANARY\n")  # private parent capture only
            frames = A.completed([json.loads(line) for line in audit.read_text().splitlines()])
            self.assertEqual(frames[0].get("returnCode"), 0, "audit must record actual command success")
            self.assertNotIn("PRIVATE_API_CANARY", audit.read_text())
            builder = root / "docker"
            builder.symlink_to(ROOT / "scripts/ci/suite-native-audit.py")
            denied = subprocess.run([str(builder), "build", "PRIVATE_PROMPT_CANARY"], env=env, capture_output=True, timeout=10)
            self.assertEqual(denied.returncode, 1)
            self.assertNotIn(b"PRIVATE_PROMPT_CANARY", denied.stdout + denied.stderr + audit.read_bytes())

    def test_forward_accepts_actual_library_argument_order(self):
        try:
            frame = A.classify(["-n", "orka-system", "port-forward", "--address", "127.0.0.1", "svc/orka-api", "19180:8080"])
        except ValueError:
            self.fail("actual owned-forward argument order was refused")
        self.assertEqual((frame["resource"], frame["name"]), ("service", "orka-api"))

    def test_secret_key_presence_allowed_but_value_projection_denied(self):
        safe = 'go-template=secret\n{{range $key, $_ := .data}}{{if eq $key "api-key"}}present{{end}}{{end}}'
        A.classify(["get", "secret", "local-provider-key", "-o", safe])
        for output in ("json", "yaml", "jsonpath={.data.api-key}", "go-template={{.data}}", safe + "{{.data}}"):
            with self.subTest(output=output), self.assertRaises(ValueError):
                A.classify(["get", "secret", "local-provider-key", "-o", output])



if __name__ == "__main__":
    unittest.main()
