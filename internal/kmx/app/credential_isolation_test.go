package app

import (
	"errors"
	"testing"

	"github.com/zalando/go-keyring"
)

var errAppTestKeyring = errors.New("native credential access is disabled in app tests")

func TestAppTestCredentialStoreIsIsolated(t *testing.T) {
	// The TestMain mock must reject every operation, independent of platform or
	// whether the machine has a working session bus or an unlocked keychain.
	if _, err := keyring.Get("app-test-isolation", "test"); !errors.Is(err, errAppTestKeyring) {
		t.Fatalf("get bypassed the isolated credential store: %v", err)
	}
	for operation, err := range map[string]error{
		"set":    keyring.Set("app-test-isolation", "test", "not-a-credential"),
		"delete": keyring.Delete("app-test-isolation", "test"),
	} {
		if !errors.Is(err, errAppTestKeyring) {
			t.Fatalf("%s bypassed the isolated credential store: %v", operation, err)
		}
	}
}
