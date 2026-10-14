// Copyright (c) Kaimahi contributors. Licensed under the MIT License.
// Package policy defines AgentSuite enforcement policy contracts.
package policy

const (
	SchemaVersion       = "agentsuite.policy/v1"
	FilesystemDenyWrite = "filesystem/deny-write/v1"
)

// SuitePolicy is strict user intent. DenyWritePaths is a set of absolute paths.
type SuitePolicy struct {
	Version              string   `json:"version"`
	DenyWritePaths       []string `json:"denyWritePaths"`
	RequiredCapabilities []string `json:"requiredCapabilities,omitempty"`
}

// AdministrativePolicy adds mandatory restrictions; composition never removes user restrictions.
type AdministrativePolicy struct {
	DenyWritePaths []string `json:"denyWritePaths,omitempty"`
}

// EffectivePolicy is normalized, fully composed policy.
type EffectivePolicy struct {
	Version              string   `json:"version"`
	DenyWritePaths       []string `json:"denyWritePaths"`
	RequiredCapabilities []string `json:"requiredCapabilities"`
}

// ProviderPayload carries authoritative canonical bytes. Consumers must preserve PolicyBytes.
type ProviderPayload struct {
	Version              string   `json:"version"`
	PolicyBytes          []byte   `json:"policyBytes"`
	PolicyDigest         string   `json:"policyDigest"`
	RequiredCapabilities []string `json:"requiredCapabilities"`
}
