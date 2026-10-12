// Package kmx defines experimental contracts for building agent revisions,
// shipping AgentSuite OCI artifacts, preparing environments, and managing
// deployments.
//
// The contracts keep ownership domains separate: AgentSuites owns validation
// plus the initial image-backed build and push workflow, platforms own target
// infrastructure, runtimes own workload mechanics, and KMX composes those
// capabilities. Builder selection, OCI transport and runtime adapters remain
// implementation details.
// In particular, retiring a deployment is not authority to tear down its target.
//
// This package is alpha design evidence. It is not yet wired to the kmx CLI and
// makes no compatibility commitment.
package kmx
