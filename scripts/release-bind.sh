#!/usr/bin/env bash
# Bind the release credential to ONE repository, at the plane.
#
# The release agent's read tools are on its allowlist, which says WHICH
# TOOLS it may call and says nothing about which repository it may call
# them on. This adds the missing half as a standing constraint: the
# read tools are callable only with owner=<owner> and repo=<name>, and a
# call naming any other repository is denied and files a request, exactly
# as an unlisted tool does.
#
# ONLY THE READ TOOLS. A constraint admits — a call inside its bounds
# needs no human. Right for reading a repo, wrong for create_branch or
# actions_run_trigger, which get approved every time. Those carry no
# constraint and no allowlist entry, so every attempt is denied. That's
# the design, not an omission.
#
# NOT COMMITTED, because `Azure/aks-desktop` is somebody's project and a
# public repo's committed table is the wrong place for it — same reason
# no Azure identifier is committed. It's operator config, so it goes
# where operator config goes: the overlay ConfigMap
# (kaimahi-upstreams-extra), as one fragment.
#
# That's the point of using the overlay at all: `make plane` reapplies the
# base table and would drop an in-place patch, and a repository binding
# that vanishes on the next deploy is worse than none. The merge is
# per-name and refuses collisions, so this sits beside the AP agent's
# constraint without touching it.
#
# Written with `kubectl patch --type merge` on the one key, never a
# create-or-replace: other operators' fragments share that ConfigMap.
#
# BUILDS ARE BOUND, NOT APPROVED. With ADO_ORG + ADO_PROJECT +
# ADO_PIPELINES this also constrains pipelines_write to `action
# run_pipeline` on the pipeline ids named, in one project of one org.
# Those run with no human, audited; anything else on that tool — another
# pipeline, another project, create_pipeline, update_build_stage — is
# denied and files a request.
#
# Why that's right and not a weakening: a build is repeatable, reversible
# and cheap. What's consequential is cutting the branch and publishing.
# Approving each build spends attention on the safest step — and it
# creates a race the process can't win, measured on the first real run:
# the ADO credential lives about an hour, and the gap between filing a
# request, approving it and calling is exactly that long. Approvals got
# stranded by an expired token, not by anyone's decision.
#
# The caveat, since a constraint ADMITS: the ref a pipeline builds is
# nested (resources.repositories.*.refName) and can't be a policy field,
# so this admits those pipelines on any ref. It bounds which pipeline
# runs, not what it builds.
#
# Usage: GITHUB_REPO=owner/name bash scripts/release-bind.sh
#          [ADO_ORG=... ADO_PROJECT=... ADO_PIPELINES=1000,1001,1003]
#        GITHUB_REPO=- bash scripts/release-bind.sh     (remove the binding)
set -euo pipefail
umask 077

KUBECTL="${KUBECTL:-kubectl}"
CRED_RELEASE="${CRED_RELEASE:-release-agent}"
repo="${GITHUB_REPO:-}"
ado_org="${ADO_ORG:-}"
ado_project="${ADO_PROJECT:-}"
ado_pipelines="${ADO_PIPELINES:-}"

[ -n "$repo" ] || { echo 'usage: make release-bind GITHUB_REPO=owner/name  (or GITHUB_REPO=- to remove)' >&2; exit 2; }
if [ "$repo" != "-" ] && ! [[ "$repo" =~ ^[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9._-]{1,100}$ ]]; then
  echo "invalid GITHUB_REPO '$repo' (want owner/name, or - to remove)" >&2
  exit 2
fi

OVERLAY_CM="${OVERLAY_CM:-kaimahi-upstreams-extra}"
FRAGMENT="${FRAGMENT:-release-bind.json}"

here="$(cd "$(dirname "$0")/.." && pwd)"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

probe_ctx=$($KUBECTL config view --minify -o jsonpath='{.contexts[0].name}')
KUBE_NS="kaimahi" KUBE_CTX="$probe_ctx" bash "$here/scripts/kube-guard.sh" "release-bind $repo"

# The BASE table has to be readable, and has to declare the tools the
# constraint names: a constraint on an undeclared tool refuses the whole
# config at boot, which would take the plane down rather than fail this
# command. Check it here, where the failure is one message.
$KUBECTL -n kaimahi get configmap kaimahi-upstreams -o jsonpath='{.data.upstreams\.json}' > "$work/base.json"
test -s "$work/base.json" || { echo 'the plane is not deployed (no kaimahi-upstreams ConfigMap)' >&2; exit 1; }

if [ "$repo" = "-" ]; then
  # Remove just this fragment. A null value in a merge patch DELETES the
  # key, leaving every other operator's fragment alone.
  # A null value in a merge patch deletes the key, leaving other
  # operators' fragments alone. Only NotFound is tolerated: `|| true` on
  # everything would let an RBAC denial or an unreachable API server end
  # with "the binding is removed" printed below, which is the one thing
  # this repo tells its agent never to do.
  if ! err=$($KUBECTL -n kaimahi patch configmap "$OVERLAY_CM" --type merge \
       -p "{\"data\": {\"$FRAGMENT\": null}}" 2>&1 >/dev/null); then
    case "$err" in
      *NotFound*|*"not found"*) : ;;   # nothing to remove
      *) echo "release-bind: could not remove the fragment: $err" >&2; exit 1 ;;
    esac
  fi
else
  python3 - "$work/base.json" "$CRED_RELEASE" "$repo" "$ado_org" "$ado_project" "$ado_pipelines" <<'EOF' > "$work/fragment.json"
import json, sys

base, cred, repo = sys.argv[1], sys.argv[2], sys.argv[3]
ado_org, ado_project, ado_pipelines = sys.argv[4], sys.argv[5], sys.argv[6]
c = json.load(open(base))
owner, name = repo.split("/", 1)

# Only the tools that READ. Anything consequential must stay outside every
# constraint so that it is denied and approved, call by call — a standing
# constraint ADMITS, and admitting a branch creation would be the opposite
# of the design.
read_tools = ["list_pull_requests", "list_commits", "list_tags", "list_releases",
              "get_latest_release", "get_release_by_tag", "actions_list", "actions_get"]
declared = {}
for up in c.get("tool_upstreams", {}).values():
    declared.update(up.get("tools") or {})
missing = [t for t in read_tools if t not in declared]
if missing:
    sys.exit("the running table declares no policy fields for %s — is the plane older "
             "than this script? (make plane)" % ", ".join(missing))
if cred in (c.get("standing_constraints") or {}):
    sys.exit("the committed table already constrains %r; refusing to shadow it from an "
             "overlay (the merge would be refused at boot anyway)" % cred)

constraints = {t: [{"field": "owner", "op": "eq", "value": owner},
                   {"field": "repo", "op": "eq", "value": name}] for t in read_tools}

# The builds, if asked for. Every field named here must be one the tool
# DECLARES policy-relevant, or the plane refuses the whole config at boot.
if ado_pipelines:
    if not (ado_org and ado_project):
        sys.exit("ADO_PIPELINES needs ADO_ORG and ADO_PROJECT")
    try:
        ids = [int(x) for x in ado_pipelines.split(",") if x.strip()]
    except ValueError:
        sys.exit("ADO_PIPELINES must be a comma-separated list of numeric pipeline ids")
    if not ids:
        sys.exit("ADO_PIPELINES named no pipelines")
    declared_write = declared.get("pipelines_write", {}).get("policy_fields") or []
    for f in ("action", "orgName", "project", "pipelineId"):
        if f not in declared_write:
            sys.exit("pipelines_write does not declare %r as policy-relevant, so a "
                     "constraint on it could not be enforced" % f)
    constraints["pipelines_write"] = [
        {"field": "action", "op": "eq", "value": "run_pipeline"},
        {"field": "orgName", "op": "eq", "value": ado_org},
        {"field": "project", "op": "eq", "value": ado_project},
        {"field": "pipelineId", "op": "in", "values": ids},
    ]

# The fragment carries ONLY standing_constraints. An overlay may not set
# an upstream's custody fields, and this needs none of them.
json.dump({"standing_constraints": {cred: constraints}}, sys.stdout, indent=2)
EOF

  # Create the overlay ConfigMap if this is the first fragment, then set
  # OUR key with a merge patch — never a create-or-replace, because other
  # operators' fragments live in the same ConfigMap.
  $KUBECTL -n kaimahi get configmap "$OVERLAY_CM" >/dev/null 2>&1 \
    || $KUBECTL -n kaimahi create configmap "$OVERLAY_CM" >/dev/null
  python3 - "$work/fragment.json" "$FRAGMENT" <<'EOF' > "$work/patch.json"
import json, sys
print(json.dumps({"data": {sys.argv[2]: open(sys.argv[1]).read()}}))
EOF
  $KUBECTL -n kaimahi patch configmap "$OVERLAY_CM" --type merge --patch-file "$work/patch.json" >/dev/null
fi

# The overlay is read at boot, so the change is a rollout. A fragment the
# plane refuses takes the rollout down and leaves the serving replicas
# up — the same fail-closed shape every other config error has.
$KUBECTL -n kaimahi rollout restart deploy/kaimahi-proxy >/dev/null
if ! $KUBECTL -n kaimahi rollout status deploy/kaimahi-proxy --timeout=300s >/dev/null; then
  echo "release-bind: the proxy did not roll out — the plane refused the overlay." >&2
  $KUBECTL -n kaimahi logs -l app=kaimahi-proxy --tail=20 >&2 || true
  echo "  The previously serving replicas are still up. Remove the fragment with:" >&2
  echo "    make release-bind GITHUB_REPO=-" >&2
  exit 1
fi

if [ "$repo" = "-" ]; then
  echo "release-bind: the repository binding for $CRED_RELEASE is removed." >&2
else
  echo "release-bind: $CRED_RELEASE may now read only $repo." >&2
  echo "  A read naming any other repository is denied and files a request." >&2
  echo "  Consequential calls are unaffected: they are approved one at a time." >&2
  echo "  It lives in the overlay ($OVERLAY_CM/$FRAGMENT), so 'make plane' keeps it." >&2
fi
