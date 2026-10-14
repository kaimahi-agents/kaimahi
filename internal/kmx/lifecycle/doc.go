// Package lifecycle defines KMX's internal implementation ports and
// runtime-native intermediate values.
//
// Public consumers depend on the northbound contracts in pkg/kmx. The
// application composition root wires these ports to concrete runtime
// implementations; environment setup and focused AgentSuite ports live outside
// this deployment subsystem.
package lifecycle
