// Package lifecycle defines KMX's internal implementation ports and
// runtime-native intermediate values.
//
// Public consumers depend on the northbound contracts in pkg/kmx. The
// application composition root wires these ports to concrete runtime
// implementations; environment setup and focused AgentSuite ports live outside
// this deployment subsystem.
//
// It holds experimental deployment runtime ports, runtime-native documents,
// recovery and receipt factories behind the public alpha workflow. Environment
// setup has no mirrored southbound SPI; focused builder, storage and transfer
// ports remain with the internal suite implementation. This is experimental
// scaffolding.
package lifecycle
