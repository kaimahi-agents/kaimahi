// Package kaimahi embeds the assets kmx needs when installed without a checkout.
// Embedded paths are relative to this module root.
package kaimahi

import (
	"embed"
	"encoding/json"
	"strings"
)

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

// failureWording is the canvas's canonical failure wording; the Copilot plugin
// generates its browser module from the same JSON.
//
//go:embed plugins/copilot-wsl/com.github.copilot/extensions/agent-view/failure-wording.json
var failureWording []byte

// FileCheckFailure returns the canonical failure text for a file check, or ""
// for kinds without one (llmJudge, unknown). Callers sanitize value.
func FileCheckFailure(kind, value string) string {
	var templates map[string]string
	if err := json.Unmarshal(failureWording, &templates); err != nil {
		panic("kaimahi: invalid embedded failure wording: " + err.Error())
	}
	return strings.Replace(templates[kind], "{value}", value, 1)
}
