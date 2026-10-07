// Package agentsuite validates the draft AgentSuite artifact format.
//
// AgentSuite is definition and packaging data. It does not contain sessions,
// checkpoints, runtime snapshots, credentials, or secret values.
//
// The packaging contracts declared in storage.go and packer.go are an
// extraction boundary. Keep them portable enough to move into a standalone
// AgentSuite module without changing callers. They must not depend on host
// lifecycle or CLI types, operating-system paths, ORAS interfaces, cloud
// provider SDKs, authentication, UI state, or deployment policy.
//
// Provider-specific behavior belongs behind these contracts. Filesystem,
// ORAS, registry, Azure Storage, and other service bindings should live in
// implementation packages and remain independently replaceable.
package agentsuite
