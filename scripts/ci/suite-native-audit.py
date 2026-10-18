#!/usr/bin/env python3
"""Finite kubectl boundary audit for the native-suite CI scenario; no payload log."""
import hashlib
import json
import os
import pathlib
import re
import subprocess
import sys
import uuid
import urllib.parse

SECRET_METADATA = 'go-template={"Kind":{{printf "%q" .kind}},"Metadata":{"Name":{{printf "%q" .metadata.name}},"Namespace":{{printf "%q" .metadata.namespace}},"UID":{{printf "%q" .metadata.uid}},"ResourceVersion":{{printf "%q" .metadata.resourceVersion}}},"Terminating":{{if .metadata.deletionTimestamp}}true{{else}}false{{end}}}'
PRESENCE = re.compile(r'go-template=secret\n\{\{range \$key, \$_ := \.data\}\}\{\{if eq \$key "[-._a-zA-Z0-9]+"\}\}present\{\{end\}\}\{\{end\}\}')
READS = {"namespace", "namespaces", "ns", "providers.core.orka.ai", "agents.core.orka.ai", "tasks.core.orka.ai",
         "secret", "serviceaccount", "serviceaccounts", "sa", "svc", "service", "services", "deployment", "deployments",
         "deploy", "pods", "pod", "crd", "customresourcedefinitions"}
VERBS = {"get", "create", "replace", "delete", "config", "version", "port-forward", "rollout", "auth", "patch"}
ROOTS = {"providers": "Provider", "agents": "Agent", "tasks": "Task"}
INVENTORIES = {"/apis/batch/v1/namespaces/orka-system/jobs": "Job",
               "/api/v1/namespaces/orka-system/pods": "Pod", "/api/v1/namespaces/orka-system/configmaps": "ConfigMap",
               **{"/apis/core.orka.ai/v1alpha1/namespaces/orka-system/" + p: k for p, k in ROOTS.items()}}


def check_scope(args, context):
    contexts = [a.split("=", 1)[1] for a in args if a.startswith("--context=")]
    contexts += [args[i + 1] for i, a in enumerate(args[:-1]) if a == "--context"]
    verb = next((a for a in args if a in VERBS), "")
    if verb not in {"config", "version"} and contexts != [context]:
        raise ValueError("audit-context")
    if any(a.split("=", 1)[0] in {"--server", "--token", "--as", "--as-group", "--username", "--password", "--kubeconfig"} for a in args):
        raise ValueError("audit-override")


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("audit-duplicate-key")
        result[key] = value
    return result


def raw_prefix(args, verb):
    prefix = args[:args.index(verb)]
    while prefix:
        option = prefix.pop(0)
        if option in {"--context", "-n"} and prefix:
            prefix.pop(0)
        elif option == "--request-timeout=10s" or option.startswith("--context="):
            continue
        else:
            raise ValueError("audit-raw-option")


RELEASE_NAME = re.compile(r"kmx-release-[0-9a-f]{40}")
OPAQUE = re.compile(r"[a-zA-Z0-9._:-]{1,128}")


def release_payload(data):
    if not isinstance(data, str) or len(data.encode()) > 64 << 10:
        raise ValueError("audit-record-size")
    state = json.loads(data, object_pairs_hook=unique_object)
    def check(ok):
        if not ok:
            raise ValueError("audit-record-schema")
    def digest(v):
        return isinstance(v, str) and re.fullmatch(r"sha256:[0-9a-f]{64}", v)
    def objects(rows):
        check(isinstance(rows, list) and len(rows) == 2)
        for kind, row in zip(("Provider", "Agent"), rows):
            check(isinstance(row, dict) and not set(row) - {"kind", "uid", "generation", "specDigest", "ownershipDigest", "liftRecordDigest"})
            check(row.get("kind") == kind and all(digest(row.get(k)) for k in ("specDigest", "ownershipDigest", "liftRecordDigest")))
            uid, gen = row.get("uid", ""), row.get("generation", 0)
            check(type(gen) is int and gen >= 0 and isinstance(uid, str) and ((not uid and gen == 0) or (OPAQUE.fullmatch(uid) and gen > 0)))
    check(isinstance(state, dict) and not set(state) - {"version", "suite", "member", "runtime", "target", "originLiftID", "operation", "baseline", "pending", "history"})
    check(state.get("version") == 1 and type(state["version"]) is int and state.get("runtime") == "orka")
    check(all(isinstance(state.get(k), str) and re.fullmatch(r"[a-z0-9][-a-z0-9.]{0,252}", state[k]) for k in ("suite", "member")))
    check(isinstance(state.get("originLiftID"), str) and re.fullmatch(r"[0-9a-f]{32}", state["originLiftID"]))
    target = state.get("target")
    check(isinstance(target, dict) and set(target) == {"context", "namespace", "clusterUID", "namespaceUID"})
    check(target["namespace"] == "orka-system" and all(isinstance(v, str) and re.fullmatch(r"[-a-zA-Z0-9._:/]{1,253}", v) for v in target.values()))
    history = state.get("history", [])
    check(isinstance(history, list) and len(history) <= 10)
    pending = state.get("pending")
    if pending is None:
        check(not state.get("operation") and bool(state.get("baseline")))
    else:
        check(state.get("operation") == pending.get("liftID") == state["originLiftID"])
    if "baseline" in state:
        objects(state["baseline"])
    for revision in history + ([pending] if pending is not None else []):
        check(isinstance(revision, dict) and not set(revision) - {"liftID", "suiteDigest", "evidenceDigest", "state", "before", "objects"})
        check(isinstance(revision.get("liftID"), str) and re.fullmatch(r"[0-9a-f]{32}", revision["liftID"]))
        check(all(digest(revision.get(k)) for k in ("suiteDigest", "evidenceDigest")))
        check(revision.get("state") in ({"pending"} if revision is pending else {"completed", "partial"}))
        objects(revision.get("objects"))
        if "before" in revision:
            objects(revision["before"])
    rows = pending["objects"] if pending else state["baseline"]
    return {"releaseDigest": hashlib.sha256(data.encode()).hexdigest(),
            "releasePhase": "pending" if pending else "completed", "releaseAcknowledged": sum(bool(r.get("uid")) for r in rows)}


def release_document(doc):
    if (not isinstance(doc, dict) or set(doc) - {"apiVersion", "kind", "metadata", "data"} or
            doc.get("apiVersion") != "v1" or doc.get("kind") != "ConfigMap" or
            not isinstance(doc.get("data"), dict) or set(doc["data"]) != {"release.json"}):
        raise ValueError("audit-record-document")
    meta = doc["metadata"]
    if (not RELEASE_NAME.fullmatch(meta.get("name", "")) or meta.get("namespace") != "orka-system" or
            meta.get("labels") != {"app.kubernetes.io/managed-by": "kmx", "kaimahi.dev/release-state": "v1"}):
        raise ValueError("audit-record-metadata")
    return release_payload(doc["data"]["release.json"])


def classify(args, raw=b""):
    verb = next((a for a in args if a in VERBS), "")
    if not verb:
        raise ValueError("audit-command")
    tail = args[args.index(verb) + 1:]
    resource, name = (tail + ["", ""])[:2]
    persistent = False
    if "-n" in args and args[args.index("-n") + 1] != "orka-system":
        raise ValueError("audit-namespace")
    if any(a.startswith("--namespace") for a in args):
        raise ValueError("audit-namespace-override")
    preconditions = None
    record = {}
    if verb == "auth":
        raw_prefix(args, verb)
        if ("-n" not in args or len(tail) != 3 or tail[:1] != ["can-i"] or
                not (tail[1:] == ["create", "configmaps"] or tail[1] in {"get", "patch"} and
                     tail[2].startswith("configmaps/") and RELEASE_NAME.fullmatch(tail[2][11:]))):
            raise ValueError("audit-record-auth")
        resource, name = "release-permission", tail[2].removeprefix("configmaps/") if "/" in tail[2] else ""
        record = {"releaseRecord": True, "permission": tail[1]}
    elif verb == "patch":
        raw_prefix(args, verb)
        if "-n" not in args or not RELEASE_NAME.fullmatch(name) or tail != ["configmap", name, "--type=json", "--patch-file=/dev/stdin", "-o", "json"]:
            raise ValueError("audit-record-patch")
        ops = json.loads(raw, object_pairs_hook=unique_object)
        if (not isinstance(ops, list) or len(ops) != 4 or
                [(op.get("op"), op.get("path")) for op in ops] != [("test", "/metadata/uid"), ("test", "/metadata/resourceVersion"),
                ("test", "/data/release.json"), ("replace", "/data/release.json")] or any(set(op) != {"op", "path", "value"} for op in ops) or
                any(not isinstance(op["value"], str) or not OPAQUE.fullmatch(op["value"]) for op in ops[:2])):
            raise ValueError("audit-record-cas")
        before, after = release_payload(ops[2]["value"]), release_payload(ops[3]["value"])
        record = {"releaseRecord": True, **after, "priorDigest": before["releaseDigest"], "recordUID": ops[0]["value"], "recordRV": ops[1]["value"]}
        resource, persistent = "ConfigMap", True
    elif verb == "get" and resource == "configmap":
        raw_prefix(args, verb)
        if "-n" not in args or not RELEASE_NAME.fullmatch(name) or tail != ["configmap", name, "--ignore-not-found", "-o", "json"]:
            raise ValueError("audit-record-get")
        resource, record = "ConfigMap", {"releaseRecord": True}
    elif verb == "delete":
        match = re.fullmatch(r"/apis/core\.orka\.ai/v1alpha1/namespaces/orka-system/(providers|agents|tasks)/([a-z0-9][-a-z0-9.]{0,252})", name)
        if not match or tail != ["--raw", name, "-f", "-"]:
            raise ValueError("audit-delete-path")
        raw_prefix(args, verb)
        doc = json.loads(raw, object_pairs_hook=unique_object)
        preconditions = doc.get("preconditions")
        if (set(doc) != {"apiVersion", "kind", "preconditions", "propagationPolicy"} or doc["apiVersion"] != "v1" or
                doc["kind"] != "DeleteOptions" or doc["propagationPolicy"] != "Foreground" or not isinstance(preconditions, dict) or
                set(preconditions) != {"uid", "resourceVersion"} or
                not all(isinstance(v, str) and re.fullmatch(r"[a-zA-Z0-9._:-]{1,253}", v) for v in preconditions.values())):
            raise ValueError("audit-delete-options")
        resource, name, persistent = ROOTS[match[1]], match[2], True
    elif verb in {"create", "replace"}:
        if resource == "token" and verb == "create" and name == "orka-result-reader":
            pass
        else:
            if "-f" not in tail or tail[tail.index("-f") + 1] != "-":
                raise ValueError("audit-input")
            doc = json.loads(raw, object_pairs_hook=unique_object)
            resource, name = doc["kind"], doc["metadata"]["name"]
            if resource == "ConfigMap":
                raw_prefix(args, verb)
                if verb != "create" or "-n" not in args or tail != ["-f", "-", "--validate=strict", "-o", "json"] or set(doc["metadata"]) != {"name", "namespace", "labels"}:
                    raise ValueError("audit-record-create")
                record = {"releaseRecord": True, **release_document(doc)}
            elif resource not in {"Provider", "Agent", "Task"} or doc["apiVersion"] != "core.orka.ai/v1alpha1":
                raise ValueError("audit-resource")
            if doc["metadata"].get("namespace") != "orka-system":
                raise ValueError("audit-namespace")
            persistent = not any(a in {"--dry-run=server", "--dry-run=client"} for a in tail)
            if "--dry-run" in tail or any(a.startswith("--dry-run=") and a not in {"--dry-run=server", "--dry-run=client"} for a in tail):
                raise ValueError("audit-dry-run")
    elif verb == "get" and resource == "--raw":
        raw_prefix(args, verb)
        parsed = urllib.parse.urlsplit(name)
        query = urllib.parse.parse_qs(parsed.query, keep_blank_values=True)
        if (tail != ["--raw", name] or parsed.scheme or parsed.netloc or parsed.fragment or parsed.path not in INVENTORIES or
                query.get("limit") != ["128"] or set(query) not in ({"limit"}, {"limit", "continue"}) or
                ("continue" in query and (len(query["continue"]) != 1 or not query["continue"][0] or
                                         len(query["continue"][0]) > 4096))):
            raise ValueError("audit-raw-read")
        resource, name = INVENTORIES[parsed.path], ""
    elif verb == "get":
        if resource not in READS:
            raise ValueError("audit-read")
        if resource == "secret":
            output = tail[tail.index("-o") + 1] if "-o" in tail else ""
            if output != SECRET_METADATA and not PRESENCE.fullmatch(output):
                raise ValueError("audit-secret")
    elif verb == "rollout":
        if tail != ["status", "deploy/orka-api-controller", "--timeout=10s"] or "-n" not in args or args[args.index("-n") + 1] != "orka-system":
            raise ValueError("audit-rollout")
        resource, name = "deployment", "orka-api-controller"
    elif verb == "port-forward":
        resource = next((a for a in tail if a.startswith("svc/")), "")
        if not resource.startswith("svc/") or "--address" not in args or args[args.index("--address") + 1] != "127.0.0.1":
            raise ValueError("audit-forward")
        name, resource = resource.split("/", 1)[1], "service"
    elif verb == "config":
        if resource not in {"view", "current-context", "get-contexts"}:
            raise ValueError("audit-config")
        name = ""
    else:
        resource, name = "client", ""
    if name.startswith("-"):
        name = ""  # list reads have no named object
    if name and not re.fullmatch(r"[a-zA-Z0-9._-]{1,253}", name):
        raise ValueError("audit-name")
    return {**record, **({"preconditions": preconditions} if preconditions is not None else {}),
            "verb": verb, "resource": resource, "name": name, "persistent": persistent,
            "admission": verb in {"create", "replace"} and "--dry-run=client" not in tail and not bool(record),
            "taskAttempt": resource == "Task" and verb in {"create", "replace"},
            "stdinDigest": hashlib.sha256(raw).hexdigest(), "stdinBytes": len(raw)}


MARKERS = ("kaimahi.dev/suite", "kaimahi.dev/suite-digest", "kaimahi.dev/suite-member", "kaimahi.dev/suite-renderer",
           "kaimahi.dev/suite-binding-digest", "kaimahi.dev/suite-rendered-digest", "kaimahi.dev/suite-evaluation-run")
RESPONSE_LIMIT = 4 << 20


def spec_digest(spec):
    domain = b"kmx.native-orka.spec/v1"
    raw = json.dumps(spec, sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode()
    return hashlib.sha256(b"rendered/000.yaml 23\n" + domain + b"\nrendered/001.yaml " + str(len(raw)).encode() + b"\n" + raw + b"\n").hexdigest()


def metadata(doc):
    meta = doc["metadata"]
    return {k: meta[k] for k in ("name", "namespace", "uid", "resourceVersion", "generation") if k in meta} | {
        "terminating": bool(meta.get("deletionTimestamp")),
        "annotations": {k: meta["annotations"][k] for k in MARKERS if k in meta.get("annotations", {})},
        "taskLabel": meta.get("labels", {}).get("orka.ai/task", ""),
        "ownerReferences": [{k: owner[k] for k in ("apiVersion", "kind", "name", "uid", "controller") if k in owner}
                            for owner in meta.get("ownerReferences", [])]}


def response_snapshot(raw):
    if not raw.strip():
        return {"absent": True}
    doc = json.loads(raw, object_pairs_hook=unique_object)
    kind = doc.get("kind")
    if kind == "Status":
        return {k: doc[k] for k in ("apiVersion", "kind", "status")}
    if kind in {k + "List" for k in INVENTORIES.values()}:
        if len(doc["items"]) > 128:
            raise ValueError("audit-page-size")
        return {"apiVersion": doc["apiVersion"], "kind": kind,
                "resourceVersion": doc["metadata"]["resourceVersion"],
                "continueDigest": hashlib.sha256(doc["metadata"].get("continue", "").encode()).hexdigest(),
                "continueBytes": len(doc["metadata"].get("continue", "").encode()),
                "items": [metadata(item) for item in doc["items"]]}
    if kind == "ConfigMap" and RELEASE_NAME.fullmatch(doc.get("metadata", {}).get("name", "")):
        # Retain only fingerprints and typed progress, never the shared bytes.
        projection = release_document({k: doc[k] for k in ("apiVersion", "kind", "metadata", "data")})
        return {"apiVersion": "v1", "kind": kind, "metadata": metadata(doc), **projection}
    if kind not in {"Provider", "Agent", "Task"}:
        return None  # no arbitrary API/Secret/CRD payload projection
    result = {"apiVersion": doc["apiVersion"], "kind": kind, "metadata": metadata(doc), "specDigest": spec_digest(doc["spec"])}
    status, meta = doc.get("status") or {}, doc["metadata"]
    result["ready"] = status.get("ready") is True and any(c.get("type") == "Ready" and c.get("status") == "True" and
                        c.get("observedGeneration") == meta.get("generation") for c in status.get("conditions", []))
    if kind == "Task":
        spec = dict(doc["spec"])
        result.update(inputDigest=hashlib.sha256(spec.get("prompt", "").encode()).hexdigest(),
                      agentRef=spec.get("agentRef"), taskType=spec.get("type"), phase=status.get("phase"),
                      resultAvailable=(status.get("resultRef") or {}).get("available") is True)
        for key, value in {"priority": 500, "concurrencyPolicy": "Forbid", "failedRunsHistoryLimit": 1,
                           "successfulRunsHistoryLimit": 3, "startingDeadlineSeconds": 100}.items():
            if key in spec:
                if spec.pop(key) != value:
                    raise ValueError("audit-task-default")
        result["taskSpecDigest"] = spec_digest(spec)
    return result


def validate_response(response):
    allowed = {"absent", "apiVersion", "kind", "metadata", "specDigest", "ready", "inputDigest", "agentRef", "taskType",
               "phase", "resultAvailable", "taskSpecDigest", "resourceVersion", "continueDigest", "continueBytes", "items", "status",
               "releaseDigest", "releasePhase", "releaseAcknowledged"}
    if not isinstance(response, dict) or set(response) - allowed:
        raise ValueError("audit-response")
    if "absent" in response and response != {"absent": True}:
        raise ValueError("audit-response-absence")
    kinds = {"Provider", "Agent", "Task", "Status", "ConfigMap"} | {k + "List" for k in INVENTORIES.values()}
    if "kind" in response:
        kind = response["kind"]
        version = "batch/v1" if kind == "JobList" else "v1" if kind in {"Status", "PodList", "ConfigMapList", "ConfigMap"} else "core.orka.ai/v1alpha1"
        if kind not in kinds or response.get("apiVersion") != version:
            raise ValueError("audit-response-kind")
    if "status" in response and (response.get("kind") != "Status" or response["status"] not in {"Success", "Failure"}):
        raise ValueError("audit-response-status")
    if "resourceVersion" in response and (not isinstance(response["resourceVersion"], str) or
            not re.fullmatch(r"[a-zA-Z0-9._:-]{1,253}", response["resourceVersion"])):
        raise ValueError("audit-response-rv")
    if "continueBytes" in response and (type(response["continueBytes"]) is not int or not 0 <= response["continueBytes"] <= 4096):
        raise ValueError("audit-response-token")
    if "items" in response and (not isinstance(response["items"], list) or len(response["items"]) > 128):
        raise ValueError("audit-response-page")
    for key in ("ready", "resultAvailable"):
        if key in response and type(response[key]) is not bool:
            raise ValueError("audit-response-bool")
    for key in ("specDigest", "inputDigest", "taskSpecDigest", "continueDigest", "releaseDigest"):
        if key in response and not (isinstance(response[key], str) and re.fullmatch(r"[0-9a-f]{64}", response[key])):
            raise ValueError("audit-response-hash")
    if response.get("phase") not in {None, "Pending", "Running", "Succeeded", "Failed", "Unknown", "Scheduled", "Canceled", "Cancelled"}:
        raise ValueError("audit-response-phase")
    if "taskType" in response and response["taskType"] != "ai":
        raise ValueError("audit-response-task-type")
    for meta in ([response["metadata"]] if "metadata" in response else response.get("items", [])):
        if set(meta) - {"name", "namespace", "uid", "resourceVersion", "generation", "terminating", "annotations", "taskLabel", "ownerReferences"}:
            raise ValueError("audit-response-metadata")
        for key in ("name", "namespace", "uid", "resourceVersion", "taskLabel"):
            if key in meta and not (isinstance(meta[key], str) and (key == "taskLabel" and meta[key] == "" or
                    re.fullmatch(r"[a-zA-Z0-9._:-]{1,253}", meta[key]))):
                raise ValueError("audit-response-identity")
        if "generation" in meta and (type(meta["generation"]) is not int or meta["generation"] < 0):
            raise ValueError("audit-response-generation")
        for key, value in meta.get("annotations", {}).items():
            pattern = r"[0-9a-f]{32}" if key == MARKERS[-1] else r"sha256:[0-9a-f]{64}" if key == MARKERS[1] else r"[0-9a-f]{64}" if key in MARKERS[4:6] else r"[-a-zA-Z0-9._/]{1,253}"
            if not isinstance(value, str) or not re.fullmatch(pattern, value):
                raise ValueError("audit-response-marker")
        if set(meta.get("annotations", {})) - set(MARKERS) or len(meta.get("ownerReferences", [])) > 8:
            raise ValueError("audit-response-owner")
        for owner in meta.get("ownerReferences", []):
            if set(owner) - {"apiVersion", "kind", "name", "uid", "controller"} or any(not isinstance(value, str) or
                    not re.fullmatch(r"[-a-zA-Z0-9._:/]{1,253}", value) for key, value in owner.items() if key != "controller") or (
                    "controller" in owner and type(owner["controller"]) is not bool):
                raise ValueError("audit-response-owner")
    if "agentRef" in response and (not isinstance(response["agentRef"], dict) or set(response["agentRef"]) != {"name", "namespace"} or
            any(not isinstance(v, str) or not re.fullmatch(r"[a-zA-Z0-9._-]{1,253}", v) for v in response["agentRef"].values())):
        raise ValueError("audit-response-ref")


def completed(frames):
    """Join request intent/completion; interrupted attempts cannot disappear."""
    pending, result, order = {}, {}, []
    for frame in frames:
        identity, stage = frame.get("requestID"), frame.get("stage")
        if not identity or stage not in {"intent", "complete"} or frame["verb"] == "blocked":
            raise ValueError("audit-frame")
        if stage == "intent":
            if identity in pending or identity in result:
                raise ValueError("audit-repeat")
            pending[identity] = frame
            order.append(identity)
        else:
            intent = pending.pop(identity, None)
            if intent is None or intent["verb"] == "port-forward":
                raise ValueError("audit-completion")
            expected = {k: v for k, v in intent.items() if k != "stage"}
            if "response" in frame:
                validate_response(frame["response"])
            if "responseError" in frame:
                raise ValueError("audit-response-error")
            actual = {k: v for k, v in frame.items() if k not in {"stage", "returnCode", "response"}}
            if expected != actual or not isinstance(frame.get("returnCode"), int):
                raise ValueError("audit-completion")
            result[identity] = frame
    for identity, intent in pending.items():
        if intent["verb"] != "port-forward":
            raise ValueError("audit-interrupted")
        result[identity] = intent
    return [result[identity] for identity in order]


def verify_cleanup_order(frames):
    completed(frames)
    pending = None
    for frame in frames:
        if frame["verb"] != "delete":
            continue
        if frame["stage"] == "intent":
            if pending is not None:
                raise ValueError("audit-delete-overlap")
            pending = frame["requestID"]
        elif frame.get("returnCode") != 0 or pending != frame["requestID"]:
            raise ValueError("audit-delete-completion")
        else:
            pending = None
    if pending:
        raise ValueError("audit-delete-interrupted")


def verify_read_only(frames):
    if any(f["verb"] not in {"get", "config", "version"} or f.get("returnCode") != 0 or
           f["persistent"] or f.get("admission") or f.get("taskAttempt") for f in frames):
        raise ValueError("audit-readonly")


def append(frame):
    path = pathlib.Path(os.environ["KMX_NATIVE_AUDIT"])
    fd = os.open(path, os.O_WRONLY | os.O_APPEND | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, "ab") as stream:
        stream.write(json.dumps(frame, sort_keys=True).encode() + b"\n")


def main():
    try:
        if pathlib.Path(sys.argv[0]).name != "kubectl":
            raise ValueError("audit-builder")
        args = sys.argv[1:]
        check_scope(args, os.environ["KMX_NATIVE_CONTEXT"])
        stdin = "-f" in args or "--patch-file=/dev/stdin" in args
        raw = sys.stdin.buffer.read() if stdin else b""
        frame = {**classify(args, raw), "requestID": uuid.uuid4().hex}
        real = os.environ["KMX_NATIVE_KUBECTL"]
        append({**frame, "stage": "intent"})
        if frame["verb"] == "port-forward":
            # The existing client proves owned bind; execv owns its lifecycle.
            os.execv(real, [real, *args])
        capture = frame["verb"] in {"get", "create", "replace", "delete", "patch"} and ("--raw" in args or
                  ("-o" in args and args[args.index("-o") + 1] == "json")) and frame["resource"] in {
                      "Provider", "Agent", "Task", "providers.core.orka.ai", "agents.core.orka.ai", "tasks.core.orka.ai",
                      "Job", "Pod", "ConfigMap"}
        observation = {}
        if capture:
            # A bounded private memory buffer; forward the original bytes, never
            # append server error bodies or payloads to the metadata-only ledger.
            buffer, overflow = bytearray(), False
            with subprocess.Popen([real, *args], stdin=subprocess.PIPE if stdin else None, stdout=subprocess.PIPE) as child:
                if stdin:
                    child.stdin.write(raw)
                    child.stdin.close()
                while True:
                    chunk = child.stdout.read(65536)
                    if not chunk:
                        break
                    sys.stdout.buffer.write(chunk)
                    sys.stdout.buffer.flush()
                    if len(buffer) + len(chunk) <= RESPONSE_LIMIT and not overflow:
                        buffer.extend(chunk)
                    else:
                        buffer.clear()
                        overflow = True
                code = child.wait()
            if code == 0:
                try:
                    if overflow:
                        raise ValueError("audit-response-size")
                    response = response_snapshot(bytes(buffer))
                    if response is not None:
                        validate_response(response)
                        observation["response"] = response
                except Exception:
                    observation["responseError"] = True
        else:
            code = subprocess.run([real, *args], input=raw if stdin else None, check=False).returncode
        append({**frame, **observation, "stage": "complete", "returnCode": code})

        return code
    except Exception:
        append({"verb": "blocked", "resource": "boundary", "name": "", "persistent": False})
        print("native audit: boundary-refused", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
