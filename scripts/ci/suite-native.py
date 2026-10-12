#!/usr/bin/env python3
"""Owned kind/AIKit infrastructure and actual CLI qualification, never fake Tasks."""
import hashlib
import json
import os
import pathlib
import re
import shutil
import signal
import subprocess
import sys
import tarfile
import tempfile
import time
import urllib.request
import urllib.error
import uuid

ROOT = pathlib.Path(__file__).resolve().parents[2]
CLUSTER = "kmx-suite-native"
CONTEXT = "kind-" + CLUSTER
NAMESPACE = "orka-system"
AIKIT_IMAGE = "ghcr.io/kaito-project/aikit/qwen3.5:2b@sha256:d838c5eebf533b5b73f67cc7f4984921f87d64d28e4a52d482740b667f46b8b4"
AIKIT_ID = "sha256:07a421e27f3e3dc265803fbe04995b27ed07c91b62452439a7253224ad9d4902"
NODE_IMAGE = "kindest/node:v1.35.0@sha256:4613778f3cfcd10e615029370f5786704559103cf27bef934597ba562b269661"
CHART_URL = "https://github.com/orka-agents/orka/releases/download/v0.2.0/orka-0.2.0.tgz"
CHART_SHA = "b7596c4e35d7189a3b2cf25921cd50e6c31328dbb83bdee50e20c757e0f03b79"
CONTROLLER = "ghcr.io/orka-agents/orka@sha256:7c1727f92d5c0cf05d464c6eb9e70b8342cb5a7a87c2e35338f051d7b85aa370"
WORKER = "ghcr.io/orka-agents/orka/ai-worker@sha256:814d7149308e62730df59759c5137667cad04520c94cc50ff56e5028fdc8bcd4"
OWNER_LABEL = "kmx.kaimahi.ai/native-qualification"


def require_absent(clusters):
    if CLUSTER in clusters:
        raise ValueError("preexisting-cluster")


def owned_node(node, identity, owner):
    return (bool(identity) and node.get("Id") == identity and
            node.get("Config", {}).get("Labels", {}).get("io.x-k8s.kind.cluster") == CLUSTER and
            node.get("QualificationOwner") == owner)


def verify_image(info):
    if info.get("Id") != AIKIT_ID or "ghcr.io/kaito-project/aikit/qwen3.5@" + AIKIT_IMAGE.split("@", 1)[1] not in info.get("RepoDigests", []):
        raise ValueError("model-image-pin")


def private_environment(work, inherited):
    allowed = ("PATH", "DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH",
               "GOCACHE", "GOMODCACHE", "GOPROXY", "GOSUMDB", "RUNNER_TEMP", "TMPDIR")
    env = {k: inherited[k] for k in allowed if k in inherited}
    env.update(HOME=str(work / "home"), KUBECONFIG=str(work / "kubeconfig"),
               KUBECACHEDIR=str(work / "kube-cache"), KMX_HOME=str(work / "kmx-home"),
               KMX_TOOLCHAIN="off", KAIMAHI_CONFIRM=CONTEXT, NO_COLOR="1",
               DOCKER_CONFIG=str(work / "docker-config"),
               HELM_CONFIG_HOME=str(work / "helm-config"), HELM_CACHE_HOME=str(work / "helm-cache"),
               HELM_DATA_HOME=str(work / "helm-data"), GOWORK="off", PYTHONDONTWRITEBYTECODE="1")
    return env


def public_summary(receipt, digest):
    return {"schemaVersion": 1, "mode": "native-orka", "suiteDigest": receipt["subject"]["suiteDigest"],
            "receiptDigest": digest, "requiredImages": receipt["subject"]["requiredImages"],
            "cases": len(receipt["cases"]), "destinationEvaluated": False}


def remove_private_work(work):
    # Go module-cache directories are read-only. Only this freshly created
    # private temporary tree is made writable for deletion, never caller state.
    def writable_retry(function, path, _error):
        pathlib.Path(path).parent.chmod(0o700)
        if not pathlib.Path(path).is_symlink() and pathlib.Path(path).is_dir():
            pathlib.Path(path).chmod(0o700)
        function(path)
    shutil.rmtree(work, onexc=writable_retry)


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode()


def kubectl_verb(args):
    return next((a for a in args if a in {"get", "create", "apply", "replace", "patch", "delete", "port-forward", "auth", "config", "version", "rollout"}), "")


# Exact projections used by current native preparation, never arbitrary templates.
SECRET_METADATA_OUTPUT = 'go-template={"kind":{{printf "%q" .kind}},"metadata":{"name":{{printf "%q" .metadata.name}},"namespace":{{printf "%q" .metadata.namespace}},"uid":{{printf "%q" .metadata.uid}},"resourceVersion":{{printf "%q" .metadata.resourceVersion}}},"terminating":{{if .metadata.deletionTimestamp}}true{{else}}false{{end}}}'
SECRET_KEY_OUTPUT = 'go-template=secret\n{{range $key, $_ := .data}}{{if eq $key "api-key"}}present{{end}}{{end}}'
WORKER_METADATA_OUTPUT = 'go-template={"kind":{{printf "%q" .kind}},"metadata":{"name":{{printf "%q" .metadata.name}},"namespace":{{printf "%q" .metadata.namespace}},"uid":{{printf "%q" .metadata.uid}},"labels":{' + ",".join(json.dumps(k) + ':{{printf "%q" (index .metadata.labels ' + json.dumps(k) + ')}}' for k in ("helm.sh/chart", "app.kubernetes.io/instance", "app.kubernetes.io/name", "app.kubernetes.io/managed-by", "orka.ai/worker", "orka.ai/worker-trust")) + '}},"terminating":{{if .metadata.deletionTimestamp}}true{{else}}false{{end}}}'


def audit_arguments(args):
    # Parse only the flags this qualification actually uses. Short flag clusters,
    # aliases with duplicate effective keys and unknown future commands fail shut.
    aliases = {"-n": "namespace", "--namespace": "namespace", "-o": "output", "--output": "output", "-f": "filename"}
    options, operands = {}, []
    index = 0
    while index < len(args):
        arg = args[index]
        index += 1
        if not arg.startswith("-"):
            operands.append(arg)
            continue
        key, separator, value = arg.partition("=")
        key = aliases.get(key, key.removeprefix("--"))
        if key not in {"context", "request-timeout", "namespace", "output", "filename", "ignore-not-found", "dry-run", "validate", "duration", "address", "timeout"} or key in options:
            raise ValueError("audit-flag")
        if not separator:
            if index == len(args):
                raise ValueError("audit-flag-value")
            value = args[index]
            index += 1
        if not value or (value.startswith("-") and not (key == "filename" and value == "-")):
            raise ValueError("audit-flag-value")
        options[key] = value
    return options, operands


def audit_frame(args, data, expected=CONTEXT):
    if args == ["version", "--client"]:
        return {"verb": "version", "dryRun": False, "contextPinned": False, "persistentWrite": False}
    options, operands = audit_arguments(args)
    if options.pop("context", None) != expected or options.pop("request-timeout", "10s") != "10s" or not operands:
        raise ValueError("audit-context")
    verb = operands[0]
    frame = {"verb": verb, "dryRun": False, "contextPinned": True, "persistentWrite": False}
    if operands == ["config", "view"] and options == {"output": "json"}:
        return frame
    if operands == ["rollout", "status", "deploy/orka-api-controller"] and options == {"namespace": NAMESPACE, "timeout": "10s"}:
        return frame
    if operands == ["port-forward", "svc/orka-api", "19187:8080"] and options == {"namespace": NAMESPACE, "address": "127.0.0.1"}:
        return frame
    if operands == ["create", "token", "orka-result-reader"] and options == {"namespace": NAMESPACE, "duration": "10m", "output": "json"}:
        return frame | {"tokenRequest": True}
    if verb == "get":
        ns, output = options.pop("namespace", None), options.pop("output", None)
        if options.pop("ignore-not-found", "true") != "true" or options:
            raise ValueError("audit-read-flags")
        resource = operands[1] if len(operands) > 1 else ""
        name = operands[2] if len(operands) == 3 else ""
        allowed = False
        if len(operands) == 3 and ns is None:
            allowed = ((resource == "namespace" and name in ("kube-system", NAMESPACE) and output == "json") or (resource == "crd" and name in ("agents.core.orka.ai", "providers.core.orka.ai", "tasks.core.orka.ai", "tools.core.orka.ai") and output in ("name", "json")))
        elif ns == NAMESPACE:
            if len(operands) == 2:
                allowed = resource in ("deploy", "serviceaccounts", "services") and output == "json"
            elif len(operands) == 3:
                allowed = ((resource == "providers.core.orka.ai" and name in ("qualification-inference", "native-proof") and output == "json") or
                           (resource == "agents.core.orka.ai" and name == "native-proof" and output == "json") or
                           (resource == "secret" and name == "qualification-model-key" and output in (SECRET_METADATA_OUTPUT, SECRET_KEY_OUTPUT)) or
                           (resource == "serviceaccount" and name == "orka-api-ai-worker" and output == WORKER_METADATA_OUTPUT) or
                           (resource == "serviceaccount" and name == "orka-result-reader" and output == "name") or
                           (resource == "service" and name == "orka-api" and output == "json") or
                           (resource == "tasks.core.orka.ai" and re.fullmatch(r"native-proof-eval-[0-9a-f]{16}", name) is not None and output == "json"))
        if not allowed:
            raise ValueError("audit-read-domain")
        frame["resource"] = resource
        if resource == "tasks.core.orka.ai":
            frame["name"] = name
        return frame
    if operands not in (["create"], ["replace"]):
        raise ValueError("audit-command")
    dry = options.pop("dry-run", None)
    if dry not in (None, "server") or (verb == "replace" and dry != "server") or options != {"namespace": NAMESPACE, "filename": "-", "validate": "strict", "output": "json"} or len(data) > 4 << 20:
        raise ValueError("audit-write-flags")
    import yaml
    try:
        docs = list(yaml.safe_load_all(data))
    except yaml.YAMLError:
        raise ValueError("audit-document") from None
    if len(docs) != 1 or not isinstance(docs[0], dict):
        raise ValueError("audit-document")
    doc = docs[0]
    metadata, spec = doc.get("metadata"), doc.get("spec")
    if not isinstance(metadata, dict) or not isinstance(spec, dict):
        raise ValueError("audit-document")
    kind, name = doc.get("kind"), metadata.get("name")
    if doc.get("apiVersion") != "core.orka.ai/v1alpha1" or metadata.get("namespace") != NAMESPACE or not isinstance(name, str):
        raise ValueError("audit-write-domain")
    if kind in ("Provider", "Agent"):
        if name != "native-proof":
            raise ValueError("audit-write-name")
    elif kind == "Task":
        if re.fullmatch(r"native-proof-eval-[0-9a-f]{16}", name) is None or spec.get("type") != "ai" or spec.get("agentRef") != {"name": "native-proof", "namespace": NAMESPACE} or verb != "create" or dry is not None:
            raise ValueError("audit-task-domain")
    else:
        raise ValueError("audit-write-kind")
    return frame | {"dryRun": dry == "server", "persistentWrite": dry is None, "kind": kind, "name": name, "namespace": NAMESPACE}


def audit_kubectl(args):
    data = sys.stdin.buffer.read() if "-f" in args else None
    try:
        frame = audit_frame(args, data or b"", os.environ["KMX_SUITE_AUDIT_CONTEXT"])
        audit = pathlib.Path(os.environ["KMX_SUITE_AUDIT_FILE"])
        prior = [json.loads(line) for line in audit.read_bytes().splitlines()] if audit.exists() else []
        writes = [f for f in prior if f.get("persistentWrite")]
        if frame.get("persistentWrite") and any(f.get("kind") == frame["kind"] for f in writes):
            raise ValueError("audit-repeated-write")
        if frame.get("resource") == "tasks.core.orka.ai" and not any(f.get("kind") == "Task" and f.get("name") == frame["name"] for f in writes):
            raise ValueError("audit-unrecorded-task")
    except (ValueError, KeyError, IndexError, TypeError, OSError):
        return 96
    with open(os.environ["KMX_SUITE_AUDIT_FILE"], "ab") as out:
        out.write(canonical(frame) + b"\n")
    if data is not None:
        return subprocess.run([os.environ["KMX_SUITE_REAL_KUBECTL"], *args], input=data, check=False).returncode
    os.execv(os.environ["KMX_SUITE_REAL_KUBECTL"], ["kubectl", *args])


def bounded_run(command, *, timeout, input=None, env=None, cwd=None, grace=2):
    # Every command gets an owned process group, including its forwards. A
    # timeout or signal never leaves a child listening on a reusable port.
    process = subprocess.Popen(command, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                               stderr=subprocess.PIPE, env=env, cwd=cwd, start_new_session=True)
    try:
        stdout, stderr = process.communicate(input, timeout=timeout)
        return subprocess.CompletedProcess(command, process.returncode, stdout, stderr)
    finally:
        try:
            os.killpg(process.pid, signal.SIGTERM)
        except ProcessLookupError:
            pass
        try:
            process.wait(timeout=grace)
        except subprocess.TimeoutExpired:
            pass
        # The leader may have exited before a term-ignoring descendant.
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        process.communicate()


class NoModelRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, _request, _fp, _code, _message, _headers, _url):
        return None


def model_http(request, *, timeout):
    # Warmup stays on the owned loopback model, not a caller proxy or redirect.
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoModelRedirect())
    try:
        return opener.open(request, timeout=timeout)
    except urllib.error.HTTPError as error:
        error.close()
        raise


class Qualification:
    def __init__(self, artifact):
        self.artifact = artifact
        self.work = pathlib.Path(tempfile.mkdtemp(prefix="kmx-suite-native-", dir=os.environ.get("RUNNER_TEMP", "/tmp")))
        self.env = private_environment(self.work, os.environ)
        self.phase = "prerequisites"
        self.node_id = ""
        self.nodes = {}
        self.cluster_uid = ""
        self.namespace_uid = ""
        self.owner = "qualification-" + uuid.uuid4().hex[:12]
        self.model_id = ""
        self.evidence_verified = False
        self.cluster_create_attempted = False
        self.model_create_attempted = False
        self.summary = {"schemaVersion": 1, "qualified": False, "cleanup": "not-created"}
        for name in ("home", "bin", "kmx-home", "kube-cache", "helm-config", "helm-cache", "helm-data", "docker-config"):
            (self.work / name).mkdir(mode=0o700)
        (self.work / "docker-config/config.json").write_text('{"auths":{"ghcr.io":{}}}')
        self.log = open(self.work / "private.log", "ab")

    def run(self, *args, input=None, timeout=180, ok=(0,), env=None):
        result = bounded_run([str(a) for a in args], input=input, env=env or self.env, cwd=ROOT, timeout=timeout)
        self.log.write(result.stdout + result.stderr)
        self.log.flush()
        if result.returncode not in ok:
            known = ((b"kubectl preflight unavailable", "kubectl-preflight"),
                     (b"native preparation unavailable", "native-preparation"),
                     (b"native evidence subject unavailable", "evidence-subject"),
                     (b"target confirmation unavailable", "target-confirmation"),
                     (b"result authorization preflight unavailable", "result-preflight"),
                     (b"did not complete a full passing run", "incomplete-native-run"),
                     (b"has suite ownership markers", "legacy-suite-ownership-refused"),
                     (b"the object has been modified", "api-resource-version-conflict"),
                     (b"Forbidden", "api-forbidden"),
                     (b"admission webhook", "api-admission-refused"),
                     (b"publisher must be enabled", "chart-publisher-required"),
                     (b"timed out waiting", "readiness-timeout"),
                     (b"no such host", "dns-unavailable"),
                     (b"connection refused", "endpoint-unavailable"))
            self.summary["reason"] = next((code for text, code in known if text in result.stderr), "command-refused")
            raise ValueError("command-refused")
        return result

    def k(self, *args, **kwargs):
        return self.run(self.work / "bin/kubectl-real", "--context", CONTEXT, "--cache-dir", self.work / "kube-cache", *args, **kwargs)

    def obj(self, kind, name, namespace=NAMESPACE):
        return json.loads(self.k("-n", namespace, "get", kind, name, "-o", "json").stdout)

    def download(self, url, digest, path):
        with urllib.request.urlopen(url, timeout=60) as response:
            raw = response.read(100 << 20)
        if hashlib.sha256(raw).hexdigest() != digest:
            raise ValueError("download-digest")
        path.write_bytes(raw)
        return path

    def tools(self):
        self.phase = "pinned-tools"
        for tool in ("go", "docker", "python3"):
            if shutil.which(tool, path=self.env["PATH"]) is None:
                self.summary["reason"] = tool + "-missing"
                raise ValueError("missing-prerequisite")
        tools = (("kind", "https://kind.sigs.k8s.io/dl/v0.33.0/kind-linux-amd64", "aee6151561422756b764a4ae28e7f44cda5af5a9eead3cc9985112b1de8d8e0d"),
                 ("kubectl-real", "https://dl.k8s.io/release/v1.35.0/bin/linux/amd64/kubectl", "a2e984a18a0c063279d692533031c1eff93a262afcc0afdc517375432d060989"))
        for name, url, digest in tools:
            self.download(url, digest, self.work / "bin" / name).chmod(0o700)
        archive = self.download("https://get.helm.sh/helm-v3.20.0-linux-amd64.tar.gz", "dbb4c8fc8e19d159d1a63dda8db655f9ffa4aac1b9a6b188b34a40957119b286", self.work / "helm.tgz")
        with tarfile.open(archive) as tar:
            raw = tar.extractfile("linux-amd64/helm").read()
        (self.work / "bin/helm").write_bytes(raw)
        (self.work / "bin/helm").chmod(0o700)
        self.env["PATH"] = str(self.work / "bin") + ":" + self.env["PATH"]
        # Reuse setup-go/operator Go caches without exposing their paths or
        # inheriting kubeconfig, Helm, registry or model credentials.
        cache_env = self.env | {"HOME": os.environ.get("HOME", self.env["HOME"])}
        caches = self.run("go", "env", "GOCACHE", "GOMODCACHE", env=cache_env).stdout.decode().splitlines()
        if len(caches) != 2 or not all(pathlib.Path(p).is_absolute() for p in caches):
            raise ValueError("go-cache-prerequisite")
        self.env["GOCACHE"], self.env["GOMODCACHE"] = caches
        self.phase = "build-current-cli"
        self.run("go", "build", "-o", self.work / "kmx", "./cmd/kmx", timeout=300)

    def cluster_node_ids(self):
        raw = self.run("docker", "ps", "-a", "--no-trunc", "--filter", "label=io.x-k8s.kind.cluster=" + CLUSTER, "--format", "{{.ID}}").stdout.decode().splitlines()
        if len(raw) != len(set(raw)) or any(re.fullmatch(r"[0-9a-f]{64}", identity) is None for identity in raw):
            raise ValueError("cluster-inventory-unavailable")
        return set(raw)

    def infrastructure(self):
        self.phase = "cluster-absent"
        require_absent(self.run("kind", "get", "clusters").stdout.decode().splitlines())
        if self.cluster_node_ids():
            raise ValueError("preexisting-cluster-container")
        self.phase = "kind-create"
        self.cluster_create_attempted = True
        self.run("kind", "create", "cluster", "--retain", "--name", CLUSTER, "--image", NODE_IMAGE, "--wait", "180s", timeout=300)
        # Successful create alone is not enough for later deletion authority.
        node = json.loads(self.run("docker", "inspect", CLUSTER + "-control-plane").stdout)[0]
        name = CLUSTER + "-control-plane"
        identity = node["Id"]
        if node.get("Config", {}).get("Labels", {}).get("io.x-k8s.kind.cluster") != CLUSTER or node.get("Name") != "/" + name or self.cluster_node_ids() != {identity}:
            raise ValueError("node-inventory-identity")
        self.k("label", "node", name, OWNER_LABEL + "=" + self.owner)
        live = json.loads(self.k("get", "node", name, "-o", "json").stdout)["metadata"]
        if live.get("name") != name or not live.get("uid") or live.get("labels", {}).get(OWNER_LABEL) != self.owner:
            raise ValueError("node-live-identity")
        self.cluster_uid = self.obj("namespace", "kube-system")["metadata"]["uid"]
        if not self.cluster_uid:
            raise ValueError("cluster-uid-unavailable")
        self.node_id, self.nodes = identity, {identity: {"name": name, "uid": live["uid"]}}
        self.phase = "verified-chart"
        chart = self.download(CHART_URL, CHART_SHA, self.work / "orka.tgz")
        with tarfile.open(chart) as tar:
            members = [m for m in tar.getmembers() if m.name.startswith("orka/crds/") and m.isfile()]
            tar.extractall(self.work / "chart", members=members, filter="data")
        self.k("apply", "-f", self.work / "chart/orka/crds", timeout=120)
        self.k("wait", "--for=condition=Established", "--timeout=120s", "-f", self.work / "chart/orka/crds", timeout=150)
        self.phase = "orka-harness-v2"
        # The pinned chart requires Publisher and its SCM proxy in harness-v2.
        # These are Orka infrastructure, not member images. Empty ACP providers
        # disable built-in agent runtimes; no supervisor/application is built.
        values = {"fullnameOverride": "orka-api", "controller": {"mode": "harness-v2", "watchNamespace": NAMESPACE,
            "image": {"digest": CONTROLLER.split("@", 1)[1]}, "acpRuntime": {
                "codexImage": "", "claudeImage": "", "copilotImage": "", "opencodeImage": ""}},
            "workers": {"ai": {"image": {"digest": WORKER.split("@", 1)[1]}}},
            "publisher": {"image": {"digest": "sha256:596a074bac827e65c8d8813a6f4ac1ebac2bc130632bf0a185db6d521a1b1243"}},
            "providerProxy": {"enabled": False}}
        (self.work / "values.json").write_bytes(canonical(values))
        self.k("create", "namespace", NAMESPACE)
        self.namespace_uid = self.obj("namespace", NAMESPACE)["metadata"]["uid"]
        if not self.namespace_uid:
            raise ValueError("namespace-uid-unavailable")
        self.run("helm", "--kube-context", CONTEXT, "install", "orka", chart, "-n", NAMESPACE,
                 "-f", self.work / "values.json", "--wait", "--timeout", "10m", timeout=660)
        self.k("-n", NAMESPACE, "rollout", "status", "deployment/orka-api-controller", "--timeout=180s", timeout=210)
        self.phase = "result-rbac-prerequisite"
        env = self.env | {"KMX_LIVE_KIND_CONTEXT": CONTEXT}
        result = self.run("go", "test", "./internal/kmx/app", "-run", "^TestLiveProvisionOrkaResultReader$", "-count=1", "-v", "-timeout=2m", env=env, timeout=180)
        if b"--- PASS: TestLiveProvisionOrkaResultReader" not in result.stdout or b"--- SKIP:" in result.stdout:
            raise ValueError("result-prerequisite-not-exercised")

    def model(self):
        self.phase = "model-image-pin"
        info = self.run("docker", "image", "inspect", AIKIT_IMAGE, ok=(0, 1))
        if info.returncode:
            self.run("docker", "pull", AIKIT_IMAGE, timeout=600)
            info = self.run("docker", "image", "inspect", AIKIT_IMAGE)
        verify_image(json.loads(info.stdout)[0])
        self.phase = "model-startup"
        # Docker chooses a free loopback port; no caller listener is hijacked.
        self.model_create_attempted = True
        result = self.run("docker", "run", "-d", "--name", self.owner + "-aikit", "--label", OWNER_LABEL + "=" + self.owner,
                          "--network", "kind", "--cpus", "4", "--memory", "6g", "-e", "LOCALAI_FORCE_META_BACKEND_CAPABILITY=cpu",
                          "--mount", "type=bind,src=" + str(ROOT / "scripts/ci/eval-loop-model.yaml") + ",dst=/config.yaml,readonly",
                          "-p", "127.0.0.1::8080", AIKIT_IMAGE, "--config-file=/config.yaml", timeout=60)
        self.model_id = result.stdout.decode().strip()
        info = json.loads(self.run("docker", "inspect", self.model_id).stdout)[0]
        if info["Image"] != AIKIT_ID or info["Config"]["Cmd"] != ["--config-file=/config.yaml"]:
            raise ValueError("model-config-startup")
        ip = info["NetworkSettings"]["Networks"]["kind"]["IPAddress"]
        port = info["NetworkSettings"]["Ports"]["8080/tcp"][0]
        if port["HostIp"] != "127.0.0.1":
            raise ValueError("model-loopback")
        base = "http://127.0.0.1:" + port["HostPort"]
        deadline = time.monotonic() + 180
        while time.monotonic() < deadline:
            try:
                with model_http(base + "/readyz", timeout=5) as response:
                    if response.status == 200:
                        break
            except (OSError, ValueError):
                time.sleep(2)
        else:
            raise ValueError("model-not-ready")
        self.phase = "model-warmup-unscored"
        request = (ROOT / "scripts/ci/eval-loop-warmup.json").read_bytes()
        with model_http(urllib.request.Request(base + "/v1/chat/completions", data=request,
                                               headers={"Content-Type": "application/json"}), timeout=240) as response:
            warm = json.loads(response.read(1 << 20))
        if warm.get("model") != "qwen-3.5-2b" or not warm["choices"][0]["message"]["content"]:
            raise ValueError("model-warmup-unavailable")
        self.phase = "inference-prerequisite"
        secret = {"apiVersion": "v1", "kind": "Secret", "metadata": {"name": "qualification-model-key", "namespace": NAMESPACE},
                  "stringData": {"api-key": "not-used-by-this-endpoint"}}
        provider = {"apiVersion": "core.orka.ai/v1alpha1", "kind": "Provider", "metadata": {"name": "qualification-inference", "namespace": NAMESPACE},
                    "spec": {"type": "openai", "defaultModel": "deliberately-not-the-packaged-model", "baseURL": "http://" + ip + ":8080/v1",
                             "secretRef": {"name": "qualification-model-key", "key": "api-key"}}}
        for doc in (secret, provider):
            self.k("create", "-f", "-", input=canonical(doc))
        self.k("-n", NAMESPACE, "wait", "--for=condition=Ready", "provider/qualification-inference", "--timeout=120s", timeout=150)
        self.source = self.obj("provider", "qualification-inference")
        self.summary["sourceReady"] = {"generation": self.source["metadata"].get("generation"),
            "ready": self.source.get("status", {}).get("ready"), "conditions": [
                {k: c.get(k) for k in ("type", "status", "observedGeneration")} for c in self.source.get("status", {}).get("conditions", [])]}
        # Project Secret metadata only, never retain its data or value hashes.
        self.secret_metadata = self.k("-n", NAMESPACE, "get", "secret", "qualification-model-key", "-o",
            'go-template={{.metadata.uid}}:{{.metadata.resourceVersion}}').stdout

    def cli(self, *args, **kwargs):
        env = self.env
        if (self.work / "fail-bin").is_dir():
            env = self.env | {"PATH": str(self.work / "fail-bin") + ":" + self.env["PATH"]}
        return self.run(self.work / "kmx", *args, env=env, **kwargs)

    def evaluate_args(self, root, out):
        return ("agent", "evaluate", "--workspace", root, "--to-context", CONTEXT, "--to-namespace", NAMESPACE,
                "--inference", "provider:qualification-inference", "--out", out, "--case-timeout", "5m", "--result-port", "19187")

    def qualify(self):
        self.phase = "offline-author-and-prepare"
        root = self.work / "workspace"
        instructions = "Answer arithmetic questions with only the decimal integer. Do not explain.\n"
        (self.work / "instructions.md").write_text(instructions)
        self.cli("agent", "create", "native-proof", "--workspace", root, "--model", "qwen-3.5-2b", "--instructions", self.work / "instructions.md")
        cases = list((root / "eval").glob("*.yaml"))
        if len(cases) != 1:
            raise ValueError("authored-inventory")
        cases[0].write_text('id: arithmetic\ninput: "What is 2 + 2? Reply with only the integer."\nassertions:\n  - id: decimal-four\n    type: regex\n    pattern: "^\\\\s*4[.!]?\\\\s*$"\n')
        self.cli("suite", "push", "--workspace", root, "--to-layout", self.work / "prepared-transfer", "qualification:v1")
        initial = json.loads((self.work / "prepared-transfer/index.json").read_bytes())["manifests"][0]["digest"]
        self.phase = "actual-cli-native-full-evaluation"
        traps = self.work / "fail-bin"
        traps.mkdir(mode=0o700)
        for name in ("docker", "buildx", "helm", "kagent"):
            tool = traps / name
            tool.write_text('#!/bin/sh\n: > ' + str(self.work / "forbidden-tool-attempt") + '\nexit 96\n')
            tool.chmod(0o700)
        audit = self.work / "audit.jsonl"
        wrapper = self.work / "bin/kubectl"
        wrapper.write_text('#!' + sys.executable + '\nimport runpy,sys\nm=runpy.run_path(' + repr(str(ROOT / "scripts/ci/suite-native.py")) + ')\nsys.exit(m["audit_kubectl"](sys.argv[1:]))\n')
        wrapper.chmod(0o700)
        self.env.update(KMX_SUITE_AUDIT_CONTEXT=CONTEXT, KMX_SUITE_REAL_KUBECTL=str(self.work / "bin/kubectl-real"), KMX_SUITE_AUDIT_FILE=str(audit))
        receipt_path = self.work / "receipt.json"
        result = self.cli(*self.evaluate_args(root, receipt_path), timeout=660)
        receipt = json.loads(receipt_path.read_bytes())
        if receipt["subject"]["suiteDigest"] != initial or receipt["subject"]["requiredImages"] != []:
            raise ValueError("evaluated-package-substitution")
        # An exact metadata grammar, not searching for a known answer, protects
        # default output even if the model chooses unexpected response text.
        history = self.work / "kmx-home/suite-evidence/runs" / ("eval-" + receipt["runID"] + ".json")
        expected_stdout = "".join(c["id"] + ": " + c["verdict"] + "\n" for c in receipt["cases"])
        expected_stdout += ("suite evaluation full: pass\nS: " + initial + "\nrunID: " + receipt["runID"] +
                            "\nrequiredImages: []\nhistory: " + str(history) + "\nreceipt: " + str(receipt_path) + "\n")
        if result.stdout != expected_stdout.encode():
            raise ValueError("terminal-content-or-summary-shape")
        if self.source != self.obj("provider", "qualification-inference") or self.secret_metadata != self.k("-n", NAMESPACE, "get", "secret", "qualification-model-key", "-o", 'go-template={{.metadata.uid}}:{{.metadata.resourceVersion}}').stdout:
            raise ValueError("source-binding-mutated")
        provider = self.obj("provider", "native-proof")
        agent = self.obj("agent", "native-proof")
        if provider["spec"]["defaultModel"] != "qwen-3.5-2b" or agent["spec"]["systemPrompt"]["inline"] != instructions:
            raise ValueError("packaged-behavior-override")
        digest = "sha256:" + hashlib.sha256(receipt_path.read_bytes()).hexdigest()
        policy = {"schemaVersion": 1, "domain": "suite-promotion", "receiptDigest": digest, "runID": receipt["runID"],
                  "evaluated": receipt["subject"], "destination": receipt["subject"], "allowedTransitionPaths": []}
        policy_path = self.work / "promotion.json"
        policy_path.write_bytes(canonical(policy))
        self.phase = "strict-live-receipt-and-pure-policy"
        env = self.env | {"PATH": str(traps) + ":" + self.env["PATH"],
            "KMX_LIVE_KIND_CONTEXT": CONTEXT, "KMX_SUITE_NATIVE_WORKSPACE": str(root),
            "KMX_SUITE_NATIVE_RECEIPT": str(receipt_path), "KMX_SUITE_NATIVE_POLICY": str(policy_path),
            "KMX_SUITE_NATIVE_RESULT_PORT": "19187", "KMX_SUITE_NATIVE_PROOF_DIR": str(self.work)}
        result = self.run("go", "test", "./internal/kmx/app", "-run", "^TestLiveSuiteNativeEvidence$", "-count=1", "-v", "-timeout=2m", env=env, timeout=180)
        if b"--- PASS: TestLiveSuiteNativeEvidence" not in result.stdout or b"--- SKIP:" in result.stdout or not (self.work / "verified.json").is_file():
            raise ValueError("strict-live-proof-not-exercised")
        self.evidence_verified = True
        self.phase = "exact-s-publication-and-pull"
        self.cli("suite", "push", "--workspace", root, "--to-layout", self.work / "published", "qualification:v1")
        published = json.loads((self.work / "published/index.json").read_bytes())["manifests"][0]["digest"]
        if published != initial:
            raise ValueError("publisher-subject")
        self.cli("suite", "pull", "qualification:v1", "--from-layout", self.work / "published", "--output", self.work / "extracted")
        self.cli("suite", "validate", self.work / "extracted")
        for blob in (self.work / "prepared-transfer/blobs/sha256").iterdir():
            if blob.read_bytes() != (self.work / "published/blobs/sha256" / blob.name).read_bytes():
                raise ValueError("rearchived-package")
        self.phase = "source-pin-drift-negatives"
        import yaml
        member = root / "agents/native-proof/member.yaml"
        source_files = (cases[0], root / "agents/native-proof/instructions.md", member)
        for i, path in enumerate(source_files):
            original = path.read_bytes()
            if i == 2:
                doc = yaml.safe_load(original)
                doc["model"]["model"] = "changed-model"
                changed = yaml.safe_dump(doc).encode()
            else:
                changed = original + b"\n# deliberately changed authored input\n"
            path.write_bytes(changed)
            refused = self.cli("suite", "push", "--workspace", root, "--to-layout", self.work / ("drift-" + str(i)), "qualification:v1", ok=(1,))
            path.write_bytes(original)
            if b"rerun evaluation" not in refused.stderr + refused.stdout or (self.work / ("drift-" + str(i))).exists():
                raise ValueError("source-pin-drift-not-refused")
        self.phase = "ownership-zero-write-negatives"
        for mode in ("unmarked", "bundle", "mixed"):
            self.phase = "ownership-" + mode + "-prerequisite"
            changed = self.obj("provider", "native-proof")
            ann = changed["metadata"].get("annotations", {})
            if mode == "unmarked":
                ann = {k: v for k, v in ann.items() if not k.startswith("kaimahi.dev/suite")}
            else:
                if mode == "bundle":
                    ann = {k: v for k, v in ann.items() if not k.startswith("kaimahi.dev/suite")}
                ann["kaimahi.dev/bundle"] = "foreign"
            changed["metadata"]["annotations"] = ann
            # Own test prerequisite mutation, outside the audited CLI boundary.
            self.k("replace", "-f", "-", input=canonical(changed))
            observed = self.obj("provider", "native-proof")
            if observed["metadata"].get("annotations", {}) != ann or observed["metadata"]["uid"] != provider["metadata"]["uid"]:
                raise ValueError("ownership-prerequisite-not-observed")
            self.phase = "ownership-" + mode + "-cli-refusal"
            before = len(audit.read_bytes())
            refused = self.cli(*self.evaluate_args(root, self.work / (mode + ".json")), ok=(1,), timeout=120)
            frames = [json.loads(line) for line in audit.read_bytes()[before:].splitlines()]
            if any(f["verb"] in ("create", "apply", "replace", "patch", "delete") and not f["dryRun"] for f in frames):
                raise ValueError("ownership-refusal-wrote")
            if b"native preparation unavailable; no placement attempted" not in refused.stderr + refused.stdout:
                raise ValueError("ownership-refusal-wrong-diagnostic")
            self.phase = "ownership-" + mode + "-specific-read-only-refusal"
            checked = self.run("go", "test", "./internal/kmx/app", "-run", "^TestLiveSuiteNativeEvidence$", "-count=1", "-v", "-timeout=2m", env=env | {"KMX_SUITE_NATIVE_REFUSAL": mode}, timeout=180)
            if b"--- PASS: TestLiveSuiteNativeEvidence" not in checked.stdout or not (self.work / ("refused-" + mode + ".json")).is_file():
                raise ValueError("specific-ownership-refusal-not-observed")
            self.phase = "ownership-" + mode + "-restore-prerequisite"
            current = self.obj("provider", "native-proof")
            current["metadata"]["annotations"] = provider["metadata"]["annotations"]
            self.k("replace", "-f", "-", input=canonical(current))
        self.phase = "legacy-create-suite-ownership-refusal"
        before = len(audit.read_bytes())
        legacy = self.cli("--context", CONTEXT, "agent", "create", "native-proof", "--namespace", NAMESPACE, "--provider-type", "openai", "--model", "qwen-3.5-2b", "--secret", "qualification-model-key", "--base-url", self.source["spec"]["baseURL"], "--out", self.work / "legacy.yaml", "--bundle-path", self.work / "legacy", ok=(1,), timeout=120)
        if b"has suite ownership markers" not in legacy.stderr + legacy.stdout:
            raise ValueError("legacy-refusal-wrong-diagnostic")
        if any(f["persistentWrite"] for f in map(json.loads, audit.read_bytes()[before:].splitlines())):
            raise ValueError("legacy-refusal-wrote")
        self.phase = "metadata-audit"
        frames = [json.loads(line) for line in audit.read_bytes().splitlines()]
        writes = [f for f in frames if f["verb"] in ("create", "apply", "replace", "patch", "delete") and not f["dryRun"] and "kind" in f]
        if (self.work / "forbidden-tool-attempt").exists():
            raise ValueError("builder-or-installer-attempt")
        if [f["kind"] for f in writes] != ["Provider", "Agent", "Task"] or not all(f["contextPinned"] or f["verb"] == "version" for f in frames):
            raise ValueError("native-write-inventory")
        self.summary = public_summary(receipt, digest) | {"qualified": True, "modelImage": AIKIT_IMAGE,
            "modelImageID": AIKIT_ID, "cluster": CLUSTER, "context": CONTEXT, "nativeWrites": {"Provider": 1, "Agent": 1, "Task": 1},
            "auditScope": "native-cli-and-read-only-verification-not-infrastructure-prerequisites",
            "secretWrites": 0, "builderCalls": 0, "sourceDriftNegatives": 3, "ownershipNegatives": 4,
            "gateNegatives": 6, "warmupScored": False, "terminalJobLineageClaimed": False}
    def cleanup(self):
        clean = True
        try:
            # Establish authority for the COMPLETE recorded inventory before any
            # deletion. Never let kind reselect containers by a reusable label/name.
            if (self.cluster_create_attempted and not self.nodes) or (self.model_create_attempted and not self.model_id):
                self.summary["reason"] = "ambiguous-create-left-for-inspection"
                raise ValueError("ambiguous-create")
            if self.node_id:
                if set(self.nodes) != {self.node_id} or self.cluster_node_ids() != set(self.nodes) or not self.cluster_uid:
                    raise ValueError("cluster-cleanup-inventory")
                for identity, recorded in self.nodes.items():
                    node = json.loads(self.run("docker", "inspect", identity).stdout)[0]
                    live = json.loads(self.k("get", "node", recorded["name"], "-o", "json").stdout)["metadata"]
                    node["QualificationOwner"] = live.get("labels", {}).get(OWNER_LABEL)
                    if not owned_node(node, identity, self.owner) or node.get("Name") != "/" + recorded["name"] or live.get("name") != recorded["name"] or live.get("uid") != recorded["uid"]:
                        raise ValueError("cluster-cleanup-ownership")
                if self.obj("namespace", "kube-system")["metadata"]["uid"] != self.cluster_uid:
                    raise ValueError("cluster-cleanup-cluster-uid")
                if self.namespace_uid:
                    if self.obj("namespace", NAMESPACE)["metadata"]["uid"] != self.namespace_uid:
                        raise ValueError("cluster-cleanup-namespace-uid")
                elif self.k("get", "namespace", NAMESPACE, "--ignore-not-found=true", "-o", "json").stdout.strip():
                    raise ValueError("cluster-cleanup-unrecorded-namespace")
            if self.model_id:
                info = json.loads(self.run("docker", "inspect", self.model_id).stdout)[0]
                if info["Id"] != self.model_id or info["Config"]["Labels"].get(OWNER_LABEL) != self.owner:
                    raise ValueError("model-cleanup-ownership")
            if self.model_id:
                self.run("docker", "stop", "--time", "10", self.model_id)
                self.run("docker", "rm", self.model_id)
            for identity in self.nodes:
                # Immutable IDs cannot broaden to foreign nodes added after the
                # check. Remove only these containers/anonymous volumes; keep the
                # shared kind network and caller kubeconfig completely untouched.
                self.run("docker", "rm", "--force", "--volumes", identity, timeout=180)
            if self.nodes:
                if self.cluster_node_ids():
                    raise ValueError("cluster-cleanup-residual-inventory")
                require_absent(self.run("kind", "get", "clusters").stdout.decode().splitlines())
        except (ValueError, OSError, KeyError, IndexError, TypeError, subprocess.SubprocessError):
            clean = False
        audit = self.work / "audit.jsonl"
        if audit.is_file():
            shutil.copyfile(audit, self.artifact / "audit.jsonl")
        # Persist only evidence the actual strict Go verifier accepted. An
        # unreadable/unknown response is never an artifact-content authority.
        if self.evidence_verified:
            for name in ("receipt.json", "verified.json", "promotion.json"):
                if (self.work / name).is_file():
                    shutil.copyfile(self.work / name, self.artifact / name)
                    (self.artifact / name).chmod(0o600)
        self.summary["cleanup"] = ("owned-resources-removed" if self.node_id or self.model_id else "no-owned-resources") if clean else "refused-or-failed-inspect-owned-resources"
        self.summary["failedPhase"] = "" if self.summary.get("qualified") and clean else self.phase
        self.summary["qualified"] = self.summary.get("qualified", False) and clean
        (self.artifact / "qualification.json").write_bytes(canonical(self.summary))
        (self.artifact / "qualification.json").chmod(0o600)
        self.log.close()
        try:
            remove_private_work(self.work)
        except OSError:
            self.summary["cleanup"] = "private-work-removal-failed"
            self.summary["qualified"] = False
            (self.artifact / "qualification.json").write_bytes(canonical(self.summary))
            clean = False
        return clean


def main(argv):
    if argv:
        print("usage: suite-native.sh (SUITE_NATIVE_ARTIFACT_DIR must name a new directory)", file=sys.stderr)
        return 2
    artifact = os.environ.get("SUITE_NATIVE_ARTIFACT_DIR", "")
    if not artifact:
        print("native suite BLOCKED: phase=artifact-directory-required", file=sys.stderr)
        return 1
    qualifier = None
    os.umask(0o077)
    try:
        path = pathlib.Path(artifact)
        if not path.is_absolute() or path.resolve() != path or path.is_relative_to(ROOT) or not path.parent.is_dir():
            raise ValueError("artifact-directory")
        path.mkdir(mode=0o700)
        qualifier = Qualification(path)
        qualifier.tools()
        qualifier.infrastructure()
        qualifier.model()
        qualifier.qualify()
    except (ValueError, OSError, KeyError, IndexError, subprocess.SubprocessError) as error:
        if qualifier:
            if isinstance(error, subprocess.TimeoutExpired):
                qualifier.summary["reason"] = "command-timeout"
            elif isinstance(error, json.JSONDecodeError):
                qualifier.summary["reason"] = "metadata-decode-refused"
            print("native suite BLOCKED: phase=" + qualifier.phase + " reason=" + qualifier.summary.get("reason", "control-refused") + "; no qualification claimed", file=sys.stderr)
        else:
            print("native suite BLOCKED: phase=private-prerequisites", file=sys.stderr)
        return 1
    finally:
        # A second signal must not interrupt ownership verification/cleanup.
        signal.signal(signal.SIGTERM, signal.SIG_IGN)
        signal.signal(signal.SIGINT, signal.SIG_IGN)
        if qualifier and not qualifier.cleanup():
            print("native suite BLOCKED: phase=owned-cleanup", file=sys.stderr)
    if not qualifier.summary.get("qualified"):
        return 1
    print("native suite qualified: S=" + qualifier.summary["suiteDigest"] + " requiredImages=[] cleanup=owned-resources-removed")
    return 0


if __name__ == "__main__":
    signal.signal(signal.SIGTERM, lambda *_: sys.exit(143))
    signal.signal(signal.SIGINT, lambda *_: sys.exit(130))
    sys.exit(main(sys.argv[1:]))
