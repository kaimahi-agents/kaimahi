package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
)

func TestOfflineClusterFlagsValidateBeforeConfiguration(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"cluster", "up", "--offline-bundle", "bundle"}, "--step cluster"},
		{[]string{"cluster", "up", "--step", "orka", "--offline-bundle", "bundle"}, "--step cluster"},
		{[]string{"cluster", "up", "--step", "model", "--offline-bundle", "bundle"}, "--step cluster"},
		{[]string{"cluster", "up", "--step", "ollama", "--offline-bundle", "bundle"}, "--step cluster"},
		{[]string{"cluster", "up", "--step", "cluster", "--offline-bundle="}, "non-empty"},
		{[]string{"cluster", "up", "--step", "cluster", "--offline-bundle", " "}, "non-empty"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			var out bytes.Buffer
			loads := 0
			deps := dependencies{stdout: &out, stderr: &out, goos: "linux", loadConfig: func(string, string) (*config.Config, error) {
				loads++
				return nil, errors.New("configuration must not run")
			}}
			err := execute(tc.args, deps)
			if err == nil || !strings.Contains(err.Error(), tc.want) || loads != 0 {
				t.Fatalf("err=%v config loads=%d", err, loads)
			}
		})
	}
}

func TestOfflineInstallFlagsBeforeConfiguration(t *testing.T) {
	for _, args := range [][]string{
		{"--offline-bundle", "bundle"},
		{"--offline-bundle=", "--provider", "-"},
		{"--offline-bundle", " ", "--provider", "-"},
		{"--offline-bundle", "bundle", "--provider", " - "},
		{"--offline-bundle", "bundle", "--provider", "-", "--no-apply", "--dry-run"},
	} {
		loads := 0
		var out bytes.Buffer
		err := execute(append([]string{"cluster", "orka", "install"}, args...), dependencies{stdout: &out, stderr: &out, goos: "linux", loadConfig: func(string, string) (*config.Config, error) { loads++; return nil, errors.New("must not load") }})
		if err == nil || loads != 0 || strings.Contains(err.Error(), "unknown flag") {
			t.Fatalf("args=%q loads=%d err=%v", args, loads, err)
		}
	}
	var out bytes.Buffer
	err := execute([]string{"cluster", "orka", "install", "--provider", "-", "--offline-bundle", "bundle", "--model", "ignored", "--model-url", "https://unused.invalid"}, dependencies{stdout: &out, stderr: &out, goos: "linux", loadConfig: func(string, string) (*config.Config, error) { return nil, errors.New("valid dispatch") }})
	if err == nil || !strings.Contains(err.Error(), "valid dispatch") {
		t.Fatalf("inert model flags refused: %v", err)
	}
}
func TestOfflineClusterFlagDispatchAndInertHelp(t *testing.T) {
	for _, args := range [][]string{
		{"cluster", "up", "--step", "cluster", "--offline-bundle", "bundle"},
		{"cluster", "up", "--step", "cluster"},
	} {
		var out bytes.Buffer
		loads := 0
		sentinel := errors.New("configuration reached")
		err := execute(args, dependencies{stdout: &out, stderr: &out, goos: "linux", loadConfig: func(string, string) (*config.Config, error) { loads++; return nil, sentinel }})
		if !errors.Is(err, sentinel) || loads != 1 {
			t.Fatalf("args=%q err=%v loads=%d", args, err, loads)
		}
	}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"cluster", "up", "--help"}, "--offline-bundle"},
		{[]string{"__complete", "cluster", "up", "--offline-bundle", ""}, ":16"},
	} {
		var out bytes.Buffer
		err := execute(tc.args, dependencies{stdout: &out, stderr: &out, goos: "linux", loadConfig: func(string, string) (*config.Config, error) {
			t.Fatal("help/completion loaded config")
			return nil, nil
		}})
		if err != nil || !strings.Contains(out.String(), tc.want) {
			t.Fatalf("args=%q err=%v output=%s", tc.args, err, out.String())
		}
	}
	var out bytes.Buffer
	err := execute([]string{"cluster", "orka", "install", "--offline-bundle", "bundle"}, dependencies{stdout: &out, stderr: &out, goos: "linux"})
	if err == nil || !strings.Contains(err.Error(), "--provider -") {
		t.Fatalf("install requires no-provider combination: %v", err)
	}
}
