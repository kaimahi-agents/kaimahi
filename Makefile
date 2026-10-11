# Checkout-only operator helpers around kmx. The default only builds.
# guard checks the effective kubectl context for operator actions.
TARGET ?= kind
.DEFAULT_GOAL := build
CONTAINER_ENGINE ?= docker
KIND_CLUSTER ?= kaimahi-p1
AKS_CLUSTER ?= kaimahi
MODEL ?= qwen2.5:3b
KMX ?= bin/kmx

# Native assets packaged by kmx, including when it runs outside a clone.
KMX_ASSETS := k8s/ollama.yaml k8s/orka-k8s-tool.yaml scripts/orka-k8s-tool.py \
	scripts/aks-up.sh scripts/aks-down.sh scripts/kube-guard.sh

# Preserve the environment interface used by the installed command.
export KMX_KIND_CLUSTER := $(KIND_CLUSTER)
export KMX_CONTAINER_ENGINE := $(CONTAINER_ENGINE)
export KMX_MODEL := $(MODEL)
export KMX_CONFIRM := $(KAIMAHI_CONFIRM)

ifeq ($(TARGET),kind)
KUBE_CTX ?= kind-$(KIND_CLUSTER)
else ifeq ($(TARGET),aks)
KUBE_CTX ?= $(AKS_CLUSTER)
else
$(error unknown TARGET '$(TARGET)' — expected 'kind' or 'aks')
endif
export KMX_KUBE_CTX := $(KUBE_CTX)
GUARD_NS ?= ollama, orka-system (common, not exhaustive; see action for other namespaces)

# Let Go's cache track all source, embedded data, and build-option changes.
.PHONY: build test lint docs-check guard aks-creds $(KMX)

## test, lint, docs-check: local checks matching the keyless CI gates
test:
	go test ./...

lint:
	staticcheck ./...

docs-check:
	python3 scripts/check-brand-assets.py --selftest
	python3 scripts/check-brand-assets.py
	python3 scripts/check-doc-links.py --selftest
	python3 scripts/check-doc-links.py
	python3 scripts/check-readme-front-door-test.py
	python3 scripts/check-readme-front-door.py
	python3 scripts/check-legacy-runtime.py --selftest
	python3 scripts/check-legacy-runtime.py

## build: build kmx from this checkout and print the resulting path
build: $(KMX)
	@echo "kmx ready: $(abspath $(KMX))"

$(KMX): $(KMX_ASSETS)
	@command -v go >/dev/null 2>&1 || { \
		echo 'kmx needs a Go toolchain to build from a checkout (https://go.dev/dl/).' >&2; \
		echo 'Without a clone: go install github.com/kaimahi-agents/kaimahi/cmd/kmx@<sha>' >&2; \
		exit 1; }
	@mkdir -p $(dir $@)
	go build -o $(KMX) ./cmd/kmx

guard:
	@KUBE_CTX='$(KUBE_CTX)' KUBE_NS='$(GUARD_NS)' \
		bash scripts/kube-guard.sh '$(if $(MAKECMDGOALS),$(MAKECMDGOALS),$(.DEFAULT_GOAL)) [TARGET=$(TARGET)]'

## aks-creds: refresh an existing cluster's kubeconfig entry
aks-creds:
	@test -n "$(AKS_RESOURCE_GROUP)" || \
		{ echo 'usage: make aks-creds AKS_RESOURCE_GROUP=<rg> [AKS_CLUSTER=<name>]' >&2; exit 1; }
	az aks get-credentials --name $(AKS_CLUSTER) \
		--resource-group $(AKS_RESOURCE_GROUP) --overwrite-existing
