package main

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"

	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
)

func TestWorkspacePushSavedExactHostHTTPAndExplicitFalse(t *testing.T) {
	for _, mode := range []string{"saved", "explicit false", "other host", "layout"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("KMX_HOME", t.TempDir())
			root, _ := suiteEvalSource(t)
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(http.StatusTeapot) }))
			defer server.Close()
			host := strings.TrimPrefix(server.URL, "http://")
			policyHost := host
			if mode == "other host" {
				policyHost = "127.0.0.1:1"
			}
			os.WriteFile(filepath.Join(root, "kmx.yaml"), []byte(fmt.Sprintf("version: 1\nregistries:\n  '%s': {plainHTTP: true}\n", policyHost)), 0600)
			args := []string{"push", "--workspace", root, host + "/agents:v1", "--provenance=false"}
			if mode == "explicit false" {
				args = append(args, "--plain-http=false")
			}
			if mode == "layout" {
				os.WriteFile(filepath.Join(root, "kmx.yaml"), []byte("malformed: [\n"), 0600)
				args = []string{"push", "--workspace", root, "agents:v1", "--to-layout", filepath.Join(t.TempDir(), "layout")}
			}
			var out, diag bytes.Buffer
			deps, loads := testDependencies(&out, &diag)
			err := execute(args, deps)
			want := int32(0)
			if mode == "saved" {
				want = 1
			}
			if requests.Load() != want || *loads != 0 || (mode == "layout") != (err == nil) {
				t.Fatalf("mode %s: requests=%d err=%v loads=%d", mode, requests.Load(), err, *loads)
			}
		})
	}
}

func TestOperationalConfigOnlyEditsPreserveSourceAndSuiteIdentity(t *testing.T) {
	root, _ := suiteEvalSource(t)
	cache := filepath.Join(t.TempDir(), "cache")
	before, err := workspace.Prepare(t.Context(), root, cache)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "kmx.yaml"), []byte("version: 1\ntargets: {prod: {context: prod}}\n"), 0600)
	after, err := workspace.Prepare(t.Context(), root, cache)
	if err != nil {
		t.Fatal(err)
	}
	if before.Fingerprint != after.Fingerprint || before.Descriptor.Digest != after.Descriptor.Digest {
		t.Fatal("operational config changed packaged identity")
	}
	os.WriteFile(filepath.Join(root, "kmx.yaml"), []byte("version: 1\ntargets: {prod: {context: changed}}\n"), 0600)
	if err := workspace.CheckCurrent(t.Context(), root, before); err != nil {
		t.Fatalf("operational edit treated as source drift: %v", err)
	}
}

func TestNamedEvaluationResolvedContextAndExplicitOverrides(t *testing.T) {
	for _, name := range []string{"named", "evalTarget", "explicit overrides", "explicit context bypass", "missing target", "runtime mismatch", "changed config"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("KMX_HOME", t.TempDir())
			root, outfile := suiteEvalSource(t)
			t.Chdir(root)
			raw := "version: 1\nevalTarget: dev\npublishRepository: registry.example/agents\ntargets:\n  dev: {context: kind-saved, namespace: agents, runtime: orka, inference: 'provider:saved'}\n"
			path := filepath.Join(root, "kmx.yaml")
			if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			args := []string{"eval", "--out", outfile}
			wantCtx := "kind-saved"
			wantLoads := 1
			switch name {
			case "named", "changed config":
				args = append(args, "--to", "dev")
			case "explicit overrides":
				args = append(args, "--to", "dev", "--to-context", "kind-explicit", "--to-namespace", "explicit", "--inference", "provider:explicit")
				wantCtx = "kind-explicit"
			case "explicit context bypass":
				os.WriteFile(path, []byte("malformed: [\n"), 0600)
				args = append(args, "--to-context", "kind-explicit", "--inference", "provider:explicit")
				wantCtx = "kind-explicit"
			case "missing target":
				args = append(args, "--to", "missing")
				wantLoads = 0
			case "runtime mismatch":
				os.WriteFile(path, []byte("version: 1\nevalTarget: dev\ntargets: {dev: {context: kind-saved, runtime: kagent}}\n"), 0600)
				args = append(args, "--inference", "provider:saved")
				wantLoads = 0
			}
			var out, diag bytes.Buffer
			deps, loads := testDependencies(&out, &diag)
			boundary := errors.New("resolved configured boundary")
			deps.loadConfig = func(ctx, engine string) (*config.Config, error) {
				*loads++
				if ctx != wantCtx {
					t.Fatalf("configured unresolved context: %q want %q", ctx, wantCtx)
				}
				if name == "changed config" {
					os.WriteFile(path, []byte("version: 1\ntargets: {dev: {context: redirected, inference: 'provider:redirected'}}\n"), 0600)
					return nil, boundary
				}
				return nil, boundary
			}
			deps.newApp = func(*config.Config) *app.App {
				t.Fatal("captured configuration boundary constructed an App")
				return nil
			}
			err := execute(args, deps)
			if err == nil || *loads != wantLoads {
				t.Fatalf("%s err=%v loads=%d diag=%s", name, err, *loads, &diag)
			}
			if wantLoads == 1 && !errors.Is(err, boundary) {
				t.Fatalf("wrong boundary: %v", err)
			}
			if name == "changed config" && (strings.Contains(diag.String(), "redirected") || strings.Count(diag.String(), "Destination:") != 1 || !strings.Contains(diag.String(), "kind-saved") || !strings.Contains(diag.String(), `namespace="agents"; expected-runtime="orka"; inference="provider:saved"; bindings=""; registry="registry.example/agents"`)) {
				t.Fatalf("config reread/duplicate display: %s", &diag)
			}
		})
	}
}

func TestNamedKagentEvaluationUnionReplacementAndNoOrkaDefault(t *testing.T) {
	for _, mode := range []string{"saved bindings", "replace inference", "explicit both", "namespace mismatch"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("KMX_HOME", t.TempDir())
			base, root, binding := suiteKagentCLIArgs(t, ".yaml")
			t.Chdir(root)
			raw := "version: 1\ntargets:\n  dev: {context: kind-saved, runtime: kagent, bindings: '" + binding + "'}\n"
			args := []string{"eval", "--workspace", root, "--to", "dev", "--out", base[len(base)-1]}
			if mode == "replace inference" {
				raw = "version: 1\ntargets:\n  dev: {context: kind-saved, inference: 'provider:saved'}\n"
				args = append(args, "--bindings", binding)
			}
			if mode == "explicit both" {
				args = append(args, "--inference", "provider:explicit", "--bindings", binding)
			}
			if mode == "namespace mismatch" {
				args = append(args, "--to-namespace", "other")
			}
			os.WriteFile(filepath.Join(root, "kmx.yaml"), []byte(raw), 0600)
			var out bytes.Buffer
			deps, loads := testDependencies(&out, &out)
			boundary := errors.New("kagent resolved boundary")
			deps.loadConfig = func(ctx, engine string) (*config.Config, error) {
				*loads++
				if ctx != "kind-saved" {
					t.Fatalf("wrong context %q", ctx)
				}
				return nil, boundary
			}
			err := execute(args, deps)
			wantLoads := 1
			if mode == "explicit both" || mode == "namespace mismatch" {
				wantLoads = 0
			}
			if err == nil || *loads != wantLoads || wantLoads == 1 && !errors.Is(err, boundary) {
				t.Fatalf("mode %s: err=%v loads=%d", mode, err, *loads)
			}
		})
	}
}

func TestNamedLiftWorkspaceErrorsGiveSupportedRecoveryAndPreserveCause(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		t.Run(fmt.Sprint(invalid), func(t *testing.T) {
			root := t.TempDir()
			t.Chdir(root)
			if invalid {
				if err := os.WriteFile(filepath.Join(root, "workspace.yaml"), []byte("malformed: [\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			var out bytes.Buffer
			deps, loads := testDependencies(&out, &out)
			err := execute([]string{"lift", "registry.example/suite@sha256:" + strings.Repeat("a", 64), "--to", "prod", "--evidence", "private.json", "--plan"}, deps)
			if err == nil || *loads != 0 || strings.Contains(err.Error(), "--workspace") {
				t.Fatalf("unsupported recovery or configuration loaded: err=%v loads=%d", err, *loads)
			}
			if invalid && !strings.Contains(err.Error(), "workspace.yaml") {
				t.Fatalf("lost descriptor failure: %v", err)
			}
		})
	}
}

func TestExplicitReferenceLiftWithoutNamedSelectorIgnoresWorkspaceConfig(t *testing.T) {
	root := destinationWorkspace(t, "invalid: [\n")
	os.WriteFile(filepath.Join(root, "workspace.yaml"), []byte("invalid: [\n"), 0600)
	t.Chdir(root)
	var out bytes.Buffer
	deps, loads := testDependencies(&out, &out)
	boundary := errors.New("source free configured boundary")
	deps.loadConfig = func(ctx, engine string) (*config.Config, error) {
		*loads++
		if ctx != "kind-explicit" {
			t.Fatal(ctx)
		}
		return nil, boundary
	}
	err := execute([]string{"lift", "registry.example/suite@sha256:" + strings.Repeat("a", 64), "--to-context", "kind-explicit", "--inference", "provider:inference", "--evidence", "private.json", "--plan"}, deps)
	if !errors.Is(err, boundary) || *loads != 1 {
		t.Fatalf("source-free lift discovered local config: %v loads=%d", err, *loads)
	}
}
func TestNamedLiftExplicitContextOverridesAndBindingsReplaceInference(t *testing.T) {
	root := destinationWorkspace(t, "version: 1\ntargets: {prod: {context: aks-saved, inference: 'provider:saved'}}\n")
	t.Chdir(root)
	var out bytes.Buffer
	deps, loads := testDependencies(&out, &out)
	boundary := errors.New("named lift configured boundary")
	deps.loadConfig = func(ctx, engine string) (*config.Config, error) {
		*loads++
		if ctx != "kind-explicit" {
			t.Fatal(ctx)
		}
		return nil, boundary
	}
	err := execute([]string{"lift", "registry.example/suite@sha256:" + strings.Repeat("a", 64), "--to", "prod", "--to-context", "kind-explicit", "--bindings", "author.yaml", "--evidence", "private.json", "--plan"}, deps)
	if !errors.Is(err, boundary) || *loads != 1 {
		t.Fatalf("named Lift selection: %v loads=%d", err, *loads)
	}
}
