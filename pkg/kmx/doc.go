// Package kmx defines experimental contracts for establishing logical agent
// environments, building agent revisions, shipping AgentSuite OCI artifacts,
// and managing deployments.
//
// The contracts keep ownership domains separate: AgentSuites owns validation
// plus the initial image-backed build and push workflow, platforms own target
// infrastructure, runtimes own workload mechanics, and KMX composes those
// capabilities. Environment setup does not itself select a deployment target;
// builder selection, OCI transport and runtime adapters remain implementation
// details. Retiring a deployment is not authority to tear down its environment.
//
// This package is alpha design evidence. It is not yet wired to the kmx CLI and
// makes no compatibility commitment.
package kmx
