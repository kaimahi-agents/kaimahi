// Package agentsuite validates the draft AgentSuite artifact format.
//
// AgentSuite is definition and packaging data. It does not contain sessions,
// checkpoints, runtime snapshots, credentials, or secret values.
//
// The packing, storage, and transfer contracts declared in packer.go,
// storage.go, and transfer.go are an extraction boundary. Keep them portable
// enough to move into a standalone AgentSuite module without changing callers.
// They must not depend on host lifecycle or CLI types, operating-system paths,
// ORAS interfaces, cloud provider SDKs, authentication, UI state, or deployment
// policy.
//
// Provider-specific behavior belongs behind these contracts. Filesystem,
// ORAS, registry, Azure Storage, and other service bindings live in
// implementation packages and remain independently replaceable. The CAS
// contracts intentionally match ORAS method shapes structurally without
// importing ORAS.
package agentsuite
