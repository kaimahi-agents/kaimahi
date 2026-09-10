# Repository-only demos, connectors and probes around kmx.
# TARGET selects the environment for those retained recipes.
#
# KUBE_CTX is overridable, which is the whole point of the managed path and
# also its one hazard. Nearly every MUTATING target below
# therefore depends on `guard` (scripts/kube-guard.sh): it prints where
# the action is going, and demands explicit confirmation for anything that
# is not a local kind cluster. Fail closed — no confirmation, no action.
#
# The exceptions, stated so the rule is not trusted further than it holds:
# `netpol-verify` and `inbound-fire` run the same guard INSIDE their
# scripts, deriving the context from the KUBECTL they are handed rather
# than an inherited KUBE_CTX; managed lift aliases are guarded inside kmx
# (the cluster phase has no context to guard yet); and `plane-image` and
# `erp-image` on AKS build in the registry and touch no cluster at all.
TARGET         ?= kind

# A bare `make` is build-only. Provisioning a cluster is consequential and
# stays behind the explicit `make up`; the default builds kmx and prints where
# it was written.
.DEFAULT_GOAL := build

# Container engine for the kind demo path. Explicit rather than auto-detected:
# which engine built an image is exactly the kind of thing that should be
# visible in the command, not inferred from what happens to be installed.
#   make erp CONTAINER_ENGINE=podman
CONTAINER_ENGINE ?= docker
KIND_CLUSTER   ?= kaimahi-p1
AKS_CLUSTER    ?= kaimahi
KAGENT_VERSION ?= 0.9.12
MODEL          ?= qwen2.5:3b
KAGENT         ?= bin/kagent
export KMX_KIND_CLUSTER := $(KIND_CLUSTER)
export KMX_CONTAINER_ENGINE := $(CONTAINER_ENGINE)
export KMX_KAGENT_VERSION := $(KAGENT_VERSION)
export KMX_MODEL := $(MODEL)
export KMX_KAGENT := $(KAGENT)
export KMX_CONFIRM := $(KAIMAHI_CONFIRM)
# ---- kmx ---------------------------------------------------------------
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
# that `make sandbox`, `make lift` or a blueprint run then applied.
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
		CRED="$$KMX_CRED" \
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
GOVERNED_PRESET  ?= governed-ollama
# The proxy leaves the cluster only when Copilot is enabled
# (k8s/egress-copilot.yaml). Not on kind by default — the probe asserts
# the proxy is internet-free. Set to 1 after `make plane-copilot-secret`.
COPILOT_EGRESS   ?= 0
else ifeq ($(TARGET),aks)
KUBE_CTX         ?= $(AKS_CLUSTER)
# The demo ERP travels the same road as the proxy: built by the registry,
# pulled by the kubelet identity, never published. A private ACR is not
# publication, and the guardrail against publishing the demo ERP holds.
ERP_IMAGE        ?= $(ACR_NAME).azurecr.io/$(ERP_IMAGE_REPO):$(ERP_IMAGE_TAG)
ERP_TARGET       := registry
# Copilot-only on AKS. No Ollama is deployed there, so the agent goes
# straight onto the governed Copilot preset rather than the ollama one.
GOVERNED_PRESET  ?= governed-copilot
# Copilot-only: the proxy's 443 allowance is always applied here.
COPILOT_EGRESS   ?= 1
else
$(error unknown TARGET '$(TARGET)' — expected 'kind' or 'aks')
endif

export KMX_KUBE_CTX := $(KUBE_CTX)

ERP_PULL_POLICY   ?= IfNotPresent
KUBECTL        := kubectl --context $(KUBE_CTX)
CRED           ?= hello-world
export KMX_ADMIN_PORT := $(ADMIN_PORT)
export KMX_OPS_PORT := $(OPS_PORT)
export KMX_CRED := $(CRED)
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
# The READ tools are allowlisted from the start. The two consequential
# ones are not allowlisted and must never be: creating the release branch
# and dispatching a build are the actions a human approves, one call at a
# time. There is no destructive tool in either list, and none is
# offered by the servers either — the upstream table excludes them at the
# server with X-MCP-Exclude-Tools / X-MCP-Toolsets.
CRED_RELEASE   ?= release-agent
RELEASE_TOOLS  ?= get_latest_release,list_tags,list_releases,get_release_by_tag,list_pull_requests,list_commits,actions_list,actions_get,core_list_projects,pipelines_definition,pipelines_build,pipelines_build_log
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

.PHONY: build guard model-secret copilot-secret \
	plane-image \
	slack-secret slack-mcp govern-slack \
	slack-post slack-down aks-creds \
	netpol-verify egress-copilot egress-copilot-off \
	inbound-secret inbound-fire \
	inbound-expose inbound-unexpose exposure-scan \
	slack-approvers notify-slack slack-mention \
	github-revoke egress-hosted egress-hosted-off \
	govern-github github-ask github-down \
	erp erp-image erp-fixtures govern-ap ap-ask ap-demo ap-injection ap-down \
	release-revoke govern-release release-down

## build: build kmx from this checkout and print the resulting path
build: $(KMX)
	@echo "kmx ready: $(abspath $(KMX))"

# guard: the context-safety net every MUTATING target depends on. Prints
# the target context/namespaces; demands explicit confirmation for
# anything that is not a local kind cluster; fails closed. Make runs it
# once per invocation.
#
# The scripts/tool-*-probe.sh scripts mutate governance state and ARE
# guarded, because they run outside make and
# governance state and ARE guarded, because they run outside make and
# would otherwise follow whatever `kubectl config current-context` says —
# which `az aks get-credentials` rewrites. Mutation is why anyone cares;
# an inherited context is what makes it a surprise.
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

## ---- the governance plane (docs/spend.md) ----

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

## ---- the enforcing MCP gateway (docs/tool-governance.md) ----

## ---- the managed-cluster path (docs/aks.md) ----
#
# Azure identifiers are supplied by the operator and never committed:
#   AKS_RESOURCE_GROUP  required   the group these scripts create/delete
#   ACR_NAME            required   globally-unique private registry name
#   AKS_CLUSTER         optional   cluster + kube-context (default kaimahi)
#   AKS_LOCATION        optional   default westus3
#   AKS_NODE_SIZE       optional   default Standard_B4ms
#   AKS_NODE_COUNT      optional   default 1
#   AKS_NETWORK_POLICY  optional   cilium (default) | azure | calico; an
#                                  explicitly empty value is forwarded and
#                                  refused rather than replaced by a default
# See docs/aks.md for why those defaults, and what a run costs.

## aks-creds: refresh the kubeconfig entry for an existing AKS cluster
aks-creds:
	@test -n "$(AKS_RESOURCE_GROUP)" || \
		{ echo 'usage: make aks-creds AKS_RESOURCE_GROUP=<rg> [AKS_CLUSTER=<name>]' >&2; exit 1; }
	az aks get-credentials --name $(AKS_CLUSTER) \
		--resource-group $(AKS_RESOURCE_GROUP) --overwrite-existing

## kmx: build the CLI from this checkout
# Not .PHONY: retained demos rebuild only when an input changed.
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
# Still here because `slack-post` and `github-ask` invoke it through
# $(call kagent_forward,...).
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
govern-slack: guard $(KMX)
	@$(KMX_ENV) $(KMX) credential issue $(CRED_SLACK) --secret kaimahi-slack-token
	@$(KMX_ENV) $(KMX) tools allow "$(SLACK_TOOLS)" --credential $(CRED_SLACK)
	$(KUBECTL) apply -f k8s/kaimahi-slack.yaml
	$(KUBECTL) -n kagent wait \
		--for=jsonpath='{.status.conditions[?(@.type=="Accepted")].status}'=True \
		remotemcpserver/kaimahi-slack --timeout=300s
	$(KUBECTL) apply -f k8s/slack-agent.yaml
	$(KUBECTL) -n kagent patch agents.kagent.dev hello-slack --type merge \
		-p '{"spec":{"declarative":{"tools":[{"type":"McpServer","mcpServer":{"apiGroup":"kagent.dev","kind":"RemoteMCPServer","name":"kaimahi-slack","toolNames":[$(SLACK_TOOLNAMES_JSON)]}}]}}}'
	$(KUBECTL) -n kagent wait \
		--for=jsonpath='{.status.conditions[?(@.type=="Ready")].status}'=True \
		agents.kagent.dev/hello-slack --timeout=300s

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
	-$(KUBECTL) -n kagent delete agents.kagent.dev hello-slack
	-$(KUBECTL) -n kagent delete remotemcpserver kaimahi-slack
	-$(KUBECTL) -n kaimahi delete mcpserver kaimahi-slack-mcp

## ---- the network boundary (docs/egress.md) ----
#
# The policies themselves need no target: k8s/plane/network-policy.yaml
# ships with the plane on every environment. What needs a target is PROOF:
# a NetworkPolicy the CNI ignores is indistinguishable from one
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
## public addresses — the Copilot upstream. `kmx models credential copilot`
## applies this for you; this target is for the case where the token was
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

## github-revoke: the inverse — delete the token Secret and close the
## allowance. Governed GitHub calls then fail closed (503: no credential;
## and 502: no route out), which is the point.
github-revoke: guard
	$(KUBECTL) -n kaimahi delete secret kaimahi-github-pat --ignore-not-found
	$(KUBECTL) delete -f k8s/egress-hosted.yaml --ignore-not-found

## egress-hosted / egress-hosted-off: the allowance on its own (CI's
## synthetic-upstream steps use these). Delete by manifest, not by a typed
## name, so a renamed policy
## cannot leave the hole open with exit 0.
egress-hosted: guard
	$(KUBECTL) apply -f k8s/egress-hosted.yaml

egress-hosted-off: guard
	$(KUBECTL) delete -f k8s/egress-hosted.yaml --ignore-not-found

## govern-github: put the GitHub demo agent behind the MCP gateway —
## issue its kmh_ credential (agent-side Secret kaimahi-github-token),
## set the READ-ONLY allowlist, apply the Kaimahi RemoteMCPServer and the
## agent. The write tool is deliberately absent from the allowlist.
govern-github: guard $(KMX)
	@$(KMX_ENV) $(KMX) credential issue $(CRED_GITHUB) --secret kaimahi-github-token
	@$(KMX_ENV) $(KMX) tools allow "$(GITHUB_TOOLS)" --credential $(CRED_GITHUB)
	$(KUBECTL) apply -f k8s/kaimahi-github.yaml
	$(KUBECTL) -n kagent wait \
		--for=jsonpath='{.status.conditions[?(@.type=="Accepted")].status}'=True \
		remotemcpserver/kaimahi-github --timeout=300s
	$(KUBECTL) apply -f k8s/github-agent.yaml
	$(KUBECTL) -n kagent patch agents.kagent.dev hello-github --type merge \
		-p '{"spec":{"declarative":{"tools":[{"type":"McpServer","mcpServer":{"apiGroup":"kagent.dev","kind":"RemoteMCPServer","name":"kaimahi-github","toolNames":[$(GITHUB_TOOLNAMES_JSON)]}}]}}}'
	$(KUBECTL) -n kagent wait \
		--for=jsonpath='{.status.conditions[?(@.type=="Ready")].status}'=True \
		agents.kagent.dev/hello-github --timeout=300s

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
	-$(KUBECTL) -n kagent delete agents.kagent.dev hello-github
	-$(KUBECTL) -n kagent delete remotemcpserver kaimahi-github

## ---- the release agent (docs/release-agent.md) ----
#
# Kaimahi's first real user. An agent reads what merged since the last
# release, DRAFTS the notes, and proposes each consequential call; a human
# approves the exact call; the workflow and the pipelines it dispatches
# build and publish. The agent never carries a byte and never decides to
# ship.

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
govern-release: guard $(KMX)
	@$(KMX_ENV) $(KMX) credential issue $(CRED_RELEASE) --secret kaimahi-release-token
	@$(KMX_ENV) $(KMX) tools allow "$(RELEASE_TOOLS)" --credential $(CRED_RELEASE)
	$(KUBECTL) apply -f k8s/kaimahi-release-github.yaml -f k8s/kaimahi-release-ado.yaml
	$(KUBECTL) -n kagent wait --for=condition=Accepted \
		remotemcpserver/kaimahi-release-github --timeout=300s
	$(KUBECTL) -n kagent wait --for=condition=Accepted \
		remotemcpserver/kaimahi-release-ado --timeout=300s
	$(KUBECTL) apply -f k8s/release-agent.yaml
	$(KUBECTL) -n kagent wait --for=condition=Ready agents.kagent.dev/release-agent --timeout=300s

## release-down: remove the release agent and both seams. The tokens are a
## separate decision: make release-revoke.
release-down: guard
	-$(KUBECTL) -n kagent delete agents.kagent.dev release-agent
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
govern-ap: guard $(KMX)
	@$(KMX_ENV) $(KMX) credential issue $(CRED_AP) --secret kaimahi-ap-token
	@$(KMX_ENV) $(KMX) tools allow "$(AP_TOOLS)" --credential $(CRED_AP)
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
	$(KUBECTL) -n kagent patch agents.kagent.dev ap-agent --type merge \
		-p '{"spec":{"declarative":{"modelConfig":"$(GOVERNED_PRESET)","tools":[{"type":"McpServer","mcpServer":{"apiGroup":"kagent.dev","kind":"RemoteMCPServer","name":"kaimahi-erp","toolNames":[$(AP_TOOLNAMES_JSON)]}}]}}}'
	$(KUBECTL) -n kagent wait \
		--for=jsonpath='{.status.conditions[?(@.type=="Ready")].status}'=True \
		agents.kagent.dev/ap-agent --timeout=300s

## ap-ask: ask the AP agent to investigate an invoice.
##   make ap-ask AP_INVOICE=INV-88134
ap-ask: export KAIMAHI_AP_TASK = Investigate invoice $(AP_INVOICE) and resolve it.
ap-ask: $(KMX)
	@$(KMX_ENV) $(KMX) agent chat --json ap-agent "$$KAIMAHI_AP_TASK"

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
ap-demo: guard $(KMX)
	@$(KMX_ENV) KUBECTL="$(KUBECTL)" KMX='$(abspath $(KMX))' \
		CRED_AP=$(CRED_AP) SLACK_USER='$(SLACK_USER)' \
		AP_HUMAN='$(AP_HUMAN)' \
		bash scripts/ap-demo.sh

## ap-injection: the manipulated invoice — the agent may comply; the call
## is denied anyway, audited with the changed payee, and cannot ride the
## approval the earlier call earned.
ap-injection: guard $(KMX)
	@$(KMX_ENV) KUBECTL="$(KUBECTL)" KMX='$(abspath $(KMX))' \
		CRED_AP=$(CRED_AP) SLACK_USER='$(SLACK_USER)' \
		AP_HUMAN='$(AP_HUMAN)' \
		bash scripts/ap-injection.sh

## ap-down: remove the accounts-payable demo (agent, gateway seam, ERP)
ap-down: guard
	-$(KUBECTL) -n kagent delete agents.kagent.dev ap-agent
	-$(KUBECTL) -n kagent delete remotemcpserver kaimahi-erp
	-$(KUBECTL) delete -f k8s/erp-mcp.yaml
	-$(KUBECTL) -n kaimahi delete configmap kaimahi-erp-fixtures

## ---- inbound connectors (docs/inbound.md) ----
#
# The plane's one ingress: an external event (a webhook) may trigger a
# kagent agent, on the plane's terms. The hooks live in the committed
# upstreams table (k8s/plane/upstreams.yaml); these targets store its
# signing secret and deliver an event.
HOOK          ?= demo
EVENT         ?= Reply with exactly the word PONG.

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
notify-slack: guard $(KMX)
	@$(KMX_ENV) $(KMX) credential issue $(CRED_PLANE) --secret kaimahi-notifier-token --namespace kaimahi
	@$(KMX_ENV) $(KMX) tools allow "$(SLACK_POST_TOOL)" --credential $(CRED_PLANE)

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
