package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
)

func destinationWorkspace(t *testing.T, raw string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "workspace.yaml"), []byte("schemaVersion: 1\nname: demo\nmember: agents/demo/member.yaml\nbuildProfile: build-profiles/default.json\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if raw != "" {
		if err := os.WriteFile(filepath.Join(root, "kmx.yaml"), []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}
func TestDestinationsCheckBoundariesAndIndependentFailures(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusUnauthorized, http.StatusFound} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Method != "GET" || r.URL.Path != "/v2/" || r.Header.Get("Authorization") != "" {
					t.Errorf("unsafe check: %s %s auth=%q", r.Method, r.URL, r.Header.Get("Authorization"))
				}
				w.Header().Set("Location", "/redirect")
				w.WriteHeader(status)
			}))
			defer server.Close()
			host := strings.TrimPrefix(server.URL, "http://")
			root := destinationWorkspace(t, "version: 1\npublishRepository: '"+host+"/agents'\ntargets:\n  dev: {context: kind-dev}\n  prod: {context: 'remote team'}\nregistries:\n  '"+host+"': {plainHTTP: true}\n")
			tools := t.TempDir()
			log := filepath.Join(tools, "calls")
			t.Setenv("KMX_CTX_LOG", log)
			if err := os.WriteFile(filepath.Join(tools, "kubectl"), []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" > \"$KMX_CTX_LOG\"\nprintf 'kind-dev\\nremote team\\n'\n"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", tools)
			t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
			t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
			t.Setenv("NO_PROXY", "")
			var out, diagnostics bytes.Buffer
			deps, loads := testDependencies(&out, &diagnostics)
			err := execute([]string{"package", "ctx", "--workspace", root, "--check", "-o", "json"}, deps)
			if (err != nil) != (status != 200) || *loads != 0 {
				t.Fatalf("check status: %v loads=%d", err, *loads)
			}
			var rows []struct{ Name, ContextCheck, RegistryCheck, RuntimeCheck string }
			if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
				t.Fatal(err)
			}
			wantRegistry := map[int]string{200: "reachable", 401: "auth required", 302: "refused"}[status]
			if len(rows) != 2 || rows[0].ContextCheck != "present" || rows[1].ContextCheck != "present" || rows[0].RegistryCheck != wantRegistry || rows[1].RuntimeCheck != "not checked" {
				t.Fatalf("lost/incorrect independent rows: %+v", rows)
			}
			if requests.Load() != 1 {
				t.Fatalf("redirect/repeated request: %d", requests.Load())
			}
			raw, err := os.ReadFile(log)
			if err != nil || string(raw) != "config view -o jsonpath={range .contexts[*]}{.name}{\"\\n\"}{end}\n" {
				t.Fatalf("unsafe kubectl invocation: %q %v", raw, err)
			}
		})
	}
}
func TestDestinationsCheckUnknownSanitizesToolFailure(t *testing.T) {
	root := destinationWorkspace(t, "version: 1\ntargets: {dev: {context: kind-dev}, prod: {context: aks-prod}}\n")
	tools := t.TempDir()
	os.WriteFile(filepath.Join(tools, "kubectl"), []byte("#!/bin/sh\nprintf 'PRIVATE_TOOL_DIAGNOSTIC' >&2\nexit 1\n"), 0700)
	t.Setenv("PATH", tools)
	var out bytes.Buffer
	deps, loads := testDependencies(&out, &out)
	err := execute([]string{"package", "ctx", "--workspace", root, "--check", "-o", "json"}, deps)
	if err == nil || *loads != 0 || strings.Contains(out.String()+err.Error(), "PRIVATE_TOOL") || strings.Count(out.String(), `"contextCheck":"unknown"`) != 2 {
		t.Fatalf("unknown handling: %v %s", err, &out)
	}
}

func TestDestinationsListOfflineAndAlphabetical(t *testing.T) {
	root := destinationWorkspace(t, `version: 1
publishRepository: registry.example/agents
targets:
  prod: {context: aks-prod, namespace: agents, runtime: orka, inference: 'provider:inference'}
  dev: {context: kind-dev, bindings: does-not-exist.yaml}
`)
	t.Setenv("PATH", t.TempDir())
	t.Setenv("KMX_HOME", t.TempDir())
	for _, format := range []string{"table", "json"} {
		var out, diagnostics bytes.Buffer
		deps, loads := testDependencies(&out, &diagnostics)
		err := execute([]string{"package", "ctx", "--workspace", root, "-o", format}, deps)
		if err != nil || *loads != 0 {
			t.Fatalf("offline listing: %v loads=%d", err, *loads)
		}
		if format == "json" {
			var rows []struct{ Name, Context, Repository, RuntimeCheck, InferenceCheck, BindingsCheck string }
			if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
				t.Fatal(err)
			}
			if len(rows) != 2 || rows[0].Name != "dev" || rows[1].Name != "prod" || rows[1].Context != "aks-prod" || rows[0].Repository != "registry.example/agents" || rows[0].RuntimeCheck != "not checked" || rows[0].BindingsCheck != "not checked" {
				t.Fatalf("wrong rows: %+v", rows)
			}
		} else if strings.Index(out.String(), "dev") >= strings.Index(out.String(), "prod") || !strings.Contains(out.String(), "undeclared") {
			t.Fatalf("wrong table: %s", &out)
		}
		if selected, err := config.ReadSelectedContext(); err != nil || selected != "" {
			t.Fatalf("listing selected state: %q %v", selected, err)
		}
	}
}
func TestDestinationsHelpCompletionAndLegacySyntaxAreInert(t *testing.T) {
	root := destinationWorkspace(t, "malformed: [\n")
	t.Chdir(root)
	t.Setenv("PATH", t.TempDir())
	for _, args := range [][]string{{"package", "ctx", "--help"}, {"help", "package", "ctx"}, {"__complete", "package", "ctx", ""}, {"__complete", "eval", "--to", ""}, {"__complete", "lift", "--to", ""}} {
		var out bytes.Buffer
		deps, loads := testDependencies(&out, &out)
		if err := execute(args, deps); err != nil || *loads != 0 {
			t.Fatalf("%v: %v loads=%d", args, err, *loads)
		}
	}
	for _, args := range [][]string{{"package", "ctx", "prod"}, {"package", "ctx", "use", "prod"}} {
		var out bytes.Buffer
		deps, loads := testDependencies(&out, &out)
		if err := execute(args, deps); err == nil || *loads != 0 || strings.Contains(err.Error(), "kmx.yaml") {
			t.Fatalf("legacy syntax read config: %v loads=%d", err, *loads)
		}
	}
}
