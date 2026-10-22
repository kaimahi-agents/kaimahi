// Package agentsuite implements kmx construction support for AgentSuite.
//
// AgentSuite is definition and packaging data. It does not contain sessions,
// checkpoints, runtime snapshots, credentials, or secret values.
//
// The portable data model, serializer, schemas, and validator live in the
// separate agent-primitives/suite module. KMX imports its focused core, oci,
// evidence and experimental flow packages with the suitecore, suiteoci,
// suiteevidence and suiteflow aliases. The standalone policy package remains
// independent. This internal package owns builder-specific bindings, packing,
// transfer, and sandbox construction used by kmx.
//
// Provider-specific behavior belongs behind these contracts. Filesystem,
// ORAS, registry, Azure Storage, and other service bindings live in
// implementation packages and remain independently replaceable. The CAS
// contracts intentionally match ORAS method shapes structurally without
// importing ORAS.
//
// Beyond the builder-owned sandbox binding and image metadata and attestation
// inspection, it holds provider-neutral build, packing, CAS and transfer
// contracts. Schemas are checkout assets; incident-analyst data is builder test
// support, and realistic build inputs are not portable conformance or evidence
// of runtime qualification. Embedded build descriptors are build inputs, not
// native member images.
//
// Nested packages own distinct seams. The experimental AgentKit adapter uses a
// digest-pinned frontend and selected Docker buildx builder, streams OCI output
// and reports that a runtime base is not composed. Local artifact readers
// perform bounded, confined, no-follow ingestion and exact selected-graph
// checks without registry, auth or subprocess dependencies. Registry adapters
// add exact-digest snapshots, unsigned provenance, writer exclusion and
// credential-store transport; public recipe loading does not import that
// transport. Workspace authoring and loading freeze source and cases and verify
// packaged identity, including source-free snapshots. Profiles, multi-member
// inputs, flow and policy declarations have explicit experimental loaders;
// default execution gates stay strict. Declared Sessions associations are not
// image verification or execution or Deploy authority.
package agentsuite
