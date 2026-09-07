#!/usr/bin/env bash
# Context safety for every MUTATING make target.
#
# The tooling could once only ever reach a kind cluster:
# `KUBE_CTX := kind-$(KIND_CLUSTER)` prefixed every context with `kind-`,
# so a typo produced "context not found", not a write to production. Once
# KUBE_CTX is overridable that safety net is gone, and this repo's own
# docs/CLI-PROPOSAL.md names the resulting foot-gun: "--apply on a
# production context by accident". This script is the replacement net.
#
# Contract:
#   - ALWAYS print where the action is about to land (context, API server
#     host, namespaces) on stderr, so the answer is on screen even when
#     nothing is asked.
#   - A LOCAL kind cluster proceeds silently-ish (banner only). That keeps
#     the kind path's behaviour unchanged for existing users and for CI.
#   - ANY other context requires explicit confirmation naming the context.
#   - FAIL CLOSED: no confirmation, no action. An unknown context, an
#     unreadable kubeconfig, or a non-interactive shell without
#     KAIMAHI_CONFIRM all refuse rather than guess.
#
# "Local kind" is deliberately TWO independent checks, because a context
# NAME is cosmetic — anyone can name an AKS context `kind-prod`. The
# substantive check is the API server address: kind publishes its API
# server on loopback. Both must agree.
#
# Callers run directly, not through make (scripts/tool-*-probe.sh):
# those bypass the Makefile's `guard` prerequisite because CI and humans
# invoke them as scripts. They matter because, left alone, they inherit
# whatever `kubectl config current-context` happens to be — and
# `az aks get-credentials` rewrites that silently, so after provisioning
# an AKS cluster a probe meant for kind quietly aims at the managed one.
# They resolve the effective context with
# `kubectl config view --minify`, which honours a --context carried inside
# $KUBECTL; `config current-context` ignores that flag and would guard a
# different cluster than the one acted on.
#
# `make chat` is deliberately NOT guarded, though it does spend budget and
# write a ledger row. The distinction is not "mutates" but "can be aimed
# somewhere unintended": chat runs kmx with KUBE_CTX passed explicitly, and
# kmx puts that context on every kubectl call it makes (an empty one is
# refused outright), so it cannot silently retarget the way a bare-kubectl
# probe can. Prompting on the most-used
# command would buy nothing and teach people to type past confirmations.
#
# Usage:  KUBE_CTX=... [KUBE_NS=...] kube-guard.sh "<what is about to happen>"
# Confirm non-interactively with:  KAIMAHI_CONFIRM=$KUBE_CTX make <target>
set -euo pipefail

ACTION="${1:-cluster-mutating action}"
CTX="${KUBE_CTX:-}"
NS="${KUBE_NS:-kagent, kaimahi}"

if [ -z "$CTX" ]; then
  echo "kube-guard: KUBE_CTX is empty — refusing to act on an unnamed cluster." >&2
  exit 1
fi

# Resolve the context's API server out of the kubeconfig. `kubectl config
# view` never contacts a cluster, so this stays cheap and offline.
server=""
if ! raw=$(kubectl config view -o json 2>/dev/null); then
  echo "kube-guard: cannot read the kubeconfig — refusing to act blind." >&2
  exit 1
fi
server=$(printf '%s' "$raw" | CTX="$CTX" python3 -c '
import json, os, sys
cfg = json.load(sys.stdin)
want = os.environ["CTX"]
ctx = next((c for c in cfg.get("contexts") or [] if c.get("name") == want), None)
if ctx is None:
    sys.exit(0)  # absent: reported as "" and classified below
name = (ctx.get("context") or {}).get("cluster", "")
cluster = next((c for c in cfg.get("clusters") or [] if c.get("name") == name), None)
sys.stdout.write(((cluster or {}).get("cluster") or {}).get("server", ""))
')

host=""
if [ -n "$server" ]; then
  host=$(printf '%s' "$server" | python3 -c '
import sys
from urllib.parse import urlparse
sys.stdout.write(urlparse(sys.stdin.read().strip()).hostname or "")
')
fi

# Classify. Order matters: an ABSENT context is not automatically unsafe —
# `make up` on an empty machine legitimately names a kind context that
# does not exist yet, and CI depends on that. Absent + kind-named is
# "about to be created"; absent + anything else is a typo, which is
# exactly what this guard exists to catch.
case "$CTX" in
  kind-*) named_kind=yes ;;
  *) named_kind=no ;;
esac
case "$host" in
  127.0.0.1 | localhost | ::1 | 0.0.0.0) loopback=yes ;;
  *) loopback=no ;;
esac

if [ -z "$server" ]; then
  if [ "$named_kind" = yes ]; then
    posture="local kind (context not created yet)"
    local_kind=yes
  else
    echo "kube-guard: context '$CTX' is not in the kubeconfig." >&2
    echo "  Nothing was applied. Check the name with: kubectl config get-contexts" >&2
    echo "  (Only a kind-* context may be named before it exists — that is" >&2
    echo "   'make up' creating it. Any other name here is a typo.)" >&2
    exit 1
  fi
elif [ "$named_kind" = yes ] && [ "$loopback" = yes ]; then
  posture="local kind"
  local_kind=yes
else
  posture="REMOTE / non-kind"
  local_kind=no
fi

{
  echo "----------------------------------------------------------------"
  echo "  about to: $ACTION"
  echo "  context:  $CTX"
  echo "  server:   ${host:-<none yet>}"
  echo "  namespace(s): $NS"
  echo "  posture:  $posture"
  echo "----------------------------------------------------------------"
} >&2

[ "$local_kind" = yes ] && exit 0

# Remote: explicit confirmation naming the context, or nothing happens.
if [ -n "${KAIMAHI_CONFIRM:-}" ]; then
  if [ "$KAIMAHI_CONFIRM" = "$CTX" ]; then
    echo "kube-guard: confirmed via KAIMAHI_CONFIRM." >&2
    exit 0
  fi
  echo "kube-guard: KAIMAHI_CONFIRM does not name this context — refusing." >&2
  echo "  to proceed:  KAIMAHI_CONFIRM=$CTX make <target>" >&2
  exit 1
fi

# No pre-confirmation. Prompt only if a human is actually there to answer;
# a script or CI job reaching here must fail rather than hang or assume.
if [ ! -t 0 ]; then
  echo "kube-guard: '$CTX' is not a local kind cluster and there is no TTY to ask." >&2
  echo "  to proceed:  KAIMAHI_CONFIRM=$CTX make <target>" >&2
  exit 1
fi

printf 'Type the context name to continue (anything else aborts): ' >&2
IFS= read -r answer || answer=""
if [ "$answer" != "$CTX" ]; then
  echo "kube-guard: not confirmed — nothing was applied." >&2
  exit 1
fi
echo "kube-guard: confirmed." >&2
