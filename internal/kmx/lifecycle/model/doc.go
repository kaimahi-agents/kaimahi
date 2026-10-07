// Package model defines provisional internal values for agent revisions,
// AgentSuite OCI artifacts, environments, and deployments.
//
// The contracts keep ownership domains separate: AgentSuites owns definition
// packaging and sandbox-image derivation, platforms own target infrastructure,
// runtimes own workload mechanics, and KMX composes those capabilities. In
// particular, retiring a deployment is not authority to tear down its target.
//
// This package is internal RFC evidence. It is not wired to the kmx CLI and
// makes no compatibility commitment.
package model
