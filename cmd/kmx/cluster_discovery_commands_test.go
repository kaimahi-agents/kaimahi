package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
)

func discoveryCLIEnv(t *testing.T, body string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Unix shell fixture")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte("#!/bin/sh\n"+body+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}
func TestDiscoveryCommandDoesNotLoadConfigOrProvision(t *testing.T) {
	discoveryCLIEnv(t, `if [ "$1" = "--context" ]; then
 [ "$2" = chosen ] || exit 9
 printf '%s' '{"apiVersion":"v1","kind":"NamespaceList","items":[{"metadata":{"name":"dev","uid":"uid-dev"}}]}'
 else
 printf '"local"\n"local"\n'
 fi`)
	for _, args := range [][]string{{"cluster", "discover", "-o", "jsonl"}, {"--context", "chosen", "cluster", "discover", "-o", "jsonl"}, {"cluster", "discover", "--context=chosen", "-ojsonl"}} {
		var out, diagnostics bytes.Buffer
		deps, loads := testDependencies(&out, &diagnostics)
		deps.loadConfig = func(string, string) (*config.Config, error) { t.Fatal("config load"); return nil, nil }
		deps.newApp = func(*config.Config) *app.App { t.Fatal("configured app"); return nil }
		home := filepath.Join(t.TempDir(), "no-state")
		t.Setenv("KMX_HOME", home)
		t.Setenv("KUBE_CTX", "must-not-select")
		if err := execute(args, deps); err != nil {
			t.Fatal(err, diagnostics.String())
		}
		decoder := json.NewDecoder(&out)
		var result map[string]any
		if err := decoder.Decode(&result); err != nil {
			t.Fatal(err)
		}
		if result["operation"] != "cluster.discover" || result["kind"] != "kmx.result" || result["exitCode"] != float64(0) || result["seq"] != float64(1) || result["operationId"] == "" {
			t.Fatal(result)
		}
		if err := decoder.Decode(&result); err != io.EOF {
			t.Fatal("extra output", err)
		}
		if *loads != 0 || diagnostics.Len() != 0 {
			t.Fatal(*loads, diagnostics.String())
		}
		if _, err := os.Stat(home); !os.IsNotExist(err) {
			t.Fatal("state created", err)
		}
	}
}
func TestDiscoveryArgumentsHelpCompletionAndWindows(t *testing.T) {
	for _, tc := range []struct {
		args            []string
		goos            string
		success, framed bool
	}{
		{[]string{"cluster", "discover", "--help"}, "linux", true, false},
		{[]string{"__complete", "cluster", "discover", ""}, "linux", true, false},
		{[]string{"cluster", "discover", "-ojsonl", "--namespace", "dev"}, "linux", false, true},
		{[]string{"cluster", "discover", "-ojsonl", "--namespace="}, "linux", false, true},
		{[]string{"cluster", "discover", "-ojsonl", "--context=bad\nname"}, "linux", false, true},
		{[]string{"cluster", "discover", "-ojsonl", "operand"}, "linux", false, false},
		{[]string{"cluster", "discover", "-ojson"}, "linux", false, false},
		{[]string{"cluster", "discover", "-ojsonl"}, "windows", false, false},
	} {
		var out, diagnostics bytes.Buffer
		deps, loads := testDependencies(&out, &diagnostics)
		deps.goos = tc.goos
		err := execute(tc.args, deps)
		if (err == nil) != tc.success || *loads != 0 {
			t.Fatal(tc.args, err, *loads)
		}
		if tc.framed {
			r := decodeMachineRecord(t, out.Bytes())
			if r["exitCode"] != float64(1) || r["outcome"] != "failed" {
				t.Fatal(r)
			}
		}
	}
}
func TestVersionJSONDiscoveryScopes(t *testing.T) {
	for _, platform := range []string{"linux", "darwin", "windows"} {
		var out, diagnostics bytes.Buffer
		deps := versionTestDependencies(&out, &diagnostics)
		deps.goos = platform
		if err := execute([]string{"version", "-ojson"}, deps); err != nil {
			t.Fatal(err)
		}
		r := decodeMachineRecord(t, out.Bytes())
		scopes := []string{}
		for _, raw := range r["capabilities"].([]any) {
			c := raw.(map[string]any)
			if c["operation"] == "cluster.discover" {
				if c["output"] != "jsonl" || c["schemaVersion"] != float64(1) {
					t.Fatal(c)
				}
				scopes = append(scopes, c["scope"].(string))
			}
		}
		want := "contexts,namespaces,target"
		if platform == "windows" {
			want = ""
		}
		if strings.Join(scopes, ",") != want {
			t.Fatal(platform, scopes)
		}
	}
}

func TestDiscoverySignalResult(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix signal fixture")
	}
	if os.Getenv("KMX_DISCOVERY_SIGNAL_CHILD") == "1" {
		cmd := newClusterDiscoveryCommand(&commandState{contextFlag: "chosen"})
		cmd.SilenceUsage, cmd.SilenceErrors = true, true
		cmd.SetOut(os.Stdout)
		cmd.SetArgs([]string{"-ojsonl"})
		cmd.SetContext(t.Context())
		os.Exit(exitCode(cmd.Execute()))
	}
	discoveryCLIEnv(t, `touch "$KMX_DISCOVERY_READY"; exec sleep 30`)
	ready := filepath.Join(t.TempDir(), "ready")
	t.Setenv("KMX_DISCOVERY_READY", ready)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDiscoverySignalResult$")
	child.Env = append(os.Environ(), "KMX_DISCOVERY_SIGNAL_CHILD=1")
	var out, diagnostics bytes.Buffer
	child.Stdout, child.Stderr = &out, &diagnostics
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = child.Process.Kill()
			_ = child.Wait()
			t.Fatal("child not ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := child.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	err := child.Wait()
	var status *exec.ExitError
	if !errors.As(err, &status) || status.ExitCode() != 1 {
		t.Fatal(err, diagnostics.String())
	}
	result := decodeMachineRecord(t, out.Bytes())
	if result["outcome"] != "cancelled" || result["error"].(map[string]any)["code"] != "cancelled" {
		t.Fatal(result)
	}
}

type discoveryShortWriter struct{ calls int }

func (w *discoveryShortWriter) Write(p []byte) (int, error) { w.calls++; return 1, nil }
func TestDiscoveryJSONLTerminalAndOutputFailure(t *testing.T) {
	discoveryCLIEnv(t, `printf '""\n'`)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, parent := range []context.Context{ctx, func() context.Context {
		p, c := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
		t.Cleanup(c)
		return p
	}()} {
		var out bytes.Buffer
		cmd := newClusterDiscoveryCommand(&commandState{})
		cmd.SilenceUsage, cmd.SilenceErrors = true, true // match production root
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"-ojsonl"})
		cmd.SetContext(parent)
		err := cmd.Execute()
		if err == nil {
			t.Fatal("cancel succeeded")
		}
		r := decodeMachineRecord(t, out.Bytes())
		want := "cancelled"
		if errors.Is(parent.Err(), context.DeadlineExceeded) {
			want = "failed"
		}
		if r["outcome"] != want || r["exitCode"] != float64(1) {
			t.Fatal(r)
		}
	}
	w := &discoveryShortWriter{}
	cmd := newClusterDiscoveryCommand(&commandState{})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	cmd.SetOut(w)
	cmd.SetArgs([]string{"-ojsonl"})
	cmd.SetContext(t.Context())
	err := cmd.Execute()
	if err == nil || w.calls != 1 || err.Error() != "Cannot write machine output." {
		t.Fatal(err, w.calls)
	}
}
