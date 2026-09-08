# Thin glue over kind/AKS + helm + kubectl + the kagent CLI. No Kaimahi CLI
# here — kagent already ships one (see docs/getting-started.md for the full story).
#
# TARGET selects the environment. kind is the default and its
# behaviour is unchanged: every kind command, context, and manifest is
# exactly what it was before this file learned about anything else.
#
#   make up                      # kind, as always
#   TARGET=aks make ...          # a managed cluster (docs/aks.md)
#
# KUBE_CTX is now overridable, which is the whole point of the managed
# path — and also its one new hazard, since `make down` can suddenly name
# a cluster somebody cares about. Nearly every MUTATING target below
# therefore depends on `guard` (scripts/kube-guard.sh): it prints where
# the action is going, and demands explicit confirmation for anything that
# is not a local kind cluster. Fail closed — no confirmation, no action.
#
# The exceptions, stated so the rule is not trusted further than it holds:
# `netpol-verify` and `inbound-fire` run the same guard INSIDE their
# scripts, deriving the context from the KUBECTL they are handed rather
# than an inherited KUBE_CTX; `aks-cluster` (and `cluster` when
# TARGET=aks, which delegates to it) is what CREATES the cluster, so there
# is nothing to guard yet — it points the context at the new cluster, and
# everything after it is guarded; and `plane-image` and `erp-image` on AKS
# build in the registry and touch no cluster at all.
TARGET         ?= kind

# A bare `make` is build-only. Provisioning a cluster is consequential and
# stays behind the explicit `make up`; the default builds kmx and prints where
# it was written.
.DEFAULT_GOAL := build

# Container engine for the kind path. Explicit rather than auto-detected:
# which engine built an image is exactly the kind of thing that should be
# visible in the command, not inferred from what happens to be installed.
#   make up   CONTAINER_ENGINE=podman
# kind talks to podman only when KIND_EXPERIMENTAL_PROVIDER says so, so the
# two are set together and can never disagree — a cluster created under one
# engine is invisible to the other, which otherwise reads as "kind is
# broken".
CONTAINER_ENGINE ?= docker
ifeq ($(CONTAINER_ENGINE),podman)
KIND_ENV := KIND_EXPERIMENTAL_PROVIDER=podman
else ifeq ($(CONTAINER_ENGINE),docker)
KIND_ENV :=
else
$(error unknown CONTAINER_ENGINE '$(CONTAINER_ENGINE)' — expected 'docker' or 'podman')
endif
# NOT named KIND: that is already a user-facing parameter for
# `make request KIND=tool|budget`. Shadowing it would have made the usage
# check pass with "kind" and filed a nonsense approval request.
KIND_CMD       := $(KIND_ENV) kind

KIND_CLUSTER   ?= kaimahi-p1
AKS_CLUSTER    ?= kaimahi
KAGENT_VERSION ?= 0.9.12
MODEL          ?= qwen2.5:3b
AGENT          ?= hello-world
TASK           ?= Hello! Who are you and where are you running?
ifneq ($(filter environment environment override,$(origin AGENT)),)
override AGENT := hello-world
endif
ifneq ($(filter environment environment override,$(origin TASK)),)
override TASK := Hello! Who are you and where are you running?
endif
KAGENT         ?= bin/kagent
STATUS_OUTPUT  ?= table
export KMX_STATUS_OUTPUT := $(STATUS_OUTPUT)
SESSION        ?=
export KMX_CHAT_SESSION := $(SESSION)
export KMX_CHAT_AGENT := $(AGENT)
export KMX_CHAT_TASK := $(TASK)
export KMX_KIND_CLUSTER := $(KIND_CLUSTER)
export KMX_CONTAINER_ENGINE := $(CONTAINER_ENGINE)
export KMX_KAGENT_VERSION := $(KAGENT_VERSION)
export KMX_MODEL := $(MODEL)
export KMX_KAGENT := $(KAGENT)
export KMX_CONFIRM := $(KAIMAHI_CONFIRM)
KMX_CHAT_ARGS = $(strip $(if $(filter 1,$(INTERACTIVE)),--interactive) \
	$(if $(SESSION),--session "$$KMX_CHAT_SESSION") "$$KMX_CHAT_AGENT" \
	$(if $(filter 1,$(INTERACTIVE)),$(if $(filter command line,$(origin TASK)),"$$KMX_CHAT_TASK"),"$$KMX_CHAT_TASK"))

# ---- kmx: one implementation, and the targets below call it --------------
# The developer journey — cluster, model, kagent, the agents, a conversation,
# teardown — is implemented ONCE, in Go, in cmd/kmx. The targets below that
# used to spell it out in shell are now one-line recipes that call this
# binary, so CI keeps proving the code a developer actually runs. Everything
# else in this file (the plane, governance, approvals, Slack, GitHub, AKS,
# the probes) is unchanged and still make's.
#
# A developer who has cloned the repo gets kmx built from the checkout; a
# developer who has not runs `go install github.com/kaimahi-agents/kaimahi/cmd/kmx@<sha>`
# and never sees this file. Both run the same code.
KMX          ?= bin/kmx
KMX_SOURCES  := go.mod embed.go $(shell find cmd/kmx internal/kmx -name '*.go' 2>/dev/null)
# Everything embed.go carries is INSIDE the binary (kmx runs outside a
# clone), so editing any of it has to relink. This list is the same set as
# embed.go's three //go:embed blocks — manifests, blueprints and the scripts
# the managed path ships — and it drifted from them once already: twelve
# embedded files were missing, so an edit to any of them left a stale bin/kmx
# that `make sandbox`, `make lift` or a blueprint run then applied. It is now
# held to embed.go by a test that DERIVES the list from those directives
# (internal/kmx/delegation), rather than one that restates two filenames.
# Over-covering is harmless here — a spurious relink — so the directories use
# wildcards.
KMX_ASSETS   := k8s/ollama.yaml k8s/kagent-values.yaml k8s/hello-world.yaml k8s/tools-agent.yaml \
		k8s/kaimahi-tools.yaml \
		k8s/egress-hosted.yaml k8s/egress-copilot.yaml \
		k8s/wasm/runtime.yaml \
		$(wildcard k8s/plane/*.yaml) $(wildcard k8s/models/*.yaml) \
		$(wildcard k8s/observability/*) \
		$(wildcard blueprints/*) \
		scripts/release-publish.sh \
		scripts/aks-up.sh scripts/aks-down.sh scripts/plane-deploy.sh \
		scripts/netpol-probe.sh scripts/kube-guard.sh
# kmx reads the Makefile's own variable names, so delegation passes them
# through rather than translating. KAIMAHI_CONFIRM rides along so a
# confirmation given to make is not asked for again by kmx.
KMX_ENV       = KIND_CLUSTER="$$KMX_KIND_CLUSTER" KUBE_CTX="$$KMX_KUBE_CTX" \
		CONTAINER_ENGINE="$$KMX_CONTAINER_ENGINE" KAGENT_VERSION="$$KMX_KAGENT_VERSION" \
		MODEL="$$KMX_MODEL" $(if $(filter command line,$(origin CHAT_PORT)),CHAT_PORT="$$KMX_CHAT_PORT",) \
		$(if $(filter command line environment override,$(origin KAGENT)),KAGENT="$$KMX_KAGENT",) \
		ADMIN_PORT="$$KMX_ADMIN_PORT" OPS_PORT="$$KMX_OPS_PORT" \
		CRED="$$KMX_CRED" CRED_TOOLS="$$KMX_CRED_TOOLS" \
		KAIMAHI_CONFIRM="$$KMX_CONFIRM"
export KMX_CHAT_PORT := $(CHAT_PORT)

OS   := $(shell uname -s | tr A-Z a-z)
ARCH := $(shell uname -m | sed -e s/x86_64/amd64/ -e s/aarch64/arm64/)

# The plane image. The tag moves with the phase so a stale side-loaded
# image can never satisfy a newer manifest silently.
PLANE_IMAGE_REPO ?= kaimahi-proxy
PLANE_IMAGE_TAG  ?= p10
# The revision stamped into the binary for kaimahi_build_info; the
# image build context carries no .git. "unknown" outside a checkout.
PLANE_VERSION    ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)

# The demo ERP's image (k8s/erp-mcp.yaml). Same rules as the plane's:
# the tag moves with the phase that owns the image, so a stale one can
# never satisfy a newer manifest silently. On kind this is not used at all
# — that path side-loads the committed `kaimahi-erp:dev` tag and applies
# k8s/erp-mcp.yaml exactly as committed.
ERP_IMAGE_REPO   ?= kaimahi-erp
ERP_IMAGE_TAG    ?= p13

# ---- environment-dependent settings --------------------------------------
# Everything that genuinely differs between kind and a managed cluster is
# collected here, so the recipes below stay readable.
ifeq ($(TARGET),kind)
KUBE_CTX         ?= kind-$(KIND_CLUSTER)
# Side-loaded local image; `Never` is deliberate — see k8s/plane/proxy.yaml.
PLANE_IMAGE      ?= $(PLANE_IMAGE_REPO):$(PLANE_IMAGE_TAG)
PLANE_TARGET     := kind
# The keyless in-cluster model is the default everywhere on kind.
AGENT_MODELCONFIG ?= hello-world-model
GOVERNED_PRESET  ?= governed-ollama
# The proxy leaves the cluster only when Copilot is enabled
# (k8s/egress-copilot.yaml). Not on kind by default — the probe asserts
# the proxy is internet-free. Set to 1 after `make plane-copilot-secret`.
COPILOT_EGRESS   ?= 0
else ifeq ($(TARGET),aks)
KUBE_CTX         ?= $(AKS_CLUSTER)
# Built in Azure by `az acr build` and PULLED — a private ACR, never
# a public image.
PLANE_IMAGE      ?= $(ACR_NAME).azurecr.io/$(PLANE_IMAGE_REPO):$(PLANE_IMAGE_TAG)
PLANE_TARGET     := registry
# The demo ERP travels the same road as the proxy: built by the registry,
# pulled by the kubelet identity, never published. A private ACR is not
# publication, and the guardrail against publishing the demo ERP holds.
ERP_IMAGE        ?= $(ACR_NAME).azurecr.io/$(ERP_IMAGE_REPO):$(ERP_IMAGE_TAG)
ERP_TARGET       := registry
# Copilot-only on AKS. No Ollama is deployed there, so the agent goes
# straight onto the governed Copilot preset rather than the ollama one.
AGENT_MODELCONFIG ?= governed-copilot
GOVERNED_PRESET  ?= governed-copilot
# Copilot-only: the proxy's 443 allowance is always applied here.
COPILOT_EGRESS   ?= 1
else
$(error unknown TARGET '$(TARGET)' — expected 'kind' or 'aks')
endif

export KMX_KUBE_CTX := $(KUBE_CTX)

PLANE_PULL_POLICY ?= IfNotPresent
ERP_PULL_POLICY   ?= IfNotPresent
KUBECTL        := kubectl --context $(KUBE_CTX)
CRED           ?= hello-world
CRED_TOOLS     ?= hello-tools
export KMX_ADMIN_PORT := $(ADMIN_PORT)
export KMX_OPS_PORT := $(OPS_PORT)
export KMX_CRED := $(CRED)
export KMX_CRED_TOOLS := $(CRED_TOOLS)
TOOLS          ?= k8s_get_resources
# The Slack seam has its own credential, agent and allowlist. The
# read-only tool is allowlisted from the start; POSTING is not — it is
# the action a human approves (make approvals / make approve).
CRED_SLACK     ?= hello-slack
# SLACK_TOOLS is the gateway ALLOWLIST — the authority. Posting is absent
# from it deliberately; an approval is what admits the call.
SLACK_TOOLS    ?= conversations_history
SLACK_POST_TOOL := conversations_add_message
# SLACK_AGENT_TOOLS is the agent's SELECTION (kagent wires discovered ∩
# toolNames). It names the posting tool so a grant can take effect
# without editing the agent; while the tool is not allowlisted it is not
# projected, not discovered, and not in the agent's hands.
SLACK_AGENT_TOOLS ?= $(SLACK_TOOLS),$(SLACK_POST_TOOL)
# TOOLS as a JSON string array for the Agent patch, so the agent's
# toolNames stay aligned with the gateway allowlist ("-" -> empty).
comma          := ,
TOOLNAMES_JSON  = $(if $(filter -,$(TOOLS)),,"$(subst $(comma),"$(comma)",$(TOOLS))")
SLACK_TOOLNAMES_JSON = $(if $(filter -,$(SLACK_AGENT_TOOLS)),,"$(subst $(comma),"$(comma)",$(SLACK_AGENT_TOOLS))")
# The GitHub seam (GitHub's HOSTED MCP server behind the gateway)
# has its own credential, agent and allowlist. Two READ tools are
# allowlisted from the start; the write tool is not — it is the action
# a human approves (make approvals / make approve), and the token in
# plane custody is read-only anyway.
CRED_GITHUB    ?= hello-github
GITHUB_TOOLS   ?= list_issues,list_pull_requests
GITHUB_WRITE_TOOL := issue_write
GITHUB_AGENT_TOOLS ?= $(GITHUB_TOOLS),$(GITHUB_WRITE_TOOL)
GITHUB_TOOLNAMES_JSON = $(if $(filter -,$(GITHUB_AGENT_TOOLS)),,"$(subst $(comma),"$(comma)",$(GITHUB_AGENT_TOOLS))")
# The RELEASE seam (docs/release-agent.md) — the first thing Kaimahi
# is used FOR rather than demonstrated with. Its own credential, agent and
# allowlist, separate from the read-only GitHub demo above, because this
# credential's token can change a real repository.
#
# The READ tools are allowlisted from the start, and `make release-bind`
# additionally constrains them to one repository. The two consequential
# ones are not allowlisted and must never be: creating the release branch
# and dispatching a build are the actions a human approves, one call at a
# time. There is no destructive tool in either list, and none is
# offered by the servers either — the upstream table excludes them at the
# server with X-MCP-Exclude-Tools / X-MCP-Toolsets.
CRED_RELEASE   ?= release-agent
RELEASE_TOOLS  ?= get_latest_release,list_tags,list_releases,get_release_by_tag,list_pull_requests,list_commits,actions_list,actions_get,core_list_projects,pipelines_definition,pipelines_build,pipelines_build_log
RELEASE_ACT_TOOLS := create_branch,actions_run_trigger,pipelines_write
# The accounts-payable seam (the demo's fixture ERP behind the
# gateway) has its own credential, agent and allowlist. The SIX READ
# tools are allowlisted from the start. The three with consequences are
# not — and payment_schedule must NOT be added to this list even to
# "enable" the routine invoice: it carries a STANDING CONSTRAINT in
# k8s/plane/upstreams.yaml, and where a constraint exists it binds
# instead of the allowlist. Adding it here would change nothing about
# what is admitted and would misdescribe the control to the next reader.
CRED_AP        ?= ap-agent
AP_TOOLS       ?= invoice_get,invoice_list,po_get,receiving_get,contract_get,payment_policy_get
AP_ACT_TOOLS   := payment_schedule,dispute_open,vendor_notify
# The agent's SELECTION (kagent wires discovered ∩ toolNames): it names
# the consequential tools so an approval can take effect without editing
# the agent, while conferring nothing.
AP_AGENT_TOOLS ?= $(AP_TOOLS),$(AP_ACT_TOOLS)
AP_TOOLNAMES_JSON = $(if $(filter -,$(AP_AGENT_TOOLS)),,"$(subst $(comma),"$(comma)",$(AP_AGENT_TOOLS))")
AP_INVOICE     ?= INV-88134
# 1 = the approvals in `make ap-demo` / `make ap-injection` wait for a real
# person in a real Slack rather than a synthesised app_mention. The
# default keeps kind and CI exactly as they were.
AP_HUMAN       ?= 0

.PHONY: build up cluster ollama model kagent agent tools-agent chat down status guard \
	model-secret copilot-secret use use-ollama \
	plane plane-image plane-secrets govern budget ledger plane-copilot-secret \
	credentials credential-renew \
	govern-tools ungovern-tools tool-allow tool-allowlist tool-audit \
	approvals approve deny request grants approval-audit \
	slack-secret slack-mcp govern-slack slack-allow slack-audit \
	slack-post slack-down aks-cluster aks-creds aks-down \
	netpol-verify egress-copilot egress-copilot-off \
	inbound-credential inbound-secret inbound-fire inbound-audit \
	inbound-expose inbound-unexpose exposure-scan \
	slack-approvers notify-slack slack-mention \
	backup restore plane-metrics \
	github-secret github-revoke egress-hosted egress-hosted-off \
	govern-github github-allow github-audit github-ask github-down \
	erp erp-image erp-fixtures govern-ap ap-allow ap-audit ap-ask ap-demo ap-injection ap-down \
	release-secret ado-secret release-revoke govern-release release-allow \
	release-bind release release-audit release-down release-refresh

## build: build kmx from this checkout and print the resulting path
build: $(KMX)
	@echo "kmx ready: $(abspath $(KMX))"

# guard: the context-safety net every MUTATING target depends on. Prints
# the target context/namespaces; demands explicit confirmation for
# anything that is not a local kind cluster; fails closed. Make runs it
# once per invocation, so a single `make up` asks at most once.
#
# Unguarded: status, ledger, audits and the approvals lists (they read),
# and `chat`. Calling chat "read-only" would be wrong — it spends budget,
# writes a ledger row, and can burn a grant. It is unguarded because the
# line being drawn is not "mutates" but "can be aimed somewhere
# unintended": chat runs through $(KUBECTL), which carries an explicit
# --context, so it lands wherever the rest of the invocation was already
# going to land. The scripts/tool-*-probe.sh scripts mutate the same
# governance state and ARE guarded, because they run outside make and
# would otherwise follow whatever `kubectl config current-context` says —
# which `az aks get-credentials` rewrites. Mutation is why anyone cares;
# an inherited context is what makes it a surprise.
# MAKECMDGOALS is empty for a bare `make`, which would print an action-less
# banner; name the default goal instead.
guard:
	@KUBE_CTX='$(KUBE_CTX)' KUBE_NS='$(GUARD_NS)' \
		bash scripts/kube-guard.sh '$(if $(MAKECMDGOALS),$(MAKECMDGOALS),$(.DEFAULT_GOAL)) [TARGET=$(TARGET)]'

GUARD_NS ?= kagent, kaimahi, ollama

# Port-forward the kagent controller, WAIT FOR IT, then run $(1) against
# that forward. Factored out because `chat` and `slack-post` had the same
# recipe and the same defect.
#
# The old form was `port-forward ... >/dev/null 2>&1 & sleep 3` and then
# an invoke that trusted the CLI's default localhost:8083. Three problems,
# and the portable manifests make them reachable: running a kind and a
# managed verification at once is now a first-class workflow
# (docs/aks.md), and the ports collide.
#   1. If the bind failed because ANOTHER cluster's forward already held
#      8083, the error went to /dev/null and `kagent invoke` connected to
#      that forward instead — returning a real, plausible reply from the
#      wrong cluster. It does NOT fail closed: the controller on that
#      forward answers happily. (Demonstrated while reviewing this code.)
#      --context cannot protect this path, because the aiming happens at
#      the socket, not at kubectl.
#   2. `sleep 3` is a guess, not a readiness check.
#   3. The port was hardcoded, so the runbook's "move the ports" advice for
#      concurrent clusters could not be applied here at all.
# Now: an overridable CHAT_PORT, an explicit --kagent-url so the CLI cannot
# fall back to a port we did not open, and a wait for kubectl's own
# "Forwarding from" line that fails loudly if it never appears.
CHAT_PORT ?= 8083
# The CLI defaults to localhost:8083; name the port we actually opened so
# it can never fall back to someone else's.
KAGENT_INVOKE = $(KAGENT) --kagent-url http://127.0.0.1:$(CHAT_PORT) invoke

# $(1) is the agent whose Service must be servable; $(2) is the command;
# $(3) optionally narrows which transport failures are retried (see below).
#
# Before invoking, prove the agent is actually SERVABLE — do not infer it.
#
# `use` already waits (`wait_switched`: kagent reconcile, `rollout status`,
# the single-pod wait; then the Agent's Ready condition) and none of it
# is sufficient during a preset-switch rollout: CI failed here twice with
#   dial tcp <clusterIP>:8080: connect: connection refused
# because at the moment of the call the Service had no ready backend (the
# old pod removed, the new one not yet propagated) — kube-proxy REJECTs
# that, so it looks like a broken agent rather than a race. Checking the
# endpoint list is also too weak: it can read ready one instant and be
# empty the next.
#
# So make the check the same thing the caller needs: fetch the agent's own
# A2A card THROUGH the Service, via the API server's service proxy. That
# resolves endpoints server-side and returns a real HTTP body, so it fails
# while there is no ready backend and succeeds only once the agent answers.
# (The kagent readiness probe uses the same path.) One kubectl call — no
# extra port, no second forward.
#
# This replaces the `sleep 3` the recipe used to rely on. That sleep was
# quietly doing this job: it is why `main` passes and why removing it
# surfaced the race. Padding is not a readiness check.
#
# The probe alone is NOT sufficient, and it is worth being precise about
# why: the API server's service proxy resolves the endpoint and connects to
# the POD directly, so it can succeed while kube-proxy has not yet
# programmed the ClusterIP the controller dials. Only the controller can
# answer "can I reach the agent", so the invoke below additionally retries
# a bounded number of times on exactly that error. It cannot mask a real
# outage: after the retries the original output and exit status are
# emitted unchanged, and a transport-error reply still fails
# verify-chat.py. Note kagent exits 0 on this error, so the retry keys on
# the message, not the status.
#
# The race has more than one symptom, and the first fix only caught one of
# them. `connection refused` is kube-proxy REJECTing when the Service has
# no ready backend; but once a backend IS programmed and the pod tears the
# connection down before answering, the controller reports instead:
#   failed to send HTTP request: Post "http://<agent>.kagent:8080": EOF
# That is the same race one moment later, and it reddened main once the
# managed-cluster path landed. Retry both.
#
# The predicate is anchored to the controller's WHOLE error line, not to
# transport text anywhere in the output. The output being matched is the
# combined stdout+stderr, and stdout carries the A2A task JSON — including
# the model's own reply. An agent asked to explain one of these errors
# (the FAQ documents them) would echo the words and, with a loose match,
# trigger a second invoke: duplicate spend, and for tool calls a burned
# grant. kagent prints the failure as one line, "Error invoking session:
# <wrapped error>", ending in Go's net error; anchor both ends so a line
# that starts with `{` can never match.
#
# Two classes, because they are not equally safe to retry:
#   REFUSED  — kube-proxy REJECTed; nothing reached the agent. Always safe.
#   AMBIGUOUS — EOF / connection reset: the request may have reached the
#              agent and been acted on before the connection dropped.
# `chat` retries both (re-asking a question is acceptable). Anything whose
# task performs a non-idempotent action — slack-post, which POSTS to a
# channel under a USES-bounded grant — retries only the refused class:
# a retry after an ambiguous failure could post twice. Pass the class as
# $(3); it defaults to both.
CHAT_ERROR_LINE  = ^Error invoking session: .*failed to send HTTP request: Post "[^"]*": 
CHAT_REFUSED     = dial tcp [^ ]*: connect: connection refused
CHAT_AMBIGUOUS   = EOF|(read|write) tcp [^ ]*: (read|write): connection reset by peer
CHAT_RETRYABLE      = '$(CHAT_ERROR_LINE)($(CHAT_REFUSED)|$(CHAT_AMBIGUOUS))$$'
CHAT_RETRYABLE_SAFE = '$(CHAT_ERROR_LINE)($(CHAT_REFUSED))$$'
define kagent_forward
agent_ok=; \
for _ in $$(seq 1 120); do \
	if $(KUBECTL) -n kagent get --raw \
		'/api/v1/namespaces/kagent/services/$(1):8080/proxy/.well-known/agent-card.json' \
		>/dev/null 2>&1; then agent_ok=1; break; fi; \
	sleep 1; \
done; \
if [ -z "$$agent_ok" ]; then \
	echo "agent '$(1)' is not answering through its Service after 120s — refusing to invoke" >&2; \
	echo "  (invoking now would fail with a transport error from the controller)" >&2; \
	exit 1; \
fi; \
pf_out=$$(mktemp); \
$(KUBECTL) -n kagent port-forward --address 127.0.0.1 \
	svc/kagent-controller $(CHAT_PORT):8083 >"$$pf_out" 2>&1 & \
pf=$$!; trap 'kill $$pf 2>/dev/null; rm -f "$$pf_out"' EXIT; \
ready=; \
for _ in $$(seq 1 80); do \
	if grep -q "Forwarding from 127.0.0.1:$(CHAT_PORT)" "$$pf_out" 2>/dev/null; then ready=1; break; fi; \
	kill -0 $$pf 2>/dev/null || break; \
	sleep 0.25; \
done; \
if [ -z "$$ready" ]; then \
	echo "port-forward to kagent-controller never came up on 127.0.0.1:$(CHAT_PORT):" >&2; \
	sed 's/^/  /' "$$pf_out" >&2; \
	echo "  Refusing to invoke: if another cluster's forward holds this port," >&2; \
	echo "  the task would have run THERE. Use CHAT_PORT=<free port>." >&2; \
	exit 1; \
fi; \
out=$$(mktemp); rc=0; \
for attempt in 1 2 3 4; do \
	rc=0; $(2) >"$$out" 2>&1 || rc=$$?; \
	grep -Eq $(if $(3),$(3),$(CHAT_RETRYABLE)) "$$out" || break; \
	if [ "$$attempt" != 4 ]; then \
		echo "kagent could not reach agent '$(1)' yet (transport error); retry $$attempt/3 in 5s" >&2; \
		sleep 5; \
	fi; \
done; \
cat "$$out"; rm -f "$$out"; \
exit $$rc
endef

# The `up` journey differs by environment. On kind it is unchanged. On AKS
# there is no Ollama (that path is Copilot-only), and governance has to exist
# BEFORE the agents do, because the agents go straight onto the governed
# Copilot preset — there is no keyless model for them to start on.
ifeq ($(TARGET),kind)
# The kind journey is kmx's, in one process — so it guards once, and so
# the sequence CI runs is the sequence the binary implements, not a list of
# targets that could drift from it. The individual steps below are still
# addressable (`make cluster`, `make agent`, ...) and delegate one step each.
UP_STEPS :=
else
# The Copilot credential is minted BEFORE the plane is deployed, not
# after. The proxy mounts kaimahi-copilot-token as an OPTIONAL secret
# volume, so a pod that starts before the Secret exists comes up with an
# empty mount and every governed Copilot call fails closed with "upstream
# credential unavailable" until kubelet gets round to projecting it.
# Minting first makes the first chat on a fresh cluster work. (kind never
# hit this: its governed demo path is ollama, which needs no upstream
# credential.)
UP_STEPS := cluster kagent plane-copilot-secret plane govern agent \
	tools-agent govern-tools status
endif

## up: everything from an empty machine to ready agents (hello-world + tools)
#
# `up` deliberately does NOT list `guard` itself — each step below does,
# and make runs it once per invocation, so the prompt still happens
# exactly once. Guarding here too would break the headline case: on a
# genuinely empty Azure subscription the AKS context does not exist yet,
# and an absent NON-kind context is precisely what the guard refuses as a
# typo. The first step (`cluster`) is what brings that context into
# existence; every step after it is guarded, by which time there is a real
# context to check. (On kind the whole journey is one `kmx up`, which runs
# the same guard once, in-process, before it touches anything.)
ifeq ($(TARGET),kind)
up: $(KMX)
	@$(KMX_ENV) $(KMX) up
else
up: $(UP_STEPS)
endif

ifeq ($(TARGET),kind)
# The Podman recovery #53 added to this recipe — restart the cluster's
# stopped nodes rather than trying to create a cluster that already exists,
# refuse when kind lists a cluster Podman has no nodes for, and wait for
# /readyz and CoreDNS before returning — moved into kmx with it, so the one
# implementation keeps it. See internal/kmx/app/up.go, stepCluster.
cluster: $(KMX)
	@$(KMX_ENV) $(KMX) up --step cluster
else
## cluster (TARGET=aks): resource group + private ACR + AKS, via the az CLI
cluster: aks-cluster
endif

# Ollama is the kind path's keyless model server. On AKS it is deliberately
# not deployed there: the keyless path is already proven on kind by CI on
# every PR, and AKS's job is proving the plane runs on a managed cluster
# with a real model. Refuse loudly rather than half-deploying it.
#
# The non-kind forms carry no `guard`: they touch nothing, so making the
# operator confirm a cluster before being told the target does not apply
# there is pure friction — and friction is what teaches people to type
# past confirmations.
ifeq ($(TARGET),kind)
ollama: $(KMX)
	@$(KMX_ENV) $(KMX) up --step ollama

model: $(KMX)
	@$(KMX_ENV) $(KMX) up --step model
else
ollama:
	@echo 'ollama is not deployed on TARGET=$(TARGET) — the managed path is' >&2
	@echo 'Copilot-only. See docs/aks.md.' >&2
	@exit 1

model:
	@echo 'no Ollama on TARGET=$(TARGET) — nothing to pull.' >&2
	@exit 1
endif

ifeq ($(TARGET),kind)
kagent: $(KMX)
	@$(KMX_ENV) $(KMX) up --step kagent
else
kagent: guard
	helm upgrade --install kagent-crds \
		oci://ghcr.io/kagent-dev/kagent/helm/kagent-crds \
		--version $(KAGENT_VERSION) --namespace kagent --create-namespace \
		--kube-context $(KUBE_CTX)
	helm upgrade --install kagent \
		oci://ghcr.io/kagent-dev/kagent/helm/kagent \
		--version $(KAGENT_VERSION) --namespace kagent \
		--kube-context $(KUBE_CTX) -f k8s/kagent-values.yaml
	$(KUBECTL) -n kagent wait --for=condition=Ready pods --all --timeout=420s
endif

# Re-applying the committed YAML must not silently drop governance (or
# any preset switch) from a live agent: capture the current modelConfig
# first and restore a non-default one after the apply, with a warning.
# Only a NotFound (fresh cluster) may skip the capture — any other read
# failure aborts rather than risk silently un-governing.
#
# The managed path generalises the same mechanism one step. The committed
# artifact names the keyless ollama ModelConfig, which does not exist on a
# Copilot-only managed cluster, so the desired config is:
#   a live non-default one (preserve it, as before)  else
#   $(AGENT_MODELCONFIG)  — hello-world-model on kind (identical to the
#   previous behaviour: the patch branch is simply never taken), and the
#   governed Copilot preset on AKS.
# k8s/hello-world.yaml is still never mutated.
ifeq ($(TARGET),kind)
## agent: the hello-world agent (kmx owns the preservation rules above)
agent: $(KMX)
	@$(KMX_ENV) $(KMX) up --step agent

## tools-agent: the tools-enabled agent
tools-agent: $(KMX)
	@$(KMX_ENV) $(KMX) up --step tools-agent
else
agent: guard
	@current=""; \
	if out=$$($(KUBECTL) -n kagent get agent hello-world \
		-o jsonpath='{.spec.declarative.modelConfig}' 2>&1); then \
		current=$$out; \
	elif ! printf '%s' "$$out" | grep -q 'NotFound'; then \
		echo "cannot read hello-world's live modelConfig (refusing to risk un-governing it): $$out" >&2; exit 1; \
	fi; \
	desired='$(AGENT_MODELCONFIG)'; \
	if [ -n "$$current" ] && [ "$$current" != hello-world-model ]; then \
		desired=$$current; \
		echo "NOTE: hello-world was on modelConfig '$$current' — preserving it ('make use PRESET=ollama' resets)" >&2; \
	fi; \
	$(KUBECTL) apply -f k8s/hello-world.yaml && \
	if [ "$$desired" != hello-world-model ]; then \
		$(KUBECTL) -n kagent patch agent hello-world --type merge \
			-p "{\"spec\":{\"declarative\":{\"modelConfig\":\"$$desired\"}}}"; \
	fi
	$(KUBECTL) -n kagent wait \
		--for=jsonpath='{.status.conditions[?(@.type=="Ready")].status}'=True \
		agent/hello-world --timeout=300s

## tools-agent: the tools-enabled agent (kagent-tools MCP server comes
## from the kagent helm install; this applies the Agent wired to it)
# Same desired-modelConfig treatment as `agent` above — but note this one
# IS a behavioural delta on kind, not just a generalisation: previously
# `tools-agent` never read modelConfig, so re-applying always reset
# hello-tools to the committed value. It now preserves a live non-default
# one. That is deliberate and matches the governance-preservation guard
# just below (which already covers hello-tools' gateway wiring); it is
# unreachable in every documented kind workflow, because nothing switches
# hello-tools' model — `make use` and `make govern` only touch
# hello-world. It matters on AKS, where hello-tools must come up on the
# governed Copilot preset.
tools-agent: guard
	$(KUBECTL) -n kagent wait \
		--for=jsonpath='{.status.conditions[?(@.type=="Accepted")].status}'=True \
		remotemcpserver/kagent-tool-server --timeout=300s
	@server=""; tools=""; current=""; \
	if out=$$($(KUBECTL) -n kagent get agent hello-tools -o json 2>&1); then \
		server=$$(printf '%s' "$$out" | python3 -c 'import json,sys; t=(json.load(sys.stdin)["spec"].get("declarative") or {}).get("tools") or []; print((t[0].get("mcpServer") or {}).get("name","") if t else "")') || exit 1; \
		tools=$$(printf '%s' "$$out" | python3 -c 'import json,sys; print(json.dumps((json.load(sys.stdin)["spec"].get("declarative") or {}).get("tools") or []))') || exit 1; \
		current=$$(printf '%s' "$$out" | python3 -c 'import json,sys; print((json.load(sys.stdin)["spec"].get("declarative") or {}).get("modelConfig",""))') || exit 1; \
	elif ! printf '%s' "$$out" | grep -q 'NotFound'; then \
		echo "cannot read hello-tools' live tool wiring (refusing to risk un-governing it): $$out" >&2; exit 1; \
	fi; \
	desired='$(AGENT_MODELCONFIG)'; \
	if [ -n "$$current" ] && [ "$$current" != hello-world-model ]; then desired=$$current; fi; \
	$(KUBECTL) apply -f k8s/tools-agent.yaml && \
	if [ "$$desired" != hello-world-model ]; then \
		$(KUBECTL) -n kagent patch agent hello-tools --type merge \
			-p "{\"spec\":{\"declarative\":{\"modelConfig\":\"$$desired\"}}}"; \
	fi && \
	if [ "$$server" = kaimahi-tools ] && [ -n "$$tools" ]; then \
		echo "NOTE: hello-tools was governed via kaimahi-tools — restoring gateway wiring ('make ungovern-tools' opts out)" >&2; \
		$(KUBECTL) -n kagent patch agent hello-tools --type merge \
			-p "{\"spec\":{\"declarative\":{\"tools\":$$tools}}}"; \
	fi
	$(KUBECTL) -n kagent wait \
		--for=jsonpath='{.status.conditions[?(@.type=="Ready")].status}'=True \
		agent/hello-tools --timeout=300s
endif

## chat: one question by default; INTERACTIVE=1 keeps a session open.
## Override AGENT=hello-tools for the tools-enabled agent; SESSION=<id> resumes.
#
# Delegated on every TARGET: kmx's `agent chat` is this recipe's
# `kagent_forward` — the servable-through-the-Service check, the waited-for
# port-forward on an explicit port, and the same two retry classes. The
# define itself stays because `slack-post` and `github-ask` still use it,
# with the narrower refused-only class a non-idempotent action needs.
chat: $(KMX)
	@$(KMX_ENV) $(KMX) agent chat $(KMX_CHAT_ARGS)

## model-secret: store an API key as a K8s Secret, stdin-only (paste, Enter, Ctrl-D).
# The key never touches argv, env listings, YAML, or logs; tr strips the
# trailing newline so it doesn't corrupt the Authorization header.
model-secret: guard
	@test -n "$(NAME)" || { echo 'usage: make model-secret NAME=<preset>-api-key' >&2; exit 1; }
	@echo 'Paste the API key, press Enter, then Ctrl-D:' >&2
	@tr -d '\n' | $(KUBECTL) -n kagent create secret generic $(NAME) \
		--from-file=api-key=/dev/stdin

## copilot-secret: GitHub device login (cached), then mint a short-lived
## Copilot API token and store it as the github-copilot-token Secret.
## Fail-closed, token bytes only in pipes/0600 files — see the script.
copilot-secret: guard
	@KUBECTL="$(KUBECTL)" bash scripts/copilot-secret.sh

# Wait until an agent's switch has fully landed: kagent has reconciled the
# Agent's current generation, the Deployment has rolled, and it is served
# by EXACTLY ONE pod carrying the current pod-template-hash. $(1) is the
# agent name. Replaces the bare `rollout status` every switch used to do.
#
# Three waits, because each of the obvious signals is wrong on its own:
#
# 1. kagent reconciles the Agent ASYNCHRONOUSLY. Straight after the patch,
#    `rollout status` can run before the controller has rewritten the
#    Deployment and report "successfully rolled out" about the OLD
#    template. So first wait for status.observedGeneration to reach the
#    Agent's generation. (The Ready condition is useless here: it stays
#    True, at the old observedGeneration, for the whole rollout.)
# 2. `rollout status` is then the right rollout signal, but with maxSurge
#    1 / maxUnavailable 0 it returns the moment the NEW pod is Ready —
#    while the OLD pod, still on the previous ModelConfig, is Terminating
#    (the ReplicaSet controller stops counting a pod as soon as it has a
#    deletionTimestamp, so the rollout looks complete). A chat in that
#    window can land on the old pod: a governed chat once completed with
#    an EMPTY ledger because the ungoverned pod answered it. "Governed"
#    must mean the ungoverned pod is gone, not outnumbered.
# 3. So finally wait for the pod set. The current hash is read from the
#    ReplicaSet whose revision matches the Deployment's (kagent stamps a
#    config-hash on the pod template, so a ModelConfig switch that changes
#    anything cuts a new ReplicaSet; one that changes nothing — e.g.
#    hello-world-model → ollama, identical specs — rolls nothing and
#    passes straight through). The pod listing is compared as a whole: it
#    equals the hash only when there is one pod and it is that pod.
#    Terminating pods still list, which is the point.
#
# Bounded: after `rollout status` the remaining work is the old pod's
# termination grace, so 120s is generous; on timeout the pod list is
# printed and the target fails rather than handing a half-switched agent
# to the next step. Wait 1 keys on the AGENT's generation, so a switch
# that changes only a referenced object's content (the preset's YAML
# edited, the agent already on it) passes it at once; `use` covers that
# case itself, before calling here — see the recipe.
define wait_switched
gen=$$($(KUBECTL) -n kagent get agent/$(1) -o jsonpath='{.metadata.generation}') \
	&& [ -n "$$gen" ] || { echo "cannot read agent/$(1)'s generation" >&2; exit 1; }; \
$(KUBECTL) -n kagent wait --for=jsonpath='{.status.observedGeneration}'=$$gen \
	agent/$(1) --timeout=120s >/dev/null || exit 1; \
$(KUBECTL) -n kagent rollout status deploy/$(1) --timeout=180s || exit 1; \
rev=$$($(KUBECTL) -n kagent get deploy/$(1) \
	-o jsonpath='{.metadata.annotations.deployment\.kubernetes\.io/revision}') \
	&& [ -n "$$rev" ] || { echo "cannot read deploy/$(1)'s revision" >&2; exit 1; }; \
hash=$$($(KUBECTL) -n kagent get rs -l kagent=$(1) \
	-o jsonpath='{range .items[*]}{.metadata.annotations.deployment\.kubernetes\.io/revision} {.metadata.labels.pod-template-hash}{"\n"}{end}' \
	| awk -v r="$$rev" '$$1==r{print $$2}'); \
[ -n "$$hash" ] || { echo "no ReplicaSet at revision $$rev for deploy/$(1)" >&2; exit 1; }; \
single=; \
for _ in $$(seq 1 60); do \
	pods=$$($(KUBECTL) -n kagent get pods -l kagent=$(1) \
		-o jsonpath='{range .items[*]}{.metadata.labels.pod-template-hash}{"\n"}{end}') || exit 1; \
	if [ "$$pods" = "$$hash" ]; then single=1; break; fi; \
	sleep 2; \
done; \
if [ -z "$$single" ]; then \
	echo "deploy/$(1): still not exactly one pod on template $$hash after 120s:" >&2; \
	$(KUBECTL) -n kagent get pods -l kagent=$(1) -o wide >&2; \
	exit 1; \
fi
endef

## use: switch the hello-world agent to a model preset from k8s/models/
# (e.g. make use PRESET=anthropic). Hosted presets need their Secret first
# (make model-secret) — and remember: spend through a preset is ungoverned
# until the governance plane is in front of it (make plane).
#
# One shell for apply + patch, because the wait that follows needs three
# values from BEFORE them. `wait_switched` keys its reconcile wait on the
# Agent's generation, which only moves when the Agent's spec does. Two
# switches leave it still and yet must roll the pods:
#   - the preset's YAML changed and hello-world is already on it (the
#     patch is a no-op; kagent rolls on the ModelConfig change alone);
#   - the preset was just CREATED under a name the Agent already carried.
# Verified 2026-09-02: a content-only ModelConfig change bumps its
# generation and kagent cuts a new Deployment revision within a second —
# without an Agent generation change. So when the ModelConfig's
# generation moved and the Agent's did not, wait (bounded, loud) for the
# Deployment's revision to advance past what it was before the apply;
# only then is `wait_switched`'s rollout/pod check looking at the new
# template. Identical content ("unchanged") moves neither generation and
# takes the fast path. Only a genuine NotFound may leave the "before"
# generation empty; any other read failure aborts.
ifeq ($(TARGET),kind)
# kmx owns the kind path. `wait_switched` — the three-deep wait this
# recipe used to spell out, every layer of it paid for by a flake — lives in
# internal/kmx/app/use.go with its reasons attached, and is now the ONE
# implementation the governed-tools switch shares.
use: $(KMX)
	@$(KMX_ENV) $(KMX) use $(PRESET)

## use-ollama: switch back to the keyless in-cluster model
use-ollama: $(KMX)
	@$(KMX_ENV) $(KMX) use ollama
else
use: guard
	@test -n "$(PRESET)" || { echo 'usage: make use PRESET=<name from k8s/models/>' >&2; exit 1; }
	@mc0=""; \
	if out=$$($(KUBECTL) -n kagent get modelconfig/$(PRESET) -o jsonpath='{.metadata.generation}' 2>&1); then \
		mc0=$$out; \
	elif ! printf '%s' "$$out" | grep -q 'NotFound'; then \
		echo "cannot read modelconfig/$(PRESET): $$out" >&2; exit 1; \
	fi; \
	agent0=$$($(KUBECTL) -n kagent get agent/hello-world -o jsonpath='{.metadata.generation}') || exit 1; \
	rev0=$$($(KUBECTL) -n kagent get deploy/hello-world \
		-o jsonpath='{.metadata.annotations.deployment\.kubernetes\.io/revision}') || exit 1; \
	echo "$(KUBECTL) apply -f k8s/models/$(PRESET).yaml"; \
	$(KUBECTL) apply -f k8s/models/$(PRESET).yaml || exit 1; \
	echo "$(KUBECTL) -n kagent patch agent hello-world (modelConfig: $(PRESET))"; \
	$(KUBECTL) -n kagent patch agent hello-world --type merge \
		-p '{"spec":{"declarative":{"modelConfig":"$(PRESET)"}}}' || exit 1; \
	mc1=$$($(KUBECTL) -n kagent get modelconfig/$(PRESET) -o jsonpath='{.metadata.generation}') || exit 1; \
	agent1=$$($(KUBECTL) -n kagent get agent/hello-world -o jsonpath='{.metadata.generation}') || exit 1; \
	if [ "$$agent1" = "$$agent0" ] && [ "$$mc1" != "$$mc0" ]; then \
		echo "NOTE: preset '$(PRESET)' changed while hello-world was already on it — waiting for kagent to cut a new revision (was $$rev0)" >&2; \
		rolled=; \
		for _ in $$(seq 1 60); do \
			rev=$$($(KUBECTL) -n kagent get deploy/hello-world \
				-o jsonpath='{.metadata.annotations.deployment\.kubernetes\.io/revision}') || exit 1; \
			if [ -n "$$rev" ] && [ "$$rev" -gt "$${rev0:-0}" ]; then rolled=1; break; fi; \
			sleep 2; \
		done; \
		if [ -z "$$rolled" ]; then \
			echo "deploy/hello-world: revision still $$rev0 after 120s — kagent did not roll for the changed preset; refusing to call it switched" >&2; \
			exit 1; \
		fi; \
	fi
	@$(call wait_switched,hello-world)
	$(KUBECTL) -n kagent wait \
		--for=jsonpath='{.status.conditions[?(@.type=="Ready")].status}'=True \
		agent/hello-world --timeout=300s

## use-ollama: switch back to the keyless in-cluster model
# The confirmation is passed down deliberately: reaching this line means
# the guard above already asked about THIS context and was answered, so
# the sub-make must not ask a second time for the same action.
use-ollama: guard
	$(MAKE) use PRESET=ollama KAIMAHI_CONFIRM='$(KUBE_CTX)'
endif

## ---- the governance plane (docs/spend.md) ----

ifeq ($(TARGET),kind)
## plane: build + deploy the Kaimahi proxy and its Postgres ledger
# kmx owns the kind path: it builds the proxy image, bootstraps the
# plane's secrets, applies k8s/plane/ UNRENDERED, and always restarts the
# proxy, since a rebuilt image under the same tag leaves the spec unchanged.
#
# `--source .` is the load-bearing argument. Without it kmx FETCHES the plane
# from the public Go proxy at its own revision, which is the right answer for
# someone who has no clone and the wrong one here: a pull request that
# changes plane/ must be proved against the code it changes.
plane: $(KMX)
	@$(KMX_ENV) $(KMX) plane --source .
else
## plane: build + deploy the Kaimahi proxy and its Postgres ledger
plane: guard plane-image plane-secrets plane-certificate
	@KUBECTL="$(KUBECTL)" PLANE_TARGET=$(PLANE_TARGET) \
		PLANE_IMAGE='$(PLANE_IMAGE)' PLANE_PULL_POLICY=$(PLANE_PULL_POLICY) \
		bash scripts/plane-deploy.sh
	$(KUBECTL) -n kaimahi rollout status deploy/kaimahi-postgres --timeout=300s
	@# Always restart: a rebuilt image under the SAME tag leaves the spec
	@# unchanged, so apply alone would keep the old binary running (kind's
	@# imagePullPolicy: Never reuses same-tag images without complaint, and
	@# a registry target with IfNotPresent behaves the same way).
	$(KUBECTL) -n kaimahi rollout restart deploy/kaimahi-proxy
	$(KUBECTL) -n kaimahi rollout status deploy/kaimahi-proxy --timeout=300s

endif

ifeq ($(TARGET),kind)
# kmx builds the image and side-loads it. The engine-aware load this recipe
# used to spell out — podman saves an archive because `kind load
# docker-image` cannot see podman's images, docker loads directly because it
# can and it skips a ~19MB tarball — moved into internal/kmx/app/plane.go
# with its reason attached.
plane-image: $(KMX)
	@$(KMX_ENV) $(KMX) plane --step image --source .
else
## plane-image (TARGET=aks): build IN Azure with ACR Tasks. No local docker
## build and no `docker push`: the source is uploaded and built by the
## registry, so nothing has to be logged in to a registry locally and no
## image ever leaves the private ACR.
plane-image:
	@test -n "$(ACR_NAME)" || \
		{ echo 'ACR_NAME is required for TARGET=aks (see docs/aks.md)' >&2; exit 1; }
	az acr build --registry $(ACR_NAME) --build-arg VERSION=$(PLANE_VERSION) \
		--image $(PLANE_IMAGE_REPO):$(PLANE_IMAGE_TAG) plane/
endif

ifeq ($(TARGET),kind)
plane-secrets: $(KMX)
	@$(KMX_ENV) $(KMX) plane --step secrets
else
plane-secrets: guard
	@KUBECTL="$(KUBECTL)" bash scripts/plane-secrets.sh
endif

## plane-certificate: mint or renew the certificate the two data seams
## serve with, and publish the authority agents verify it against
#
# One implementation on every target (D27): the minting, the create-once
# authority and the renewal decision are all decidable without a cluster and
# live in internal/kmx/seamcert, so there is no shell version of them to keep
# in step. The proxy refuses to start without this material, which is why it
# is a prerequisite of `plane` rather than something to remember.
plane-certificate: $(KMX)
	@$(KMX_ENV) $(KMX) plane --step certificate

## govern: issue the Kaimahi credential (opaque token -> agent-side
## Secret), apply the governed presets, switch hello-world through the
## proxy. The agent never sees a real upstream key.
#
# Both governed presets are applied on every target, but which one
# the agent is switched to depends on the environment ($(GOVERNED_PRESET):
# governed-ollama on kind, governed-copilot on AKS where no Ollama exists).
# The switch is also skipped when the agent is not there yet — on a
# managed cluster governance is stood up BEFORE the agents, because the
# agents have no keyless model to start on. On kind the agent always
# exists by this point, so the path taken is the one it always was.
# On kind this is kmx's, waits and NotFound discrimination included; the
# managed path below is unchanged — kmx drives the kind path only.
ifeq ($(TARGET),kind)
govern: $(KMX)
	@$(KMX_ENV) $(KMX) govern $(CRED) --agent hello-world --preset $(GOVERNED_PRESET)
else
govern: guard
	@KUBECTL="$(KUBECTL)" bash scripts/plane-admin.sh issue $(CRED)
	$(KUBECTL) apply -f k8s/models/governed-ollama.yaml
	$(KUBECTL) apply -f k8s/models/governed-copilot.yaml
	@# Only a genuine NotFound may skip the switch. `>/dev/null 2>&1` would
	@# collapse NotFound with an unreachable API server, an expired
	@# credential, an RBAC denial or a wrong context — every one of which
	@# would print the reassuring NOTE, exit 0, and leave hello-world on an
	@# UNGOVERNED preset, spending outside the plane. Same discrimination
	@# the `agent` target above already applies for the same reason.
	@if out=$$($(KUBECTL) -n kagent get agent hello-world 2>&1); then \
		$(MAKE) use PRESET=$(GOVERNED_PRESET) KAIMAHI_CONFIRM='$(KUBE_CTX)'; \
	elif printf '%s' "$$out" | grep -q 'NotFound'; then \
		echo "NOTE: agent hello-world does not exist yet — it will be created on '$(AGENT_MODELCONFIG)' by 'make agent'" >&2; \
	else \
		echo "cannot tell whether hello-world exists (refusing to leave it ungoverned): $$out" >&2; exit 1; \
	fi
endif

## budget: set monthly caps for a credential, e.g.
##   make budget CAP_CENTS=100 CAP_TOKENS=-     (- or empty = no cap)
ifeq ($(TARGET),kind)
budget: $(KMX)
	@$(KMX_ENV) $(KMX) budget $(CRED) --cents "$(if $(CAP_CENTS),$(CAP_CENTS),-)" --tokens "$(if $(CAP_TOKENS),$(CAP_TOKENS),-)"
else
budget: guard
	@KUBECTL="$(KUBECTL)" bash scripts/plane-admin.sh budget $(CRED) \
		"$(if $(CAP_CENTS),$(CAP_CENTS),-)" "$(if $(CAP_TOKENS),$(CAP_TOKENS),-)"
endif

## credentials: list the governed credentials and when each one expires.
## The state column is what an operator scans: EXPIRED, EXPIRING (inside
## the week's warning window), ok, or "no expiry" — the legacy class,
## issued before credentials expired, still valid, and only ever
## shrinking. Reads only; unguarded like `ledger`.
ifeq ($(TARGET),kind)
credentials: $(KMX)
	@$(KMX_ENV) $(KMX) credentials
else
credentials:
	@KUBECTL="$(KUBECTL)" bash scripts/plane-admin.sh credentials
endif

## credential-renew: extend a credential's expiry, e.g.
##   make credential-renew NAME=hello-world TTL=720h
## No token moves — renewal changes a date, so no Secret has to be
## rewritten and nothing has to travel. Rotating the MATERIAL is still
## `make govern` against a fresh name.
ifeq ($(TARGET),kind)
credential-renew: $(KMX)
	@test -n "$(NAME)" || { echo 'credential-renew: NAME=<credential> is required' >&2; exit 1; }
	@$(KMX_ENV) $(KMX) credential renew $(NAME) --ttl "$(if $(TTL),$(TTL),-)"
else
credential-renew: guard
	@test -n "$(NAME)" || { echo 'credential-renew: NAME=<credential> is required' >&2; exit 1; }
	@KUBECTL="$(KUBECTL)" bash scripts/plane-admin.sh renew $(NAME) "$(if $(TTL),$(TTL),-)"
endif

## ledger: show the spend ledger (newest first) + month-to-date totals
ifeq ($(TARGET),kind)
ledger: $(KMX)
	@$(KMX_ENV) $(KMX) ledger $(CRED)
else
ledger:
	@KUBECTL="$(KUBECTL)" bash scripts/plane-admin.sh ledger $(CRED)
endif

## ---- running it for real (docs/operations.md) ----

## backup: pg_dump the plane's database to a local file (default
## backups/kaimahi-<UTC timestamp>.sql). Streams through kubectl exec
## over the Postgres pod's unix socket — no password leaves the pod, no
## local client needed. The dump holds credential HASHES and audit
## trails, never a token or an upstream key; keep it as you would the
## database. Reads only; unguarded like `ledger`.
##   make backup [FILE=path]
ifeq ($(TARGET),kind)
backup: $(KMX)
	@$(KMX_ENV) $(KMX) backup $(FILE)
else
backup:
	@KUBECTL="$(KUBECTL)" FILE='$(FILE)' bash scripts/plane-backup.sh
endif

## restore: load a backup into the running plane's database, REPLACING
## its contents (the dump drops and recreates every table). Proven on a
## fresh cluster in CI. Guarded: this rewrites the ledger.
##   make restore FILE=backups/kaimahi-....sql
ifeq ($(TARGET),kind)
restore: $(KMX)
	@$(KMX_ENV) $(KMX) restore $(FILE)
else
restore: guard
	@test -n "$(FILE)" || { echo 'restore: FILE=<backup.sql> is required' >&2; exit 1; }
	@KUBECTL="$(KUBECTL)" FILE='$(FILE)' bash scripts/plane-restore.sh
endif

## plane-metrics: print one replica's Prometheus text (port-forward to a
## pod's ops port; the port is on no Service). POD=<name> picks a
## replica; default is the first.
ifeq ($(TARGET),kind)
plane-metrics: $(KMX)
	@$(KMX_ENV) $(KMX) metrics $(if $(POD),--pod $(POD))
else
plane-metrics:
	@KUBECTL="$(KUBECTL)" POD='$(POD)' bash scripts/plane-metrics.sh
endif

## ---- the enforcing MCP gateway (docs/tool-governance.md) ----

## govern-tools: put the tools agent behind the MCP gateway — issue its
## kmh_ credential (agent-side Secret kaimahi-tools-token), set the
## default allowlist, apply the Kaimahi RemoteMCPServer, repoint
## hello-tools at it. `make chat AGENT=hello-tools` then rides the
## gateway: authenticated, allowlisted, audited.
ifeq ($(TARGET),kind)
govern-tools: $(KMX)
	@$(KMX_ENV) $(KMX) tools govern --credential $(CRED_TOOLS) --tools "$(TOOLS)"
else
govern-tools: guard
	@KUBECTL="$(KUBECTL)" GOVERNED_SECRET=kaimahi-tools-token \
		bash scripts/plane-admin.sh issue $(CRED_TOOLS)
	@KUBECTL="$(KUBECTL)" bash scripts/plane-admin.sh tool-allow $(CRED_TOOLS) "$(TOOLS)"
	$(KUBECTL) apply -f k8s/kaimahi-tools.yaml
	$(KUBECTL) -n kagent wait \
		--for=jsonpath='{.status.conditions[?(@.type=="Accepted")].status}'=True \
		remotemcpserver/kaimahi-tools --timeout=300s
	$(KUBECTL) -n kagent patch agent hello-tools --type merge \
		-p '{"spec":{"declarative":{"tools":[{"type":"McpServer","mcpServer":{"apiGroup":"kagent.dev","kind":"RemoteMCPServer","name":"kaimahi-tools","toolNames":[$(TOOLNAMES_JSON)]}}]}}}'
	@$(call wait_switched,hello-tools)
	$(KUBECTL) -n kagent wait \
		--for=jsonpath='{.status.conditions[?(@.type=="Ready")].status}'=True \
		agent/hello-tools --timeout=300s
endif

## ungovern-tools: restore the ungoverned wiring (direct to the chart-managed
## tool server) by re-applying the committed Agent YAML
ifeq ($(TARGET),kind)
ungovern-tools: $(KMX)
	@$(KMX_ENV) $(KMX) tools ungovern
else
ungovern-tools: guard
	$(KUBECTL) apply -f k8s/tools-agent.yaml
	@$(call wait_switched,hello-tools)
endif

## tool-allow: replace the tools credential's allowlist, e.g.
##   make tool-allow TOOLS=k8s_get_resources,k8s_get_events
##   make tool-allow TOOLS=-        (empty: nothing callable)
ifeq ($(TARGET),kind)
tool-allow: $(KMX)
	@$(KMX_ENV) $(KMX) tools allow "$(TOOLS)" --credential $(CRED_TOOLS)
else
tool-allow: guard
	@KUBECTL="$(KUBECTL)" bash scripts/plane-admin.sh tool-allow $(CRED_TOOLS) "$(TOOLS)"
endif

## tool-allowlist: show the tools credential's allowlist
ifeq ($(TARGET),kind)
tool-allowlist: $(KMX)
	@$(KMX_ENV) $(KMX) tools allowlist $(CRED_TOOLS)
else
tool-allowlist:
	@KUBECTL="$(KUBECTL)" bash scripts/plane-admin.sh tool-allowlist $(CRED_TOOLS)
endif

## tool-audit: show the tool-call audit trail (newest first)
ifeq ($(TARGET),kind)
tool-audit: $(KMX)
	@$(KMX_ENV) $(KMX) audit tool $(CRED_TOOLS)
else
tool-audit:
	@KUBECTL="$(KUBECTL)" bash scripts/plane-admin.sh tool-audit $(CRED_TOOLS)
endif

## ---- approvals and time-boxed permits (docs/approvals.md) ----

## approvals: list pending approval requests (denied actions file them
## automatically; `make request` files one explicitly)
ifeq ($(TARGET),kind)
approvals: $(KMX)
	@$(KMX_ENV) $(KMX) approvals
else
approvals:
	@KUBECTL="$(KUBECTL)" bash scripts/plane-admin.sh approvals
endif

## approve: approve a pending request with BOUNDS (at least one of TTL/
## USES required; AMOUNT tokens-or-cents only for budget requests), e.g.
##   make approve ID=<uuid> TTL=60s USES=1
##   make approve ID=<uuid> TTL=5m AMOUNT=100000
ifeq ($(TARGET),kind)
approve: $(KMX)
	@$(KMX_ENV) $(KMX) approve "$(ID)" --ttl "$(if $(TTL),$(TTL),-)" --uses "$(if $(USES),$(USES),-)" --amount "$(if $(AMOUNT),$(AMOUNT),-)"
else
approve: guard
	@test -n "$(ID)" || { echo 'usage: make approve ID=<uuid> [TTL=60s] [USES=1] [AMOUNT=n]' >&2; exit 1; }
	@KUBECTL="$(KUBECTL)" bash scripts/plane-admin.sh approve "$(ID)" \
		"$(if $(TTL),$(TTL),-)" "$(if $(USES),$(USES),-)" "$(if $(AMOUNT),$(AMOUNT),-)"
endif

## deny: deny a pending request
ifeq ($(TARGET),kind)
deny: $(KMX)
	@$(KMX_ENV) $(KMX) deny "$(ID)"
else
deny: guard
	@test -n "$(ID)" || { echo 'usage: make deny ID=<uuid>' >&2; exit 1; }
	@KUBECTL="$(KUBECTL)" bash scripts/plane-admin.sh deny "$(ID)"
endif

## request: file an approval request explicitly, e.g.
##   make request KIND=tool SUBJECT=k8s_get_events
##   make request KIND=tool SUBJECT=k8s_get_events ARGS='{"namespace": "default"}'
##   make request KIND=budget SUBJECT=tokens CRED=hello-world
## ARGS (tool requests only) names the CALL to pre-approve; omitted
## means the argument-less call, never "any call".
ifeq ($(TARGET),kind)
request: $(KMX)
	@$(KMX_ENV) $(KMX) request $(KIND) $(SUBJECT) --credential "$(REQ_CRED)" $(if $(ARGS),--args '$(ARGS)')
else
request: guard
	@test -n "$(KIND)" && test -n "$(SUBJECT)" || \
		{ echo 'usage: make request KIND=tool|budget SUBJECT=<tool|tokens|cents> [CRED=...] [ARGS=<json>]' >&2; exit 1; }
	@KUBECTL="$(KUBECTL)" bash scripts/plane-admin.sh request "$(REQ_CRED)" "$(KIND)" "$(SUBJECT)" '$(ARGS)'
endif

# The filing credential: an explicit CRED= wins; otherwise tool requests
# default to the tools credential and budget requests to the chat one.
REQ_CRED = $(if $(filter command line,$(origin CRED)),$(CRED),$(if $(filter tool,$(KIND)),$(CRED_TOOLS),$(CRED)))

## grants: list grants with liveness (an expired grant is not a grant)
ifeq ($(TARGET),kind)
grants: $(KMX)
	@$(KMX_ENV) $(KMX) grants
else
grants:
	@KUBECTL="$(KUBECTL)" bash scripts/plane-admin.sh grants
endif

## approval-audit: the approvals' own audit trail (filed/approved/denied)
ifeq ($(TARGET),kind)
approval-audit: $(KMX)
	@$(KMX_ENV) $(KMX) audit approval
else
approval-audit:
	@KUBECTL="$(KUBECTL)" bash scripts/plane-admin.sh approval-audit
endif

## plane-copilot-secret: mint the Copilot token into the PROXY's
## namespace (real-key custody: the agent-side governed preset never
## holds it). Re-run to rotate; the proxy picks it up without a restart.
plane-copilot-secret: guard
	@KUBECTL="$(KUBECTL)" COPILOT_SECRET_NAMESPACE=kaimahi \
		COPILOT_SECRET_NAME=kaimahi-copilot-token \
		bash scripts/copilot-secret.sh
	@# Enabling Copilot is the moment the proxy needs the internet.
	@# The plane's own boundary (k8s/plane/network-policy.yaml) lets it
	@# reach nothing outside the cluster; this opens TCP 443 out, and
	@# only for the proxy. `make egress-copilot-off` closes it again.
	@# (The script above created the namespace if it did not exist, so
	@# this also works in the AKS ordering, where the token is minted
	@# before the plane is deployed.)
	$(KUBECTL) apply -f k8s/egress-copilot.yaml

status: $(KMX)
	@$(KMX_ENV) $(KMX) status $(if $(filter table,$(STATUS_OUTPUT)),,-o "$$KMX_STATUS_OUTPUT")

ifeq ($(TARGET),kind)
## down: delete the local kind cluster
# kmx carries the KIND_CLUSTER/KUBE_CTX consistency check this recipe had:
# the guard vouches for KUBE_CTX and the delete names KIND_CLUSTER, both are
# overridable, and a banner naming one cluster while another is destroyed is
# exactly the accident the guard exists to prevent.
down: $(KMX)
	@$(KMX_ENV) $(KMX) down
else
## down (TARGET=aks): delete the whole ephemeral resource group
down: aks-down
endif

## ---- the managed-cluster path (docs/aks.md) ----
#
# Azure identifiers are supplied by the operator and never committed:
#   AKS_RESOURCE_GROUP  required   the group these scripts create/delete
#   ACR_NAME            required   globally-unique private registry name
#   AKS_CLUSTER         optional   cluster + kube-context (default kaimahi)
#   AKS_LOCATION        optional   default westus3
#   AKS_NODE_SIZE       optional   default Standard_B4ms
#   AKS_NODE_COUNT      optional   default 1
#   AKS_NETWORK_POLICY  optional   cilium (default) | azure | calico — the
#                                  policy engine; scripts/aks-up.sh refuses
#                                  a cluster without one (the plane's
#                                  egress policies would be inert). Set it
#                                  on the make command line or export it
#                                  in the shell;
#                                  it is deliberately NOT in the recipe's
#                                  explicit list below, because that would
#                                  turn "unset" into an explicit empty
#                                  string, which the script refuses.
# See docs/aks.md for why those defaults, and what a run costs.

## aks-cluster: create the resource group, the PRIVATE ACR, and the AKS
## cluster (with AcrPull for its kubelet identity), then write kubeconfig
aks-cluster:
	@AKS_RESOURCE_GROUP='$(AKS_RESOURCE_GROUP)' ACR_NAME='$(ACR_NAME)' \
		AKS_CLUSTER='$(AKS_CLUSTER)' AKS_LOCATION='$(AKS_LOCATION)' \
		AKS_NODE_SIZE='$(AKS_NODE_SIZE)' AKS_NODE_COUNT='$(AKS_NODE_COUNT)' \
		bash scripts/aks-up.sh

## aks-creds: refresh the kubeconfig entry for an existing AKS cluster
aks-creds:
	@test -n "$(AKS_RESOURCE_GROUP)" || \
		{ echo 'usage: make aks-creds AKS_RESOURCE_GROUP=<rg> [AKS_CLUSTER=<name>]' >&2; exit 1; }
	az aks get-credentials --name $(AKS_CLUSTER) \
		--resource-group $(AKS_RESOURCE_GROUP) --overwrite-existing

## aks-down: DELETE the ephemeral resource group (cluster + registry + all).
## Refuses any group not tagged by scripts/aks-up.sh, and requires an
## explicit confirmation naming the group. This is not best-effort: a
## managed verification cluster is meant to be gone when the work ends.
aks-down:
	@AKS_RESOURCE_GROUP='$(AKS_RESOURCE_GROUP)' AKS_CLUSTER='$(AKS_CLUSTER)' \
		bash scripts/aks-down.sh

## kmx: build the CLI this Makefile's kind path delegates to
# Not .PHONY: a rebuild costs a second, but doing it on every `make chat`
# would put a Go toolchain in the middle of the most-used command.
$(KMX): $(KMX_SOURCES) $(KMX_ASSETS)
	@command -v go >/dev/null 2>&1 || { \
		echo 'kmx needs a Go toolchain to build from a checkout (https://go.dev/dl/).' >&2; \
		echo 'Without a clone: go install github.com/kaimahi-agents/kaimahi/cmd/kmx@<sha>' >&2; \
		exit 1; }
	@mkdir -p $(dir $@)
	go build -o $(KMX) ./cmd/kmx

# Pinned kagent CLI, checksum-verified. The release .sha256 files embed a
# build path, so compare digests directly.
#
# Still here, and still make's, because `slack-post` and `github-ask` invoke
# the CLI through $(call kagent_forward,...). kmx carries its own copy of the
# same pinned fetch for the install-without-a-clone case, and in an ordinary
# checkout the two are SEPARATE downloads: KMX_ENV forwards KAGENT to kmx
# only when the variable's origin is the command line or the environment, and
# the `?=` below makes its origin `file`. So a kmx-delegating target fetches
# kmx's own copy, and only `make <target> KAGENT=bin/kagent` puts both on the
# one binary. `command make -n chat` shows the KMX_ENV line with no KAGENT= in
# it.
$(KAGENT):
	mkdir -p bin
	curl -sSfLo $(KAGENT) https://github.com/kagent-dev/kagent/releases/download/v$(KAGENT_VERSION)/kagent-$(OS)-$(ARCH)
	curl -sSfLo $(KAGENT).sha256 https://github.com/kagent-dev/kagent/releases/download/v$(KAGENT_VERSION)/kagent-$(OS)-$(ARCH).sha256
	@sum=$$(if [ "$(OS)" = darwin ]; then shasum -a 256 $(KAGENT); else sha256sum $(KAGENT); fi | cut -d' ' -f1); \
	test "$$sum" = "$$(cut -d' ' -f1 $(KAGENT).sha256)" || \
		{ echo 'kagent CLI checksum mismatch' >&2; rm -f $(KAGENT); exit 1; }
	chmod +x $(KAGENT)

## ---- the governed Slack path (docs/slack.md) ----

## slack-secret: capture the Slack BOT token stdin-only and store the
## plane-side Secrets. REFUSES unless Slack confirms the channel is
## private and the bot is a member — never a shared channel.
##   make slack-secret SLACK_CHANNEL=C0XXXXXXXXX
slack-secret: guard
	@test -n "$(SLACK_CHANNEL)" || \
		{ echo 'usage: make slack-secret SLACK_CHANNEL=C0XXXXXXXXX (a PRIVATE test channel)' >&2; exit 1; }
	@KUBECTL="$(KUBECTL)" SLACK_CHANNEL="$(SLACK_CHANNEL)" bash scripts/slack-secret.sh

## slack-mcp: deploy the third-party Slack MCP server in-cluster, in the
## PLANE's namespace, via kagent's MCPServer CRD (digest-pinned). This is
## the first pod here with deliberate internet egress — see the runbook.
slack-mcp: guard
	@$(KUBECTL) -n kaimahi get secret kaimahi-slack-bot >/dev/null 2>&1 || \
		{ echo 'kaimahi-slack-bot missing — run: make slack-secret SLACK_CHANNEL=C0XXXXXXXXX' >&2; exit 1; }
	@# Without the gateway's upstream credential the server still starts,
	@# but every relayed call fails closed at 503 — and a tool-grant use is
	@# consumed BEFORE the forward, so a human approval would be spent on a
	@# message that was never sent. Check it here, not after the fact.
	@$(KUBECTL) -n kaimahi get secret kaimahi-slack-mcp-key >/dev/null 2>&1 || \
		{ echo 'kaimahi-slack-mcp-key missing — re-run: make slack-secret SLACK_CHANNEL=C0XXXXXXXXX' >&2; exit 1; }
	$(KUBECTL) apply -f k8s/slack-mcp.yaml
	$(KUBECTL) -n kaimahi wait \
		--for=jsonpath='{.status.conditions[?(@.type=="Ready")].status}'=True \
		mcpserver/kaimahi-slack-mcp --timeout=300s

## govern-slack: put the Slack demo agent behind the MCP gateway — issue
## its kmh_ credential (agent-side Secret kaimahi-slack-token), set the
## READ-ONLY allowlist, apply the Kaimahi RemoteMCPServer and the agent.
## Posting is deliberately absent from the allowlist.
govern-slack: guard
	@KUBECTL="$(KUBECTL)" GOVERNED_SECRET=kaimahi-slack-token \
		bash scripts/plane-admin.sh issue $(CRED_SLACK)
	@KUBECTL="$(KUBECTL)" bash scripts/plane-admin.sh tool-allow $(CRED_SLACK) "$(SLACK_TOOLS)"
	$(KUBECTL) apply -f k8s/kaimahi-slack.yaml
	$(KUBECTL) -n kagent wait \
		--for=jsonpath='{.status.conditions[?(@.type=="Accepted")].status}'=True \
		remotemcpserver/kaimahi-slack --timeout=300s
	$(KUBECTL) apply -f k8s/slack-agent.yaml
	$(KUBECTL) -n kagent patch agent hello-slack --type merge \
		-p '{"spec":{"declarative":{"tools":[{"type":"McpServer","mcpServer":{"apiGroup":"kagent.dev","kind":"RemoteMCPServer","name":"kaimahi-slack","toolNames":[$(SLACK_TOOLNAMES_JSON)]}}]}}}'
	$(KUBECTL) -n kagent wait \
		--for=jsonpath='{.status.conditions[?(@.type=="Ready")].status}'=True \
		agent/hello-slack --timeout=300s

## slack-allow: replace the Slack credential's allowlist, e.g.
##   make slack-allow SLACK_TOOLS=conversations_history
##   make slack-allow SLACK_TOOLS=-        (empty: nothing callable)
## Widening this is a CONFIG change; the demo widens by APPROVAL instead.
slack-allow: guard
	@KUBECTL="$(KUBECTL)" bash scripts/plane-admin.sh tool-allow $(CRED_SLACK) "$(SLACK_TOOLS)"

## slack-audit: the Slack credential's tool-call audit trail
slack-audit:
	@KUBECTL="$(KUBECTL)" bash scripts/plane-admin.sh tool-audit $(CRED_SLACK)

## slack-post: ask the demo agent to post to the channel. Denied until a
## human approves it; that denial is the point.
##   make slack-post SLACK_CHANNEL=C0XXXXXXXXX [MESSAGE='...']
MESSAGE ?= Kaimahi governance demo: this message required a human approval.
# The task text reaches the recipe through the ENVIRONMENT, not through a
# re-quoted make/shell string: a MESSAGE containing an apostrophe would
# otherwise break out of the single quotes and mangle the task (or the
# recipe). The channel gets the same anchored shape check
# scripts/slack-secret.sh applies, so nothing odd reaches the agent.
slack-post: export KAIMAHI_SLACK_TASK = Post this to Slack channel $(SLACK_CHANNEL): $(MESSAGE)
slack-post: $(KAGENT)
	@test -n "$(SLACK_CHANNEL)" || \
		{ echo 'usage: make slack-post SLACK_CHANNEL=C0XXXXXXXXX [MESSAGE=...]' >&2; exit 1; }
	@case "$(SLACK_CHANNEL)" in \
		[CG][A-Z0-9][A-Z0-9][A-Z0-9][A-Z0-9][A-Z0-9][A-Z0-9][A-Z0-9]*) ;; \
		*) echo 'invalid SLACK_CHANNEL (want a channel ID like C0XXXXXXXXX, not a #name)' >&2; exit 1 ;; \
	esac
	@case "$(SLACK_CHANNEL)" in \
		*[!A-Z0-9]*) echo 'invalid SLACK_CHANNEL (want a channel ID like C0XXXXXXXXX, not a #name)' >&2; exit 1 ;; \
	esac
	@$(call kagent_forward,hello-slack,$(KAGENT_INVOKE) --agent hello-slack --task "$$KAIMAHI_SLACK_TASK",$(CHAT_RETRYABLE_SAFE))

## slack-down: remove the Slack demo (agent, gateway seam, MCP server).
## The Secrets are left alone — delete them explicitly to revoke.
slack-down: guard
	-$(KUBECTL) -n kagent delete agent hello-slack
	-$(KUBECTL) -n kagent delete remotemcpserver kaimahi-slack
	-$(KUBECTL) -n kaimahi delete mcpserver kaimahi-slack-mcp

## ---- the network boundary (docs/egress.md) ----
#
# The policies themselves need no target: k8s/plane/network-policy.yaml
# ships with `make plane` on every environment. What needs a target is
# PROOF — a NetworkPolicy the CNI ignores is indistinguishable from one
# it enforces until something is shown to be blocked.

## netpol-verify: prove the boundary is ENFORCED, not merely present —
## policed pods demonstrably cannot reach ollama / the internet, against
## a control pod that can, plus an exec into the real Postgres pod.
## Creates and deletes a few BestEffort probe pods (~2 minutes). Runs on
## every PR in CI. The script guards its own context (like the tool
## probes), so no `guard` here — one banner, not two.
netpol-verify:
	@KUBECTL="$(KUBECTL)" COPILOT_EGRESS=$(COPILOT_EGRESS) bash scripts/netpol-probe.sh

## egress-copilot: let the proxy (and only the proxy) reach TCP 443 on
## public addresses — the Copilot upstream. `make plane-copilot-secret`
## applies this for you; it is here for the case where the token was
## minted before the plane existed and the policy needs re-applying.
egress-copilot: guard
	$(KUBECTL) apply -f k8s/egress-copilot.yaml

## egress-copilot-off: close the proxy's internet allowance again. The
## Copilot Secret is left alone; governed Copilot calls then fail closed
## (the proxy cannot dial out), which is the point.
egress-copilot-off: guard
	@# Delete by manifest, not by a name typed here: a renamed policy
	@# would otherwise delete nothing, exit 0, and leave the hole open.
	$(KUBECTL) delete -f k8s/egress-copilot.yaml --ignore-not-found

## ---- hosted upstreams (docs/hosted-upstreams.md) ----
#
# The gateway's first upstream OUTSIDE the cluster: GitHub's hosted MCP
# server, reached through the plane's one hardened dialer. The table
# entry is committed (k8s/plane/upstreams.yaml); what these targets add
# is the credential in plane custody, the opt-in network allowance, and
# the governed agent.

## github-secret: capture a fine-grained, read-only, one-repository
## GitHub token AT A PROMPT, vet it against that repository, store it as
## the plane-side Secret, and open the gateway's 443-to-public allowance.
## The value is typed with the echo off and a piped stdin is refused —
## there is no flag, variable or file that will carry it.
##   make github-secret GITHUB_REPO=owner/name
github-secret: $(KMX)
	@$(KMX_ENV) $(KMX) credential capture github $(GITHUB_REPO)

## github-revoke: the inverse — delete the token Secret and close the
## allowance. Governed GitHub calls then fail closed (503: no credential;
## and 502: no route out), which is the point.
github-revoke: guard
	$(KUBECTL) -n kaimahi delete secret kaimahi-github-pat --ignore-not-found
	$(KUBECTL) delete -f k8s/egress-hosted.yaml --ignore-not-found

## egress-hosted / egress-hosted-off: the allowance on its own (CI's
## synthetic-upstream steps use these; `make github-secret` applies it
## for you). Delete by manifest, not by a typed name, so a renamed policy
## cannot leave the hole open with exit 0.
egress-hosted: guard
	$(KUBECTL) apply -f k8s/egress-hosted.yaml

egress-hosted-off: guard
	$(KUBECTL) delete -f k8s/egress-hosted.yaml --ignore-not-found

## govern-github: put the GitHub demo agent behind the MCP gateway —
## issue its kmh_ credential (agent-side Secret kaimahi-github-token),
## set the READ-ONLY allowlist, apply the Kaimahi RemoteMCPServer and the
## agent. The write tool is deliberately absent from the allowlist.
govern-github: guard
	@KUBECTL="$(KUBECTL)" GOVERNED_SECRET=kaimahi-github-token \
		bash scripts/plane-admin.sh issue $(CRED_GITHUB)
	@KUBECTL="$(KUBECTL)" bash scripts/plane-admin.sh tool-allow $(CRED_GITHUB) "$(GITHUB_TOOLS)"
	$(KUBECTL) apply -f k8s/kaimahi-github.yaml
	$(KUBECTL) -n kagent wait \
		--for=jsonpath='{.status.conditions[?(@.type=="Accepted")].status}'=True \
		remotemcpserver/kaimahi-github --timeout=300s
	$(KUBECTL) apply -f k8s/github-agent.yaml
	$(KUBECTL) -n kagent patch agent hello-github --type merge \
		-p '{"spec":{"declarative":{"tools":[{"type":"McpServer","mcpServer":{"apiGroup":"kagent.dev","kind":"RemoteMCPServer","name":"kaimahi-github","toolNames":[$(GITHUB_TOOLNAMES_JSON)]}}]}}}'
	$(KUBECTL) -n kagent wait \
		--for=jsonpath='{.status.conditions[?(@.type=="Ready")].status}'=True \
		agent/hello-github --timeout=300s

## github-allow: replace the GitHub credential's allowlist, e.g.
##   make github-allow GITHUB_TOOLS=list_issues
##   make github-allow GITHUB_TOOLS=-        (empty: nothing callable)
## Widening this is a CONFIG change; the demo widens by APPROVAL instead.
github-allow: guard
	@KUBECTL="$(KUBECTL)" bash scripts/plane-admin.sh tool-allow $(CRED_GITHUB) "$(GITHUB_TOOLS)"

## github-audit: the GitHub credential's tool-call audit trail
github-audit:
	@KUBECTL="$(KUBECTL)" bash scripts/plane-admin.sh tool-audit $(CRED_GITHUB)

## github-ask: ask the demo agent what is open on a repository.
##   make github-ask GITHUB_REPO=owner/name
# The task reaches the recipe through the ENVIRONMENT (like slack-post),
# and the repository gets the same anchored shape check the secret
# script applies, so nothing odd reaches the agent.
github-ask: export KAIMAHI_GITHUB_TASK = What is open on the GitHub repository $(GITHUB_REPO)? List the open issues and pull requests.
github-ask: $(KAGENT)
	@test -n "$(GITHUB_REPO)" || \
		{ echo 'usage: make github-ask GITHUB_REPO=owner/name' >&2; exit 1; }
	@printf '%s' "$(GITHUB_REPO)" | grep -qE '^[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9._-]{1,100}$$' || \
		{ echo 'invalid GITHUB_REPO (want owner/name)' >&2; exit 1; }
	@$(call kagent_forward,hello-github,$(KAGENT_INVOKE) --agent hello-github --task "$$KAIMAHI_GITHUB_TASK",$(CHAT_RETRYABLE_SAFE))

## github-down: remove the GitHub demo (agent, gateway seam). The token is
## a separate decision: make github-revoke.
github-down: guard
	-$(KUBECTL) -n kagent delete agent hello-github
	-$(KUBECTL) -n kagent delete remotemcpserver kaimahi-github

## ---- the release agent (docs/release-agent.md) ----
#
# Kaimahi's first real user. An agent reads what merged since the last
# release, DRAFTS the notes, and proposes each consequential call; a human
# approves the exact call; the workflow and the pipelines it dispatches
# build and publish. The agent never carries a byte and never decides to
# ship.

## release-secret: capture the release agent's GitHub token — FINE-GRAINED,
## one repository, Contents+Actions write and Pull requests read. Applies
## the hosted allowance. Typed at a prompt; a pipe is refused.
##   make release-secret GITHUB_REPO=owner/name
release-secret: $(KMX)
	@$(KMX_ENV) $(KMX) credential capture github-release $(GITHUB_REPO)

## ado-secret: capture an Azure DevOps ACCESS TOKEN (Entra, not a PAT —
## the hosted ADO MCP server accepts nothing else) and store it in plane
## custody. It lives about an hour; re-run it before a release session
## with --replace. Mint it with `az account get-access-token --scope
## https://mcp.dev.azure.com/.default --query accessToken -o tsv`, then
## paste it at the prompt: a pipe is refused, deliberately.
##   make ado-secret ADO_ORG=<organization>
ado-secret: $(KMX)
	@$(KMX_ENV) $(KMX) credential capture ado $(ADO_ORG)

## release-revoke: delete BOTH release tokens and close the hosted
## allowance. Run it at the end of any session that was only a test.
release-revoke: guard
	$(KUBECTL) -n kaimahi delete secret kaimahi-release-pat --ignore-not-found
	$(KUBECTL) -n kaimahi delete secret kaimahi-ado-token --ignore-not-found
	$(KUBECTL) delete -f k8s/egress-hosted.yaml --ignore-not-found
	@echo 'Revoke the GitHub token at github.com/settings/personal-access-tokens too:' >&2
	@echo 'deleting the Secret stops Kaimahi using it, not GitHub honouring it.' >&2

## govern-release: put the release agent behind the MCP gateway — issue
## its kmh_ credential (agent-side Secret kaimahi-release-token), set the
## READ-ONLY allowlist, apply both seams and the agent.
##
## Unlike govern-github there is no toolNames patch: this agent's tool
## SELECTION is fixed in k8s/release-agent.yaml across two servers, and a
## merge patch would replace the whole array with one of them.
govern-release: guard
	@KUBECTL="$(KUBECTL)" GOVERNED_SECRET=kaimahi-release-token \
		bash scripts/plane-admin.sh issue $(CRED_RELEASE)
	@KUBECTL="$(KUBECTL)" bash scripts/plane-admin.sh tool-allow $(CRED_RELEASE) "$(RELEASE_TOOLS)"
	$(KUBECTL) apply -f k8s/kaimahi-release-github.yaml -f k8s/kaimahi-release-ado.yaml
	$(KUBECTL) -n kagent wait --for=condition=Accepted \
		remotemcpserver/kaimahi-release-github --timeout=300s
	$(KUBECTL) -n kagent wait --for=condition=Accepted \
		remotemcpserver/kaimahi-release-ado --timeout=300s
	$(KUBECTL) apply -f k8s/release-agent.yaml
	$(KUBECTL) -n kagent wait --for=condition=Ready agent/release-agent --timeout=300s

## release-allow: replace the release credential's allowlist, e.g.
##   make release-allow RELEASE_TOOLS=list_tags
##   make release-allow RELEASE_TOOLS=-      (empty: nothing callable)
release-allow: guard
	@KUBECTL="$(KUBECTL)" bash scripts/plane-admin.sh tool-allow $(CRED_RELEASE) "$(RELEASE_TOOLS)"

## release-bind: constrain the release credential's READ tools to ONE
## repository, at the plane. Written as an overlay fragment, so
## `make plane` keeps it.
##   make release-bind GITHUB_REPO=owner/name
##   make release-bind GITHUB_REPO=-          (remove the binding)
release-bind: guard
	@test -n "$(GITHUB_REPO)" || \
		{ echo 'usage: make release-bind GITHUB_REPO=owner/name' >&2; exit 1; }
	@KUBECTL="$(KUBECTL)" CRED_RELEASE=$(CRED_RELEASE) GITHUB_REPO="$(GITHUB_REPO)" \
		ADO_ORG='$(ADO_ORG)' ADO_PROJECT='$(ADO_PROJECT)' ADO_PIPELINES='$(ADO_PIPELINES)' \
		bash scripts/release-bind.sh

## release: cut a release. ONE command — the agent drafts and proposes,
## this waits for the approvals and polls the builds itself.
##   make release GITHUB_REPO=owner/name VERSION=v1.2.3 \
##        [BASE=main] [RELEASE_BRANCH=...] [GH_WORKFLOW=a.yml,b.yml] \
##        [ADO_PROJECT=... ADO_PIPELINES=12,13] [SLACK_USER=U0EXAMPLE] \
##        [DRY_RUN=1] [STEP=propose|cut|build|watch]
##
## DRY_RUN=1 reads and drafts the notes and stops before the first
## consequential call — the right first command against a real repository.
release: guard
	@KUBECTL="$(KUBECTL)" CRED_RELEASE=$(CRED_RELEASE) \
		GITHUB_REPO='$(GITHUB_REPO)' VERSION='$(VERSION)' BASE='$(BASE)' \
		RELEASE_BRANCH='$(RELEASE_BRANCH)' GH_WORKFLOW='$(GH_WORKFLOW)' \
		ADO_ORG='$(ADO_ORG)' ADO_PROJECT='$(ADO_PROJECT)' ADO_PIPELINES='$(ADO_PIPELINES)' \
		ADO_BUILDS='$(ADO_BUILDS)' PRERELEASE='$(PRERELEASE)' \
		ADO_ARTIFACTS='$(ADO_ARTIFACTS)' ASSET_GLOBS='$(ASSET_GLOBS)' \
		SLACK_USER='$(SLACK_USER)' DRY_RUN='$(DRY_RUN)' STEP='$(STEP)' \
		RELEASE_CHAT='make chat AGENT=release-agent TARGET=$(TARGET) KIND_CLUSTER=$(KIND_CLUSTER)' \
		bash scripts/release-run.sh

## release-refresh: re-mint the Azure DevOps token into plane custody and
## reconnect the seam. `make release` does this itself; this target is for
## everything else — a bare `make chat` against the release agent does NOT
## refresh, and an expired token reaches you as "the agent has no such
## tool" rather than as an expired token.
##   make release-refresh ADO_ORG=<organization>
release-refresh: guard
	@test -n "$(ADO_ORG)" || \
		{ echo 'usage: make release-refresh ADO_ORG=<organization>' >&2; exit 1; }
	@KUBECTL="$(KUBECTL)" CRED_RELEASE=$(CRED_RELEASE) ADO_ORG='$(ADO_ORG)' \
		STEP=refresh GITHUB_REPO=placeholder/placeholder VERSION=v0 \
		bash scripts/release-run.sh

## release-audit: the release credential's tool-call audit trail
release-audit:
	@KUBECTL="$(KUBECTL)" bash scripts/plane-admin.sh tool-audit $(CRED_RELEASE)

## release-down: remove the release agent and both seams. The tokens are a
## separate decision: make release-revoke.
release-down: guard
	-$(KUBECTL) -n kagent delete agent release-agent
	-$(KUBECTL) -n kagent delete remotemcpserver kaimahi-release-github
	-$(KUBECTL) -n kagent delete remotemcpserver kaimahi-release-ado

## ---- the accounts-payable exception demo (docs/ap-demo.md) ----
#
# The demo Kaimahi exists to make: an agent investigates an invoice that
# ordinary three-way matching cannot resolve, reaches a defensible answer,
# and then has to ask a human before any money moves — and when a later
# invoice tries to manipulate it, being manipulated is not enough to move
# money. The ERP is fixtures; the governance is the real thing.

ifeq ($(TARGET),kind)
## erp: build the demo's fixture ERP, side-load it into kind, project the
## corpus (k8s/erp-fixtures.json) as a ConfigMap and roll it out
erp: guard
	@KUBECTL="$(KUBECTL)" CONTAINER_ENGINE=$(CONTAINER_ENGINE) \
		KIND_CLUSTER='$(KIND_CLUSTER)' bash scripts/erp-deploy.sh all

## erp-fixtures: re-project k8s/erp-fixtures.json and restart the ERP.
## Editing the story needs no rebuild — this is that path.
erp-fixtures: guard
	@KUBECTL="$(KUBECTL)" bash scripts/erp-deploy.sh fixtures
else
## erp-image (TARGET=aks): build the fixture ERP IN Azure with ACR Tasks.
## No local docker build, no `docker push`, no registry login on this
## machine — the source is uploaded and built BY the private registry,
## exactly as `make plane-image` does for the proxy. Nothing is published:
## the image never leaves that private ACR.
erp-image:
	@test -n "$(ACR_NAME)" || \
		{ echo 'ACR_NAME is required for TARGET=aks (see docs/aks.md)' >&2; exit 1; }
	az acr build --registry $(ACR_NAME) \
		--image $(ERP_IMAGE_REPO):$(ERP_IMAGE_TAG) \
		--file cmd/demo/kaimahi-erp/Dockerfile .

## erp (TARGET=aks): build the ERP in the registry, project the corpus
## (k8s/erp-fixtures.json) as a ConfigMap and roll it out PULLING that
## image. scripts/erp-deploy.sh renders k8s/erp-mcp.yaml's image reference
## and pull policy for a registry target; the committed manifest keeps
## `imagePullPolicy: Never`, which is correct for kind and never edited.
erp: guard erp-image
	@KUBECTL="$(KUBECTL)" ERP_TARGET=$(ERP_TARGET) \
		ERP_IMAGE='$(ERP_IMAGE)' ERP_PULL_POLICY=$(ERP_PULL_POLICY) \
		bash scripts/erp-deploy.sh fixtures

## erp-fixtures (TARGET=aks): re-project k8s/erp-fixtures.json and restart
## the ERP. Editing the story needs no rebuild — this is that path.
erp-fixtures: guard
	@KUBECTL="$(KUBECTL)" ERP_TARGET=$(ERP_TARGET) \
		ERP_IMAGE='$(ERP_IMAGE)' ERP_PULL_POLICY=$(ERP_PULL_POLICY) \
		bash scripts/erp-deploy.sh fixtures
endif

## govern-ap: issue the AP agent's credential with a READ-ONLY allowlist,
## wire it to the ERP through the gateway, and create the agent. What it
## may DO is not here: payment_schedule is bounded by the standing
## constraint in k8s/plane/upstreams.yaml, and dispute_open and
## vendor_notify need an approval each.
govern-ap: guard
	@KUBECTL="$(KUBECTL)" GOVERNED_SECRET=kaimahi-ap-token \
		bash scripts/plane-admin.sh issue $(CRED_AP)
	@KUBECTL="$(KUBECTL)" bash scripts/plane-admin.sh tool-allow $(CRED_AP) "$(AP_TOOLS)"
	$(KUBECTL) apply -f k8s/kaimahi-erp.yaml
	$(KUBECTL) -n kagent wait \
		--for=jsonpath='{.status.conditions[?(@.type=="Accepted")].status}'=True \
		remotemcpserver/kaimahi-erp --timeout=300s
	$(KUBECTL) apply -f k8s/ap-agent.yaml
	@# The modelConfig rides the SAME merge patch as the tool selection.
	@# k8s/ap-agent.yaml commits `governed-ollama` (the kind demo and CI
	@# hold no hosted credential and stay keyless), and that ModelConfig
	@# does not exist on a Copilot-only managed cluster — the agent would
	@# never reach Ready and the wait below would time out. GOVERNED_PRESET is
	@# `governed-ollama` on kind, so this patch is a no-op there and the
	@# committed file still names the preset kind uses.
	$(KUBECTL) -n kagent patch agent ap-agent --type merge \
		-p '{"spec":{"declarative":{"modelConfig":"$(GOVERNED_PRESET)","tools":[{"type":"McpServer","mcpServer":{"apiGroup":"kagent.dev","kind":"RemoteMCPServer","name":"kaimahi-erp","toolNames":[$(AP_TOOLNAMES_JSON)]}}]}}}'
	$(KUBECTL) -n kagent wait \
		--for=jsonpath='{.status.conditions[?(@.type=="Ready")].status}'=True \
		agent/ap-agent --timeout=300s

## ap-allow: replace the AP credential's allowlist, e.g.
##   make ap-allow AP_TOOLS=invoice_get
##   make ap-allow AP_TOOLS=-        (empty: nothing callable)
## Widening this is a CONFIG change; the demo widens by APPROVAL instead.
ap-allow: guard
	@KUBECTL="$(KUBECTL)" bash scripts/plane-admin.sh tool-allow $(CRED_AP) "$(AP_TOOLS)"

## ap-audit: the AP credential's tool-call audit trail
ap-audit:
	@KUBECTL="$(KUBECTL)" bash scripts/plane-admin.sh tool-audit $(CRED_AP)

## ap-ask: ask the AP agent to investigate an invoice.
##   make ap-ask AP_INVOICE=INV-88134
ap-ask: export KAIMAHI_AP_TASK = Investigate invoice $(AP_INVOICE) and resolve it.
# No $(KAGENT) prerequisite: this delegates to `chat`, which is kmx, which
# fetches its own pinned copy. Depending on the make-side binary downloaded
# it twice over and used neither.
ap-ask:
	@$(MAKE) chat AGENT=ap-agent TASK="$$KAIMAHI_AP_TASK"

## ap-demo: the exception scenario end to end — the routine invoice pays
## itself under the standing constraint, the exception is denied, filed,
## approved in Slack by a named human and only then paid, and the dispute
## and the vendor notice need an approval each of their own.
##   make ap-demo [SLACK_USER=U0EXAMPLE] [AP_HUMAN=1]
##
## AP_HUMAN=1 is the live-workspace setting: the scenario prints each
## approval line and WAITS for that person to type it in Slack, instead of
## synthesising a signed app_mention in their name. See
## scripts/await-approval.sh.
ap-demo: guard
	@KUBECTL="$(KUBECTL)" CRED_AP=$(CRED_AP) SLACK_USER='$(SLACK_USER)' \
		AP_HUMAN='$(AP_HUMAN)' \
		AP_CHAT='make chat AGENT=ap-agent TARGET=$(TARGET) KIND_CLUSTER=$(KIND_CLUSTER)' \
		bash scripts/ap-demo.sh

## ap-injection: the manipulated invoice — the agent may comply; the call
## is denied anyway, audited with the changed payee, and cannot ride the
## approval the earlier call earned.
ap-injection: guard
	@KUBECTL="$(KUBECTL)" CRED_AP=$(CRED_AP) SLACK_USER='$(SLACK_USER)' \
		AP_HUMAN='$(AP_HUMAN)' \
		AP_CHAT='make chat AGENT=ap-agent TARGET=$(TARGET) KIND_CLUSTER=$(KIND_CLUSTER)' \
		bash scripts/ap-injection.sh

## ap-down: remove the accounts-payable demo (agent, gateway seam, ERP)
ap-down: guard
	-$(KUBECTL) -n kagent delete agent ap-agent
	-$(KUBECTL) -n kagent delete remotemcpserver kaimahi-erp
	-$(KUBECTL) delete -f k8s/erp-mcp.yaml
	-$(KUBECTL) -n kaimahi delete configmap kaimahi-erp-fixtures

## ---- inbound connectors (docs/inbound.md) ----
#
# The plane's one ingress: an external event (a webhook) may trigger a
# kagent agent, on the plane's terms. The hooks live in the committed
# upstreams table (k8s/plane/upstreams.yaml); these targets issue the
# hook's identity, store its signing secret, deliver an event, and read
# the trail. Approving a hook rides the approval targets unchanged:
# `make approvals` / `make approve ID=... USES=... TTL=...`.
HOOK          ?= demo
CRED_INBOUND  ?= inbound-demo
# Where a BEARER hook's token goes (kaimahi namespace: the caller is
# outside the cluster, not an agent). `-` discards the token: the right
# choice for SIGNED hooks, whose credential is an identity, not a bearer.
INBOUND_SECRET ?= -
EVENT         ?= Reply with exactly the word PONG.

## inbound-credential: issue the hook's plane credential, e.g.
##   make inbound-credential                                  (signed demo hook)
##   make inbound-credential CRED_INBOUND=inbound-bearer INBOUND_SECRET=kaimahi-inbound-token
inbound-credential: guard
	@KUBECTL="$(KUBECTL)" SECRET_NAMESPACE=kaimahi GOVERNED_SECRET='$(INBOUND_SECRET)' \
		bash scripts/plane-admin.sh issue $(CRED_INBOUND)

## inbound-secret: store a hook's signing secret — paste the SOURCE's
## secret on stdin, or GENERATE=1 for a fresh one a Kaimahi-scheme caller
## is then told (see scripts/inbound-secret.sh for retrieval).
inbound-secret: guard
	@KUBECTL="$(KUBECTL)" HOOK=$(HOOK) bash scripts/inbound-secret.sh $(if $(GENERATE),--generate,)

## inbound-fire: deliver one event to a hook and report the plane's
## decision. Unguarded for the same reason `chat` is (it runs through
## the guarded probe script, which resolves and vets its own context).
##   make inbound-fire [HOOK=demo] [EVENT='...'] [AUTH=hmac|bearer|none|forged|stale]
##                     [EXPECT=202] [DELIVERY=<id to resend>]
inbound-fire:
	@KUBECTL="$(KUBECTL)" bash scripts/inbound-probe.sh $(HOOK) "$(EVENT)"

## inbound-audit: the inbound event trail (decisions and outcomes, newest
## first) — every hook, unless HOOK=<name> is given on the command line.
## (HOOK's default serves inbound-fire/inbound-secret; an audit that
## silently narrowed to it would hide the other hooks' events.)
inbound-audit:
	@KUBECTL="$(KUBECTL)" bash scripts/plane-admin.sh inbound-audit \
		$(if $(filter command line,$(origin HOOK)),$(HOOK),)

## ---- approvals from Slack (docs/approvals.md, "Deciding from Slack") ----
#
# A filed request is announced in the pinned channel by the plane, under
# the plane's OWN gateway credential; an approver decides it by
# mentioning the bot; the grant carries their Slack identity. Two
# Secrets and one credential, all plane-side (kaimahi namespace).
CRED_PLANE     ?= kaimahi-plane
SLACK_USER     ?=
COMMAND        ?=

## slack-approvers: store WHO may approve from Slack — paste Slack user
## ids (U…), comma- or newline-separated, on stdin. Workspace identifiers:
## stdin-only, into Secret kaimahi-slack-approvers, never argv or YAML.
slack-approvers: guard
	@KUBECTL="$(KUBECTL)" bash scripts/slack-approvers.sh

## notify-slack: issue the PLANE's own gateway credential (kmh_ token into
## the plane-side Secret kaimahi-notifier-token) and allowlist it to the
## posting tool only. Configuration, not a grant: the plane is the trust
## root. The proxy reads the file per post (first projection can lag ~1m).
notify-slack: guard
	@KUBECTL="$(KUBECTL)" SECRET_NAMESPACE=kaimahi GOVERNED_SECRET=kaimahi-notifier-token \
		bash scripts/plane-admin.sh issue $(CRED_PLANE)
	@KUBECTL="$(KUBECTL)" bash scripts/plane-admin.sh tool-allow $(CRED_PLANE) "$(SLACK_POST_TOOL)"

## slack-mention: deliver ONE synthetic, correctly signed app_mention to
## the slack-events hook as Slack would (kind: the keyless stand-in for
## typing in the channel; CI's tool). Unguarded like inbound-fire.
##   make slack-mention SLACK_USER=U0EXAMPLE COMMAND='approve <id> uses=1' [EXPECT=200] [WANT='approved request']
slack-mention:
	@test -n "$(SLACK_USER)" && test -n "$(COMMAND)" || \
		{ echo "usage: make slack-mention SLACK_USER=U… COMMAND='approve <id> [uses=N] [ttl=D]' [EXPECT=200] [WANT=...]" >&2; exit 1; }
	@KUBECTL="$(KUBECTL)" EXPECT="$(EXPECT)" WANT="$(WANT)" bash scripts/slack-mention-probe.sh "$(SLACK_USER)" "$(COMMAND)"

## ---- the public edge (docs/inbound.md, "Putting it on the internet") ----
#
# The ONLY internet-reachable thing in this repo: a TLS edge in front of
# the inbound bridge, on TARGET=aks only. kind has no public path and
# these targets refuse there rather than pretend. KAIMAHI_DNS_LABEL is an
# Azure identifier (it becomes <label>.<region>.cloudapp.azure.com) and is
# never committed; neither is the public IP the scan reports.
ifeq ($(TARGET),aks)
## inbound-expose: put the inbound bridge on the internet — Caddy edge,
## Let's Encrypt via TLS-ALPN-01, one port. Prints the Slack Request URL.
##   TARGET=aks make inbound-expose KAIMAHI_DNS_LABEL=<unique-label>
inbound-expose: guard
	@KUBECTL="$(KUBECTL)" KAIMAHI_DNS_LABEL='$(KAIMAHI_DNS_LABEL)' AKS_LOCATION='$(AKS_LOCATION)' \
		AKS_RESOURCE_GROUP='$(AKS_RESOURCE_GROUP)' AKS_CLUSTER='$(AKS_CLUSTER)' \
		bash scripts/inbound-expose.sh

## inbound-unexpose: take the edge down (Deployment, Service + public IP,
## the certificate's volume, and the policy allowance). REMOVE the Slack
## app's Request URL too — the name this frees can be claimed by anyone.
inbound-unexpose: guard
	$(KUBECTL) delete -f k8s/inbound-edge.yaml --ignore-not-found
	@echo 'edge removed. Now remove the Request URL / disable Event Subscriptions in the Slack app.' >&2

## exposure-scan: prove the internet-facing surface is exactly the edge
## on 443 — every public IP in the cluster's node resource group is
## connect-scanned on all 65535 TCP ports (IPs masked; REVEAL_IPS=1).
exposure-scan:
	@KUBECTL="$(KUBECTL)" AKS_RESOURCE_GROUP='$(AKS_RESOURCE_GROUP)' AKS_CLUSTER='$(AKS_CLUSTER)' \
		bash scripts/exposure-scan.sh
else
inbound-expose inbound-unexpose exposure-scan:
	@echo 'the public edge exists only on TARGET=aks — a kind cluster has no internet-reachable address,' >&2
	@echo 'and the inbound bridge there is reached by port-forward only (docs/inbound.md).' >&2
	@exit 1
endif
