// Package agentsuite implements kmx construction support for AgentSuite.
//
// AgentSuite is definition and packaging data. It does not contain sessions,
// checkpoints, runtime snapshots, credentials, or secret values.
//
// The portable data model, serializer, schemas, and validator live in the
// top-level agentsuite package. This internal package owns builder-specific
// bindings, packing, transfer, and sandbox construction used by kmx.
//
// Provider-specific behavior belongs behind these contracts. Filesystem,
// ORAS, registry, Azure Storage, and other service bindings live in
// implementation packages and remain independently replaceable. The CAS
// contracts intentionally match ORAS method shapes structurally without
// importing ORAS.
package agentsuite
