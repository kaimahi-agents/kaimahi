// Package workspacetest owns the single-use lifecycle and frozen identity of a
// prepared native workspace Test invocation.
//
// It grants no source parsing, cluster access, consent, evidence, publication,
// or Deploy authority. Runtime adapters retain those capabilities explicitly
// while the existing App orchestration migrates behind this operation boundary.
// A prepared invocation is in-process authority: it cannot be serialized or
// replayed, and Close consumes it even when called before execution.
// Generated validation scratch is removed only through retained identities;
// interrupted or uncertain state refuses with a move-aside recovery instruction.
package workspacetest
