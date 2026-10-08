package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
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

func TestAgentEvaluateSessionsDoesNotUseDefaultOrkaPort(t *testing.T) {
	cmd := newAgentEvaluateCommand(&commandState{app: &app.App{Out: &bytes.Buffer{}}})
	cmd.SetArgs([]string{filepath.Join(t.TempDir(), "absent"), "--sessions", "127.0.0.1:8080"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "read bundle directory") {
		t.Fatalf("sessions command never reached source loading: %v", err)
	}
}
