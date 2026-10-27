// Package agentsuite provides KMX's AgentSuite construction and transfer contracts.
// It owns builder-specific plans and bindings; the portable format and its
// validation belong to the separate github.com/agent-primitives/suite module.
// Experimental image declarations bind published source and generated configuration;
// they grant no evaluation or deployment authority and are not sandbox conformance.
// The AgentKit adapter builds model-independent mounted configurations, while the
// ORAS adapter verifies source through localartifact and publishes complete image
// and attestation graphs with explicit different-digest tag replacement.
package agentsuite
