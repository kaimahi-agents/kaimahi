package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
)

func TestDestinationStatusRefusesBeforeConfiguration(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want []string
	}{
		{[]string{"status"}, []string{"kmx status --to <destination>", "kmx orka status"}},
		{[]string{"status", "--to="}, []string{"--to"}},
		{[]string{"status", "--to", " "}, []string{"--to"}},
		{[]string{"status", "--to", "prod", "-o", "yaml"}, []string{"table", "json"}},
		{[]string{"status", "--to", "prod", "-o", "text"}, []string{"table", "json"}},
		{[]string{"status", "--to", "prod", "-o", ""}, []string{"table", "json"}},
		{[]string{"status", "--to", "prod", "--workspace="}, []string{"--workspace"}},
		{[]string{"status", "--to", "prod", "extra"}, nil},
		{[]string{"status", "--to", "prod", "--namespace", "other"}, []string{"unknown flag"}},
		{[]string{"status", "--to", "prod", "--inference", "provider:other"}, []string{"unknown flag"}},
		{[]string{"status", "--to", "prod", "--bindings", "other.yaml"}, []string{"unknown flag"}},
		{[]string{"status", "--to", "prod", "--runtime", "orka"}, []string{"unknown flag"}},
	} {
		t.Run(strings.Join(tc.args, "_"), func(t *testing.T) {
			var out, diag bytes.Buffer
			deps, loads := testDependencies(&out, &diag)
			deps.loadConfig = func(string, string) (*config.Config, error) {
				*loads++
				return nil, errors.New("configuration must not load")
			}
			err := execute(tc.args, deps)
			if err == nil || *loads != 0 || out.Len() != 0 || diag.Len() != 0 {
				t.Fatalf("err=%v loads=%d stdout=%q stderr=%q", err, *loads, &out, &diag)
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q lacks %q", err, want)
				}
			}
		})
	}
}

func TestDestinationStatusSelectionBeforeConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, target, context, wantContext, wantError string
	}{
		{"saved", "{context: kind-saved, namespace: agents, runtime: orka}", "", "kind-saved", ""},
		{"global override", "{context: kind-saved, namespace: agents, runtime: kagent, bindings: missing.yaml}", "kind-explicit", "kind-explicit", ""},
		{"no namespace", "{context: kind-saved, runtime: kagent, bindings: missing.yaml}", "", "", "namespace"},
		{"agentsessions", "{context: kind-saved, namespace: agents, runtime: agentsessions}", "", "kind-saved", ""},
		{"no context", "{namespace: agents}", "", "", "kmx.yaml"},
		{"blank context", "{context: ' ', namespace: agents}", "", "", "kmx.yaml"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := destinationWorkspace(t, "version: 1\ntargets:\n  prod: "+tc.target+"\n")
			var out, diag bytes.Buffer
			deps, loads := testDependencies(&out, &diag)
			boundary := errors.New("configuration boundary")
			deps.loadConfig = func(ctx, _ string) (*config.Config, error) {
				*loads++
				if ctx != tc.wantContext {
					t.Fatalf("effective context=%q want %q", ctx, tc.wantContext)
				}
				return nil, boundary
			}
			args := []string{"status", "--workspace", root, "--to", "prod"}
			if tc.context != "" {
				args = append(args, "--context", tc.context)
			}
			err := execute(args, deps)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) || *loads != 0 {
					t.Fatalf("err=%v loads=%d", err, *loads)
				}
			} else if !errors.Is(err, boundary) || *loads != 1 {
				t.Fatalf("err=%v loads=%d", err, *loads)
			}
			if out.Len() != 0 || diag.Len() != 0 {
				t.Fatalf("selection printed unrelated report: stdout=%q stderr=%q", &out, &diag)
			}
		})
	}
}

func TestDestinationStatusFreezesNamedSelectionWithoutLoadingContent(t *testing.T) {
	for _, tc := range []struct {
		name, selectors, context, output string
		wantFailure                      bool
	}{
		{"saved inference", "runtime: orka, inference: 'provider:missing'", "", "json", false},
		{"global override and unreadable bindings", "bindings: missing.yaml", "kind-explicit", "json", false},
		{"table", "runtime: orka", "", "table", false},
		{"runtime expectation mismatch", "runtime: kagent, bindings: missing.yaml", "", "json", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var registryRequests atomic.Int32
			registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				registryRequests.Add(1)
				w.WriteHeader(http.StatusInternalServerError)
			}))
			defer registry.Close()
			host := strings.TrimPrefix(registry.URL, "http://")
			root := destinationWorkspace(t, "version: 1\npublishRepository: '"+host+"/agents'\nregistries: {'"+host+"': {plainHTTP: true}}\ntargets:\n  prod: {context: kind-saved, namespace: agents, "+tc.selectors+"}\n")
			// The descriptor points at nonexistent member/build-profile files.
			// Inspection must not load those or the missing binding/Provider.
			tools := t.TempDir()
			log := filepath.Join(tools, "calls")
			home := filepath.Join(t.TempDir(), "absent-home")
			t.Setenv("KMX_HOME", home)
			t.Setenv("KUBE_CONTEXT", "environment-must-not-win")
			t.Setenv("KMX_STATUS_LOG", log)
			wantContext := "kind-saved"
			if tc.context != "" {
				wantContext = tc.context
			}
			t.Setenv("KMX_STATUS_CONTEXT", wantContext)
			script := `#!/bin/sh
printf '%s\n' "$*" >> "$KMX_STATUS_LOG"
[ "$#" = 6 ] && [ "$1" = --context ] && [ "$2" = "$KMX_STATUS_CONTEXT" ] && [ "$3" = --request-timeout=10s ] && [ "$4" = get ] && [ "$5" = --raw ] || exit 91
case "$6" in
/apis) printf '%s' '{"apiVersion":"v1","kind":"APIGroupList","groups":[{"name":"core.orka.ai","versions":[{"groupVersion":"core.orka.ai/v1alpha1","version":"v1alpha1"}]}]}' ;;
/apis/core.orka.ai/v1alpha1) printf '%s' '{"apiVersion":"v1","kind":"APIResourceList","groupVersion":"core.orka.ai/v1alpha1","resources":[{"name":"agents","kind":"Agent","namespaced":true,"verbs":["get","list"]}]}' ;;
/api/v1/namespaces/kube-system) printf '%s' '{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"kube-system","uid":"cluster-uid"}}' ;;
/api/v1/namespaces/agents) printf '%s' '{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"agents","uid":"namespace-uid"}}' ;;
'/apis/core.orka.ai/v1alpha1/namespaces/agents/agents?limit=128') printf '%s' '{"apiVersion":"core.orka.ai/v1alpha1","kind":"AgentList","metadata":{"resourceVersion":"10","continue":""},"items":[]}' ;;
*) exit 92 ;;
esac
`
			if err := os.WriteFile(filepath.Join(tools, "kubectl"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", tools)
			var out, diag bytes.Buffer
			deps, loads := testDependencies(&out, &diag)
			deps.loadConfig = func(ctx, _ string) (*config.Config, error) {
				*loads++
				if ctx != wantContext {
					t.Fatalf("configuration received context %q, want %q", ctx, wantContext)
				}
				// A second selection would now fail or redirect the observation.
				if err := os.WriteFile(filepath.Join(root, "kmx.yaml"), []byte("version: 1\ntargets: {prod: {context: redirected, namespace: other, runtime: agentsessions}}\n"), 0600); err != nil {
					t.Fatal(err)
				}
				return &config.Config{KubeContext: ctx}, nil
			}
			var configured *app.App
			deps.newApp = func(cfg *config.Config) *app.App {
				configured = app.New(cfg)
				return configured
			}
			args := []string{"status", "--to", "prod", "--workspace", root, "-o", tc.output}
			if tc.context != "" {
				args = append(args, "--context", tc.context)
			}
			err := execute(args, deps)
			if (err != nil) != tc.wantFailure || *loads != 1 || registryRequests.Load() != 0 {
				t.Fatalf("err=%v loads=%d registry requests=%d stdout=%q stderr=%q", err, *loads, registryRequests.Load(), &out, &diag)
			}
			if tc.wantFailure && exitCode(err) != 1 {
				t.Fatalf("observed runtime mismatch mapped to exit %d, want 1", exitCode(err))
			}
			contextSource := configured.Cfg.ContextSource
			wantSource := "kmx.yaml (--to prod)"
			if tc.context != "" {
				wantSource = config.SourceFlag
			}
			if contextSource != wantSource {
				t.Fatalf("context source=%q want %q", contextSource, wantSource)
			}
			if tc.output == "json" {
				var report struct {
					Destination string
					Target      struct{ Context, Namespace string }
					Complete    bool
				}
				decoder := json.NewDecoder(&out)
				if err := decoder.Decode(&report); err != nil {
					t.Fatal(err)
				}
				if report.Destination != "prod" || report.Target.Context != wantContext || report.Target.Namespace != "agents" || report.Complete == tc.wantFailure {
					t.Fatalf("unfrozen report: %+v", report)
				}
				var extra any
				if err := decoder.Decode(&extra); err != io.EOF {
					t.Fatalf("stdout is not one JSON document: %v", err)
				}
			} else if !strings.Contains(out.String(), "prod") || !strings.Contains(out.String(), "agents") {
				t.Fatalf("table lacks destination: %s", &out)
			}
			if _, err := os.Stat(home); !os.IsNotExist(err) {
				t.Fatalf("inspection touched application state: %v", err)
			}
			calls, err := os.ReadFile(log)
			if err != nil || len(calls) == 0 || strings.Contains(string(calls), "redirected") {
				t.Fatalf("missing or redirected GET observations: %q %v", calls, err)
			}
			for _, irrelevant := range []string{"inference=", "bindings=", "registry="} {
				if strings.Contains(diag.String(), irrelevant) {
					t.Fatalf("irrelevant destination selectors printed: %s", &diag)
				}
			}
		})
	}
}

func TestDestinationStatusExitCodes(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want int
	}{
		{app.ErrDestinationStatusPending, 2},
		{fmt.Errorf("wrapped: %w", app.ErrDestinationStatusPending), 2},
		{errors.New("destination status failed"), 1},
	} {
		if got := exitCode(tc.err); got != tc.want {
			t.Errorf("exitCode(%v)=%d want %d", tc.err, got, tc.want)
		}
	}
}

func TestDestinationStatusHelpAndCompletionAreInert(t *testing.T) {
	root := destinationWorkspace(t, "malformed: [\n")
	t.Chdir(root)
	t.Setenv("PATH", t.TempDir())
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"status", "--help"}, "--to"},
		{[]string{"__complete", "status", "--output", ""}, "json\ntable\n:4\n"},
		{[]string{"__complete", "status", "--to", ""}, ":4\n"},
		{[]string{"__complete", "status", "--workspace", ""}, ":4\n"},
	} {
		var out, diag bytes.Buffer
		deps, loads := testDependencies(&out, &diag)
		if err := execute(tc.args, deps); err != nil || *loads != 0 || !strings.Contains(out.String(), tc.want) {
			t.Fatalf("%v: err=%v loads=%d stdout=%q", tc.args, err, *loads, &out)
		}
		if tc.args[0] == "status" {
			for _, want := range []string{"destination", "--workspace", "table|json", "orka status"} {
				if !strings.Contains(out.String(), want) {
					t.Errorf("help lacks %q: %s", want, &out)
				}
			}
		}
	}
	if _, err := os.Stat(filepath.Join(root, "agents")); !os.IsNotExist(err) {
		t.Fatalf("help/completion materialized source: %v", err)
	}
}
