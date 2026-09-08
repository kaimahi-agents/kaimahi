#!/usr/bin/env bash
# Build the demo's fixture ERP, get it onto the cluster, project the corpus
# as a ConfigMap and roll the Deployment (docs/ap-demo.md).
#
# The only thing that differs between environments is how the ERP pod gets
# its image — the same split scripts/plane-deploy.sh makes for the proxy,
# and for the same reasons:
#
#   kind — `docker build` + `kind load` side-loads a LOCAL tag, and
#          k8s/erp-mcp.yaml pins `imagePullPolicy: Never`. That pin is
#          deliberate: a side-loaded local tag must never silently fall
#          back to PULLING a squattable public name. It stays exactly as
#          committed, and this path applies the document unrendered.
#
#   registry — the image is built BY the registry (`az acr build`, run by
#          the Makefile exactly as `make plane-image` does for the proxy)
#          and PULLED from a PRIVATE ACR. Both the image reference and the
#          pull policy must change; `Never` there means ErrImageNeverPull,
#          forever.
#
# Nothing is published either way. A private ACR is not publication,
# and the guardrail against publishing the ERP stands: no public
# registry, no `docker push`, no registry login on the operator's machine.
#
# Fail closed: the render must produce exactly the intended change, and
# this script verifies that before anything is applied.
#
# The corpus is k8s/erp-fixtures.json — one source of truth. The Go tests
# validate the arithmetic of that same file (internal/demo/erp/fixtures_test.go),
# and the server refuses to start on a corpus that does not add up, so an
# edit that breaks the story fails at boot instead of answering wrongly.
#
# Steps: image | fixtures | deploy (default: all three). `image` is the
# kind path's build-and-side-load; on a registry target the image already
# exists in the registry before this script runs, so `fixtures` is the
# entry point (see the Makefile's TARGET=aks branch). `render` prints what
# a registry target WOULD apply, and contacts no cluster.
#
# Env:
#   KUBECTL          kubectl invocation incl. --context (required in practice)
#   ERP_TARGET       kind (default) | registry
#   ERP_IMAGE        image reference to deploy (registry targets)
#   ERP_PULL_POLICY  imagePullPolicy for registry targets (default IfNotPresent)
set -euo pipefail
umask 077

KUBECTL="${KUBECTL:-kubectl}"
NAMESPACE=kaimahi
CONTAINER_ENGINE="${CONTAINER_ENGINE:-docker}"
KIND_CLUSTER="${KIND_CLUSTER:-kaimahi-p1}"
# The kind path's side-loaded local tag, named so the registry path can
# REFUSE it: applying "kaimahi-erp:dev" to a managed cluster would be an
# ImagePullBackOff whose cause is two layers from the symptom.
ERP_IMAGE_KIND_DEFAULT=kaimahi-erp:dev
ERP_IMAGE="${ERP_IMAGE:-$ERP_IMAGE_KIND_DEFAULT}"
ERP_TARGET="${ERP_TARGET:-kind}"
ERP_PULL_POLICY="${ERP_PULL_POLICY:-IfNotPresent}"
FIXTURES="${FIXTURES:-k8s/erp-fixtures.json}"
STEP="${1:-all}"

here=$(cd "$(dirname "$0")/.." && pwd)
MANIFEST="$here/k8s/erp-mcp.yaml"

case "$ERP_TARGET" in
  (kind|registry) ;;
  (*) echo "unknown ERP_TARGET '$ERP_TARGET' (want kind or registry)" >&2; exit 2 ;;
esac

case "$CONTAINER_ENGINE" in
  (docker|podman) ;;
  (*) echo "unknown CONTAINER_ENGINE '$CONTAINER_ENGINE' (want docker or podman)" >&2; exit 2 ;;
esac

do_image() {
  # The kind path's build-and-side-load, unchanged. A registry target's
  # image is built BY the registry (`az acr build`, run from the Makefile
  # exactly as `make plane-image` does for the proxy) — never here, so no
  # image is built locally for, or pushed from, this machine.
  if [ "$ERP_TARGET" != kind ]; then
    echo "erp-deploy: the 'image' step is the kind side-load path." >&2
    echo "  On a $ERP_TARGET target the image is built in the registry" >&2
    echo "  before this script runs (see the Makefile's TARGET=aks branch)." >&2
    exit 1
  fi
  echo "erp: building $ERP_IMAGE with $CONTAINER_ENGINE" >&2
  "$CONTAINER_ENGINE" build -f cmd/demo/kaimahi-erp/Dockerfile -t "$ERP_IMAGE" .
  # The podman half is the plane's, carried across with its reason (see
  # internal/kmx/app/plane.go): `kind load docker-image` reports "image not
  # present locally" for images podman demonstrably has, so non-docker
  # engines go through the archive path kind documents.
  if [ "$CONTAINER_ENGINE" = podman ]; then
    tar=$(mktemp -t kaimahi-erp-XXXXXX.tar)
    trap 'rm -f "$tar"' RETURN
    podman save -o "$tar" "$ERP_IMAGE"
    kind load image-archive "$tar" --name "$KIND_CLUSTER"
  else
    kind load docker-image "$ERP_IMAGE" --name "$KIND_CLUSTER"
  fi
}

do_fixtures() {
  test -s "$FIXTURES" || { echo "no fixture corpus at $FIXTURES" >&2; exit 1; }
  python3 -c 'import json,sys; json.load(open(sys.argv[1]))' "$FIXTURES" \
    || { echo "$FIXTURES is not valid JSON" >&2; exit 1; }
  echo "erp: projecting $FIXTURES as ConfigMap kaimahi-erp-fixtures" >&2
  $KUBECTL -n "$NAMESPACE" create configmap kaimahi-erp-fixtures \
    --from-file=fixtures.json="$FIXTURES" \
    --dry-run=client -o yaml | $KUBECTL -n "$NAMESPACE" apply -f -
}

# Render k8s/erp-mcp.yaml with the registry image and a real pull policy,
# then verify the render says exactly what was intended before anything is
# applied. Done on the PARSED document, not with sed: the string
# "imagePullPolicy: Never" also appears in that file's own comments, and a
# textual substitution that hit a comment (or missed the field) would be
# silent. Same shape and the same fail-closed rule as plane-deploy.sh.
render_manifest() { # <outfile>
  ERP_IMAGE="$ERP_IMAGE" ERP_PULL_POLICY="$ERP_PULL_POLICY" \
    python3 - "$MANIFEST" > "$1" <<'PY'
import os, sys, yaml

image = os.environ["ERP_IMAGE"]
policy = os.environ["ERP_PULL_POLICY"]

docs = list(yaml.safe_load_all(open(sys.argv[1])))
patched = 0
for doc in docs:
    if not doc or doc.get("kind") != "Deployment":
        continue
    for c in doc["spec"]["template"]["spec"]["containers"]:
        if c["name"] != "erp":
            continue
        c["image"] = image
        c["imagePullPolicy"] = policy
        patched += 1

# Fail closed: if the manifest is ever restructured so the container is no
# longer found, deploying the unrendered document would put `Never` on a
# registry cluster and wedge it. Refuse instead.
if patched != 1:
    sys.exit(f"erp-deploy: expected exactly 1 erp container to render, found {patched}")

yaml.safe_dump_all(docs, sys.stdout, default_flow_style=False, sort_keys=False)
PY

  ERP_IMAGE="$ERP_IMAGE" ERP_PULL_POLICY="$ERP_PULL_POLICY" \
    python3 - "$1" <<'PY'
import os, sys, yaml

want_image = os.environ["ERP_IMAGE"]
want_policy = os.environ["ERP_PULL_POLICY"]
seen = 0
for doc in yaml.safe_load_all(open(sys.argv[1])):
    if not doc or doc.get("kind") != "Deployment":
        continue
    spec = doc["spec"]["template"]["spec"]
    for c in spec["containers"]:
        if c["name"] != "erp":
            continue
        seen += 1
        assert c["image"] == want_image, c["image"]
        assert c["imagePullPolicy"] == want_policy, c["imagePullPolicy"]
        # The POSTURE must survive the render untouched. The corpus is a
        # mounted ConfigMap and is never baked into the image, and the pod
        # holds no service-account token; a render that dropped either
        # would yield an ERP that starts and answers from nothing, or one
        # with a cluster identity it has no business having.
        mounts = {m["mountPath"] for m in c["volumeMounts"]}
        assert "/etc/kaimahi/erp" in mounts, mounts
        assert spec.get("automountServiceAccountToken") is False, spec
        vols = {v["name"]: v for v in spec["volumes"]}
        assert vols["fixtures"]["configMap"]["name"] == "kaimahi-erp-fixtures", vols
assert seen == 1, f"expected exactly 1 erp container in the render, found {seen}"
PY
}

# Everything a registry target must satisfy before a byte is rendered.
# Separate from do_apply so `erp-deploy.sh render` runs the identical
# gauntlet with no cluster in sight — which is how CI proves this path
# (the live one cannot be proven in CI: no Azure credential belongs in a
# public, fork-exposed repo).
validate_registry() {
  # A registry target must be given a registry image. The default above is
  # the kind tag, so "not set" and "set to the local tag" are the same
  # mistake and both are refused here rather than surfacing later as an
  # ImagePullBackOff.
  if [ "$ERP_IMAGE" = "$ERP_IMAGE_KIND_DEFAULT" ]; then
    echo "erp-deploy: ERP_IMAGE is required for a $ERP_TARGET target" >&2
    echo "  ('$ERP_IMAGE_KIND_DEFAULT' is the kind side-load tag and cannot be pulled)" >&2
    exit 1
  fi
  # Non-empty is not the same as well-formed. An unset ACR_NAME makes the
  # Makefile expand ERP_IMAGE to ".azurecr.io/kaimahi-erp:<tag>" — which
  # sails past an emptiness check and would be rendered in and applied.
  # Require a registry host before the first slash.
  case "$ERP_IMAGE" in
    (/* | .* | *' '* | '')
      echo "erp-deploy: malformed ERP_IMAGE '$ERP_IMAGE'" >&2
      echo "  (an unset ACR_NAME produces exactly this shape)" >&2
      exit 1 ;;
  esac
  # A SLASH IS NOT A REGISTRY. "team/erp:v1" has one and resolves through
  # Docker Hub — a public registry this project deliberately never uses.
  # Docker's own rule is what distinguishes them: the first path
  # component is a registry host only if it contains a dot or a port
  # colon (or is localhost). Anything else is a Docker Hub namespace, so
  # accepting it would pull an image from a name a stranger can register.
  case "$ERP_IMAGE" in
    (*://*)
      echo "erp-deploy: ERP_IMAGE '$ERP_IMAGE' carries a URL scheme" >&2
      echo "  (an image reference is <registry>/<repository>:<tag>, not a URL)" >&2
      exit 1 ;;
  esac
  erp_image_host=${ERP_IMAGE%%/*}
  case "$ERP_IMAGE" in (*/*) ;; (*) erp_image_host='' ;; esac
  case "$erp_image_host" in
    (localhost | *.* | *:*) ;;
    (*)
      echo "erp-deploy: ERP_IMAGE '$ERP_IMAGE' names no registry" >&2
      echo "  (a registry target needs <registry-host>/<repository>:<tag>;" >&2
      echo "   '${erp_image_host:-$ERP_IMAGE}' is a Docker Hub namespace, not a host)" >&2
      exit 1 ;;
  esac
  case "$ERP_PULL_POLICY" in
    (Always | IfNotPresent) ;;
    (*)
      echo "erp-deploy: refusing pull policy '$ERP_PULL_POLICY' on a registry target" >&2
      echo "  (Never cannot work off-cluster — the image would never be fetched)" >&2
      exit 1 ;;
  esac
}

# The rendered document on stdout, having passed every check that the
# deploy path passes. No cluster is contacted.
do_render() {
  if [ "$ERP_TARGET" = kind ]; then
    echo "erp-deploy: nothing to render on a kind target — k8s/erp-mcp.yaml is" >&2
    echo "  applied exactly as committed there. Set ERP_TARGET=registry." >&2
    exit 1
  fi
  validate_registry
  workdir=$(mktemp -d)
  # shellcheck disable=SC2064 # expand workdir now, not at trap time
  trap "rm -rf '$workdir'" RETURN
  render_manifest "$workdir/erp-mcp.yaml"
  echo "erp-deploy: erp image=$ERP_IMAGE pullPolicy=$ERP_PULL_POLICY" >&2
  cat "$workdir/erp-mcp.yaml"
}

do_apply() {
  if [ "$ERP_TARGET" = kind ]; then
    # The kind path runs the SAME command it always ran — literally
    # `kubectl apply -f k8s/erp-mcp.yaml`, no rendering, no transform —
    # which is what makes "kind is unchanged" a fact rather than a claim.
    # shellcheck disable=SC2086 # KUBECTL deliberately carries --context args
    $KUBECTL apply -f "$MANIFEST"
    return
  fi

  validate_registry
  workdir=$(mktemp -d)
  # shellcheck disable=SC2064 # expand workdir now, not at trap time
  trap "rm -rf '$workdir'" RETURN
  render_manifest "$workdir/erp-mcp.yaml"
  echo "erp-deploy: erp image=$ERP_IMAGE pullPolicy=$ERP_PULL_POLICY" >&2
  # shellcheck disable=SC2086 # KUBECTL deliberately carries --context args
  $KUBECTL apply -f "$workdir/erp-mcp.yaml"
}

do_deploy() {
  do_apply
  # Always restart: a rebuilt image under the same tag, or an edited
  # corpus, leaves the spec unchanged — apply alone would keep the old
  # binary and the old story running. Same reason `make plane` restarts.
  $KUBECTL -n "$NAMESPACE" rollout restart deploy/kaimahi-erp
  $KUBECTL -n "$NAMESPACE" rollout status deploy/kaimahi-erp --timeout=300s
  # Fail closed on the thing that actually matters: the pod is ready only
  # once the corpus loaded, because the server exits on a corpus that does
  # not add up. Say so, with the numbers, so a bad edit is obvious here.
  $KUBECTL -n "$NAMESPACE" logs -l app=kaimahi-erp --tail=20 | grep 'corpus loaded' \
    || { echo "the ERP did not report a loaded corpus — check its logs" >&2; exit 1; }
}

case "$STEP" in
  (all)      do_image; do_fixtures; do_deploy ;;
  (image)    do_image ;;
  (fixtures) do_fixtures; do_deploy ;;
  (deploy)   do_deploy ;;
  (render)   do_render ;;
  (*) echo "usage: erp-deploy.sh [all|image|fixtures|deploy|render]" >&2; exit 2 ;;
esac
