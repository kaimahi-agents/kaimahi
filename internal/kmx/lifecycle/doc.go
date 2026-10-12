// Package lifecycle defines KMX's internal implementation ports and
// runtime-native intermediate values.
//
// Public consumers depend on the northbound contracts in pkg/kmx. The
// application composition root wires these ports to concrete platform and
// runtime implementations; focused AgentSuite ports live with that subsystem.
package lifecycle
