// Package suiteview loads a verified read-only view of one AgentSuite member's
// portable recipe from a local OCI layout or uncompressed OCI layout tar. It
// can also project bounded member data from a caller-verified immutable file
// snapshot without asserting an enclosing artifact identity.
//
// Loading is read-only with respect to caller input. It returns exact immutable
// identities, copied instructions, and the complete portable model requirement.
// It does not select a runtime, composition, target, inference deployment, or
// credential; execute cases; verify evaluation evidence; publish; or Lift.
// Callers must not mutate a layout concurrently with LoadLayout.
package suiteview
