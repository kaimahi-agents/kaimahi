// Package kmx defines experimental contracts for building agent revisions,
// preparing targets, and managing agent deployments.
//
// The contracts keep three ownership domains separate: platforms own target
// infrastructure, runtimes own runtime-native workload mechanics, and KMX
// workflows compose those capabilities for authors. In particular, retiring an
// agent deployment is not authority to tear down its target.
//
// This package is alpha design evidence. It is not yet wired to the kmx CLI and
// makes no compatibility commitment.
package kmx
