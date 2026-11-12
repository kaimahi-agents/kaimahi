// Package localagent owns resident local Markdown-agent lifecycle and execution.
//
// A canonical checked source path has a stable md- identity. Each activation
// creates or reuses an immutable li- generation bound to one composed source
// snapshot and inference route. The package owns private lifecycle records,
// worker processes, local sessions, crash recovery, upgrades, and stop behavior.
// It does not parse CLI arguments, select cluster targets, evaluate AgentSuites,
// or grant publication or deployment authority.
package localagent
