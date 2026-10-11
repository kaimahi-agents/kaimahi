package main

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
)

func TestAgentEvaluateSessionsFlagValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"empty target", []string{"--sessions", " "}, "--sessions requires a non-empty"},
		{"empty CA", []string{"--sessions-ca", " "}, "--sessions-ca requires a non-empty"},
		{"context conflict", []string{"--sessions", "127.0.0.1:8080", "--to-context", "test"}, "--sessions cannot be combined with --to-context"},
		{"port conflict", []string{"--sessions", "127.0.0.1:8080", "--result-port", "19180"}, "--result-port is only for Orka"},
		{"CA without target", []string{"--sessions-ca", "ca.pem"}, "--sessions-ca requires --sessions"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := &bytes.Buffer{}
			cmd := newAgentEvaluateCommand(&commandState{app: &app.App{Out: out}})
			cmd.SetOut(out)
			cmd.SetErr(out)
			cmd.SetArgs(append([]string{"unused-bundle"}, tc.args...))
			err := cmd.Execute()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}

func TestAgentEvaluateSessionsBypassesClusterConfiguration(t *testing.T) {
	for _, sessions := range []bool{true, false} {
		t.Run(map[bool]string{true: "sessions", false: "Orka"}[sessions], func(t *testing.T) {
			out := &bytes.Buffer{}
			deps := productionDependencies()
			deps.stdout, deps.stderr = out, out
			calls := 0
			configErr := errors.New("unreadable cluster context configuration")
			deps.loadConfig = func(string, string) (*config.Config, error) {
				calls++
				return nil, configErr
			}
			args := []string{"agent", "evaluate", filepath.Join(t.TempDir(), "absent")}
			if sessions {
				args = append(args, "--sessions", "127.0.0.1:8080")
			}
			err := execute(args, deps)
			if sessions {
				if calls != 0 || err == nil || !strings.Contains(err.Error(), "read bundle directory") {
					t.Fatalf("sessions required cluster config: calls=%d err=%v", calls, err)
				}
			} else if calls != 1 || !errors.Is(err, configErr) {
				t.Fatalf("Orka no longer uses its config: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestAgentEvaluateSessionsDoesNotUseDefaultOrkaPort(t *testing.T) {
	cmd := newAgentEvaluateCommand(&commandState{app: &app.App{Out: &bytes.Buffer{}}})
	cmd.SetArgs([]string{filepath.Join(t.TempDir(), "absent"), "--sessions", "127.0.0.1:8080"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "read bundle directory") {
		t.Fatalf("sessions command never reached source loading: %v", err)
	}
}
