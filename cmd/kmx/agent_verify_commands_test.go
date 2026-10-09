package main

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
)

// Catch a verify command that lets a receipt choose a destination or loads kubeconfig.
func TestAgentVerifyRequiresOperatorDestination(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"agent", "verify", "unused.json"}, "--sessions is required"},
		{[]string{"agent", "verify", "unused.json", "--sessions", " "}, "--sessions is required"},
		{[]string{"agent", "verify", "unused.json", "--sessions", "127.0.0.1:8080", "--sessions-ca", " "}, "--sessions-ca requires a non-empty"},
		{[]string{"agent", "verify", "unused.json", "--sessions", "127.0.0.1:8080", "--timeout", "0s"}, "--timeout must be"},
	} {
		var out bytes.Buffer
		deps, loads := testDependencies(&out, &out)
		err := execute(tc.args, deps)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: %v, want %q", tc.args, err, tc.want)
		}
		if *loads != 0 {
			t.Fatal("verify loaded cluster configuration")
		}
	}
}

func TestAgentVerifyBypassesClusterConfiguration(t *testing.T) {
	var out bytes.Buffer
	deps := productionDependencies()
	deps.stdout, deps.stderr = &out, &out
	calls := 0
	deps.loadConfig = func(string, string) (*config.Config, error) {
		calls++
		return nil, errors.New("broken cluster configuration")
	}
	err := execute([]string{"agent", "verify", filepath.Join(t.TempDir(), "absent.json"), "--sessions", "127.0.0.1:8080"}, deps)
	if calls != 0 || err == nil || !strings.Contains(err.Error(), "read sessions receipt") {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}
