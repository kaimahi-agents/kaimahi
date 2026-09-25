// Package kaimahi embeds the assets kmx needs when installed without a checkout.
// Embedded paths are relative to this module root and cannot cross into plane/.
package kaimahi

import "embed"

// Manifests holds the runtime kmx installs and the model plane it deploys.
// Explicit paths keep the packaging boundary independent of unrelated
// checkout additions.
//
// What is NOT here any more: the legacy runtime's Helm values, its two demo
// agents, and the k8s/models ModelConfig presets. All five were kagent
// v1alpha2 objects for a runtime kmx no longer installs, and nothing applied
// them — `kmx plane` applies k8s/plane/, and `kmx migrate` writes a Secret
// into the operator's own namespace.
//
//go:embed k8s/ollama.yaml
//go:embed k8s/orka-k8s-tool.yaml scripts/orka-k8s-tool.py
//go:embed k8s/plane/namespace.yaml k8s/plane/postgres.yaml k8s/plane/proxy.yaml
//go:embed k8s/plane/upstreams.yaml k8s/plane/network-policy.yaml
//go:embed k8s/egress-hosted.yaml
var Manifests embed.FS

// Managed holds managed-cluster observability, model egress and installer
// scripts. Callers materialize the scripts in a checkout-shaped temporary
// tree, preserving their relative references and fail-closed shell behavior.
//
//go:embed k8s/observability/network-policy.yaml k8s/observability/podmonitor.yaml
//go:embed k8s/observability/scrape-config.yaml k8s/observability/workbook.json
//go:embed k8s/egress-copilot.yaml
//go:embed scripts/aks-up.sh scripts/aks-down.sh scripts/plane-deploy.sh
//go:embed scripts/netpol-probe.sh scripts/kube-guard.sh
var Managed embed.FS
