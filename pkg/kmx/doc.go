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
//
// The package is standard-library only and has no concrete implementation. The
// contracts describe setup-only logical environments, neutral revisions, suite
// validate, build and push values, image identities, targets and deployment and
// retirement receipts and workflows. Runtime ports and build artifacts remain
// internal.
//
// Architecture tests keep the production dependency closure in the standard
// library and reject concrete runtime, cloud, Kubernetes, subprocess and
// terminal leakage in exported names or serialized fields. Public format
// ownership remains in the external suite module; native loaders apply stricter
// private checks after public projection.
package kmx
