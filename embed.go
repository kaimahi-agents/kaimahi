// Package kaimahi embeds the assets kmx needs when installed without a checkout.
// Embedded paths are relative to this module root.
package kaimahi

import "embed"

// Manifests holds the native local model and Orka Kubernetes Tool assets.
// Explicit paths keep the packaging boundary independent of unrelated
// checkout additions.
//
//go:embed k8s/ollama.yaml
//go:embed k8s/orka-k8s-tool.yaml scripts/orka-k8s-tool.py
var Manifests embed.FS

// Managed holds managed-cluster provisioning and context-guard scripts.
// Callers materialize them in a checkout-shaped temporary tree, preserving
// their relative references and fail-closed shell behavior.
//
//go:embed scripts/aks-up.sh scripts/aks-down.sh scripts/kube-guard.sh
var Managed embed.FS
