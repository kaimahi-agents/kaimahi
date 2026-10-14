package runtime

import (
	"fmt"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
)

// assertRefusedWithoutEcho proves a refusal happened, that it named a
// credential shape rather than the field's own validation complaint, and
// that it did not carry the value.
func assertRefusedWithoutEcho(t *testing.T, err error, value string) {
	t.Helper()
	if err == nil {
		t.Fatal("a credential-shaped value was accepted")
	}
	if strings.Contains(err.Error(), value) {
		t.Fatalf("the refusal echoed the credential-shaped value: %v", err)
	}
	if !strings.Contains(err.Error(), "shaped like") {
		t.Fatalf("the refusal does not name the shape class: %v", err)
	}
}

// Every declared shape's counterexample must still be accepted: a scan that
// refused ordinary values would be a gate nobody could author past.
func TestPortableCredentialScanAcceptsCounterexamples(t *testing.T) {
	for _, shape := range secretshapes.All() {
		if shape.Counterexample == "" {
			continue
		}
		t.Run(shape.Name, func(t *testing.T) {
			if err := refusePortableSecretShape("Answer using " + shape.Counterexample); err != nil {
				t.Fatalf("a counterexample was refused as a credential: %v", err)
			}
		})
	}
}

func escapedPortableKey(value string) string {
	return fmt.Sprintf("\"\\x%02x%s\"", value[0], value[1:])
}

// The same argument for a "!!binary" key: the raw scan sees base64, and the
// strict decoder resolves the payload before naming the field it did not
// find.
