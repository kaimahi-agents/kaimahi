package app

import (
	"errors"
	"testing"
)

// Only a genuine object-not-found response establishes absence. Connection,
// authorization and discovery failures leave the object's state unknown.
// Missing tools and resource kinds are not proof that an object is absent.
// Callers must retain that distinction rather than proceeding as though
// a failed read established that no existing resource needs preserving.
//
// These are kubectl's real messages.
func TestOnlyAGenuineNotFoundReadsAsAbsent(t *testing.T) {
	absent := []string{
		`exit status 1: Error from server (NotFound): agents.kagent.dev "hello-world" not found`,
		`exit status 1: Error from server (NotFound): modelconfigs.kagent.dev "governed-ollama" not found`,
	}
	present := []string{
		// The cluster is down. Nothing may be concluded about the object.
		`exit status 1: The connection to the server 127.0.0.1:6443 was refused - did you specify the right host or port?`,
		`exit status 1: Unable to connect to the server: dial tcp: lookup nowhere.invalid: no such host`,
		// Read denied is not absence.
		`exit status 1: Error from server (Forbidden): agents.kagent.dev is forbidden: User "x" cannot get resource "agents"`,
		// The CRD is not installed yet — kagent has not been deployed.
		`exit status 1: error: the server doesn't have a resource type "agent"`,
		// kubectl itself is missing. "executable file not found" must not be
		// mistaken for "the agent is not found".
		`exec: "kubectl": executable file not found in $PATH`,
		`exit status 1: error: You must be logged in to the server (Unauthorized)`,
	}
	for _, message := range absent {
		if !isNotFound(errors.New(message)) {
			t.Errorf("should read as absent: %s", message)
		}
	}
	for _, message := range present {
		if isNotFound(errors.New(message)) {
			t.Errorf("must NOT read as absent: %s", message)
		}
	}
	if isNotFound(nil) {
		t.Error("no error is not an absence")
	}
}
