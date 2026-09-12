# Checkout-only operator helpers around kmx. The default only builds.
# Cluster-mutating recipes depend on guard; netpol-verify guards its own
# effective kubectl context. Registry image builds touch no cluster.
TARGET ?= kind
.DEFAULT_GOAL := build
CONTAINER_ENGINE ?= docker
KIND_CLUSTER ?= kaimahi-p1
AKS_CLUSTER ?= kaimahi
KAGENT_VERSION ?= 0.9.12
MODEL ?= qwen2.5:3b
KAGENT ?= bin/kagent
KMX ?= bin/kmx
CRED ?= hello-world

# Relink for every embedded asset: the binary also runs outside a clone.
KMX_SOURCES := go.mod embed.go $(shell find cmd/kmx internal/kmx -name '*.go' 2>/dev/null)
KMX_ASSETS := k8s/ollama.yaml k8s/kagent-values.yaml k8s/hello-world.yaml k8s/tools-agent.yaml \
	k8s/egress-hosted.yaml k8s/egress-copilot.yaml \
	$(wildcard k8s/plane/*.yaml) $(wildcard k8s/models/*.yaml) \
	$(wildcard k8s/observability/*) \
	scripts/aks-up.sh scripts/aks-down.sh scripts/plane-deploy.sh \
	scripts/netpol-probe.sh scripts/kube-guard.sh

# Preserve the environment interface used by the installed command.
export KMX_KIND_CLUSTER := $(KIND_CLUSTER)
export KMX_CONTAINER_ENGINE := $(CONTAINER_ENGINE)
export KMX_KAGENT_VERSION := $(KAGENT_VERSION)
export KMX_MODEL := $(MODEL)
export KMX_KAGENT := $(KAGENT)
export KMX_CONFIRM := $(KAIMAHI_CONFIRM)
export KMX_CHAT_PORT := $(CHAT_PORT)
export KMX_ADMIN_PORT := $(ADMIN_PORT)
export KMX_OPS_PORT := $(OPS_PORT)
export KMX_CRED := $(CRED)
KMX_ENV = KIND_CLUSTER="$$KMX_KIND_CLUSTER" KUBE_CTX="$$KMX_KUBE_CTX" \
	CONTAINER_ENGINE="$$KMX_CONTAINER_ENGINE" KAGENT_VERSION="$$KMX_KAGENT_VERSION" \
	MODEL="$$KMX_MODEL" $(if $(filter command line,$(origin CHAT_PORT)),CHAT_PORT="$$KMX_CHAT_PORT",) \
	$(if $(filter command line environment override,$(origin KAGENT)),KAGENT="$$KMX_KAGENT",) \
	ADMIN_PORT="$$KMX_ADMIN_PORT" OPS_PORT="$$KMX_OPS_PORT" \
	CRED="$$KMX_CRED" KAIMAHI_CONFIRM="$$KMX_CONFIRM"

PLANE_IMAGE_REPO ?= kaimahi-proxy
PLANE_IMAGE_TAG ?= p10
PLANE_VERSION ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)

ifeq ($(TARGET),kind)
KUBE_CTX ?= kind-$(KIND_CLUSTER)
COPILOT_EGRESS ?= 0
else ifeq ($(TARGET),aks)
KUBE_CTX ?= $(AKS_CLUSTER)
COPILOT_EGRESS ?= 1
else
$(error unknown TARGET '$(TARGET)' — expected 'kind' or 'aks')
endif
export KMX_KUBE_CTX := $(KUBE_CTX)
KUBECTL := kubectl --context $(KUBE_CTX)
GUARD_NS ?= kagent, kaimahi, ollama

.PHONY: build guard model-secret copilot-secret plane-image aks-creds \
	netpol-verify egress-copilot egress-copilot-off egress-hosted egress-hosted-off

## build: build kmx from this checkout and print the resulting path
build: $(KMX)
	@echo "kmx ready: $(abspath $(KMX))"

$(KMX): $(KMX_SOURCES) $(KMX_ASSETS)
	@command -v go >/dev/null 2>&1 || { \
		echo 'kmx needs a Go toolchain to build from a checkout (https://go.dev/dl/).' >&2; \
		echo 'Without a clone: go install github.com/kaimahi-agents/kaimahi/cmd/kmx@<sha>' >&2; \
		exit 1; }
	@mkdir -p $(dir $@)
	go build -o $(KMX) ./cmd/kmx

guard:
	@KUBE_CTX='$(KUBE_CTX)' KUBE_NS='$(GUARD_NS)' \
		bash scripts/kube-guard.sh '$(if $(MAKECMDGOALS),$(MAKECMDGOALS),$(.DEFAULT_GOAL)) [TARGET=$(TARGET)]'

## model-secret: capture an API key from stdin; no credential in argv or logs
model-secret: guard
	@test -n "$(NAME)" || { echo 'usage: make model-secret NAME=<preset>-api-key' >&2; exit 1; }
	@echo 'Paste the API key, press Enter, then Ctrl-D:' >&2
	@tr -d '\n' | $(KUBECTL) -n kagent create secret generic $(NAME) \
		--from-file=api-key=/dev/stdin

## copilot-secret: device login and short-lived token, fail-closed custody
copilot-secret: guard
	@KUBECTL="$(KUBECTL)" bash scripts/copilot-secret.sh

ifeq ($(TARGET),kind)
plane-image: $(KMX)
	@$(KMX_ENV) $(KMX) plane --step image --source .
else
# Build in the operator's private registry; no public image publication.
plane-image:
	@test -n "$(ACR_NAME)" || \
		{ echo 'ACR_NAME is required for TARGET=aks (see docs/aks.md)' >&2; exit 1; }
	az acr build --registry $(ACR_NAME) --build-arg VERSION=$(PLANE_VERSION) \
		--image $(PLANE_IMAGE_REPO):$(PLANE_IMAGE_TAG) plane/
endif

## aks-creds: refresh an existing cluster's kubeconfig entry
aks-creds:
	@test -n "$(AKS_RESOURCE_GROUP)" || \
		{ echo 'usage: make aks-creds AKS_RESOURCE_GROUP=<rg> [AKS_CLUSTER=<name>]' >&2; exit 1; }
	az aks get-credentials --name $(AKS_CLUSTER) \
		--resource-group $(AKS_RESOURCE_GROUP) --overwrite-existing

## netpol-verify: prove blocked paths against a reachable control
netpol-verify:
	@KUBECTL="$(KUBECTL)" COPILOT_EGRESS=$(COPILOT_EGRESS) bash scripts/netpol-probe.sh

# Public TCP 443 only, private ranges excluded. Remove by manifest so a
# renamed policy cannot silently leave the allowance open.
egress-copilot: guard
	$(KUBECTL) apply -f k8s/egress-copilot.yaml

egress-copilot-off: guard
	$(KUBECTL) delete -f k8s/egress-copilot.yaml --ignore-not-found

egress-hosted: guard
	$(KUBECTL) apply -f k8s/egress-hosted.yaml

egress-hosted-off: guard
	$(KUBECTL) delete -f k8s/egress-hosted.yaml --ignore-not-found
