// Package kaimahi embeds the assets kmx needs when installed without a checkout.
// go:embed paths are relative to this module root and cannot cross into plane/.
package kaimahi

import "embed"

// Manifests holds the runtime, retained agents, model presets and model plane.
// Presets name Secrets; they never contain credential values. Explicit paths
// keep the packaging boundary independent of unrelated checkout additions.
//
//go:embed k8s/ollama.yaml k8s/kagent-values.yaml k8s/hello-world.yaml k8s/tools-agent.yaml
//go:embed k8s/plane/namespace.yaml k8s/plane/postgres.yaml k8s/plane/proxy.yaml
//go:embed k8s/plane/upstreams.yaml k8s/plane/network-policy.yaml
//go:embed k8s/models
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
