package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/agent-primitives/sessions/api"
	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/harness/chatagent"
	"github.com/agent-primitives/sessions/placement"
	"github.com/agent-primitives/sessions/runtime/local"
	"github.com/agent-primitives/sessions/session"
	"github.com/agent-primitives/sessions/sqlitelog"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"google.golang.org/grpc"
)

func TestSuiteSessionsCLIExplicitDevelopmentAndNoClusterConfiguration(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("KMX_HOME", t.TempDir())
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := sqlitelog.Open(filepath.Join(dir, "journal.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	calls := new(atomic.Int64)
	backend := local.New(chatagent.Harness{Model: "fixture-model"})
	defer backend.Close()
	routes, err := placement.NewRegistry("chat", map[string]*placement.Placer{"chat": placement.New(backend, func(context.Context, api.ModelRequest) (api.ModelResponse, error) {
		calls.Add(1)
		return api.ModelResponse{Message: *api.TextMessage("assistant", "4")}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	v1.RegisterSessionsServer(server, session.NewService(store, routes))
	go server.Serve(listener)
	defer server.Stop()
	input := filepath.Join(dir, "instructions.md")
	if err := os.WriteFile(input, []byte("Answer four."), 0600); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "workspace")
	if err := workspace.Create(t.Context(), root, workspace.CreateOptions{Name: "greeter", Model: "fixture-model", InstructionsFile: input, Runtime: workspace.RuntimeProfile{ID: "agentsessions", Version: workspace.AgentSessionsVersion, Engine: "chat"}}); err != nil {
		t.Fatal(err)
	}
	host := map[string]any{"schemaVersion": 1, "address": listener.Addr().String(), "observation": map[string]any{
		"mode": "operator-observed-development", "hostID": "cli-host", "journalID": "cli-journal", "runtimeVersion": workspace.AgentSessionsVersion,
		"runtimeBinaryDigest": "sha256:" + strings.Repeat("a", 64), "runtimeConfigDigest": "sha256:" + strings.Repeat("b", 64), "inferenceID": "cli-model", "inferenceRevision": "sha256:" + strings.Repeat("c", 64), "model": "fixture-model", "harness": "chat", "harnessVersion": "1", "retention": "operator-managed",
	}}
	raw, err := json.Marshal(host)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "host.json")
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	suiteJudgeCase(t, root)
	judgeCalls := new(atomic.Int64)
	judge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		judgeCalls.Add(1)
		_, _ = w.Write([]byte(`{"model":"judge-model","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"{\"score\":90,\"reasoning\":\"private\"}"}}]}`))
	}))
	defer judge.Close()
	binding := filepath.Join(dir, "binding.yaml")
	bindingDoc := "apiVersion: kmx.kaimahi.dev/v1alpha1\nkind: AgentSessionsBinding\ntarget: {platform: kubernetes, id: demo}\nplacement: {backend: substrate}\natespace: demo\nsandbox: {class: microvm, configName: microvm}\nsnapshot: {fidelity: MEMORY, storageLocation: 'gs://snapshots/demo/'}\nresources: {cpu: '1', memory: 512Mi}\nmodel: {name: fixture-model, provider: openai, credential: {hostRef: {type: workload-identity, name: chat-model}}}\n"
	if err := os.WriteFile(binding, []byte(bindingDoc), 0600); err != nil {
		t.Fatal(err)
	}
	mismatch := filepath.Join(dir, "mismatch.yaml")
	if err := os.WriteFile(mismatch, []byte(strings.Replace(bindingDoc, "name: fixture-model", "name: other-model", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	var out, diagnostics bytes.Buffer
	deps, loads := testDependencies(&out, &diagnostics)
	base := []string{"eval", "--workspace", root, "--development-host", file}
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"missing bindings", []string{}, "requires --bindings FILE"},
		{"model mismatch", []string{"--bindings", mismatch}, `serves model "other-model" but the suite pins "fixture-model"`},
	} {
		denied := filepath.Join(dir, strings.ReplaceAll(tc.name, " ", "-")+".json")
		err := execute(append(append(append([]string{}, base...), tc.args...), "--out", denied), deps)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: %v, want %q", tc.name, err, tc.want)
		}
		if _, err := os.Stat(denied); !os.IsNotExist(err) || calls.Load() != 0 || judgeCalls.Load() != 0 || *loads != 0 {
			t.Fatal("binding refusal reached the host or wrote evidence", tc.name)
		}
	}
	args := append(append([]string{}, base...), "--bindings", binding, "--judge-base-url", judge.URL, "--out", filepath.Join(dir, "pass.json"))
	missingJudge := append(append([]string{}, base...), "--bindings", binding, "--out", filepath.Join(dir, "missing-judge.json"))
	if err := execute(missingJudge, deps); err == nil || !strings.Contains(err.Error(), "llmJudge assertions require --judge-base-url") || calls.Load() != 0 || judgeCalls.Load() != 0 {
		t.Fatal("judge preflight reached execution or wrong refusal", err)
	}
	diagnostics.Reset()
	if err := execute(args, deps); err != nil {
		t.Fatal(err)
	}
	if strings.Count(diagnostics.String(), legacyEvalNotice) != 1 {
		t.Fatalf("successful legacy evaluation notice: %q", &diagnostics)
	}
	if *loads != 0 || calls.Load() != 1 || judgeCalls.Load() != 2 || !strings.Contains(diagnostics.String(), "Development: unauthenticated operator-observed host") {
		t.Fatal("development used cluster configuration or wrong execution", *loads, calls.Load(), diagnostics.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "pass.json")); err != nil {
		t.Fatal("no complete CLI proof", err)
	}
	for _, flags := range [][]string{{"--to-context", "kind-other"}, {"--to-namespace", "orka-system"}, {"--inference", "provider:inference"}, {"--result-port", "19180"}, {"--context", "kind-other"}, {"--container-engine", "docker"}, {"--development-host", ""}} {
		negative := append([]string{}, args...)
		negative[len(negative)-1] = filepath.Join(dir, strings.TrimPrefix(flags[0], "--")+"-denied.json")
		want := "--development-host refuses " + flags[0]
		if flags[0] == "--context" {
			want = "--context cannot"
		}
		if flags[0] == "--development-host" {
			want = "non-empty --development-host"
		}
		if err := execute(append(negative, flags...), deps); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("wrong selector refusal %v: %v, want %q", flags, err, want)
		}
		if _, err := os.Stat(negative[len(negative)-1]); !os.IsNotExist(err) {
			t.Fatal("mode refusal published evidence", flags, err)
		}
		if *loads != 0 || calls.Load() != 1 {
			t.Fatal("mode refusal reached config/model", flags)
		}
	}
	for _, helper := range [][]string{{"eval", "--help"}, {"__complete", "eval", "--development-host", ""}} {
		if err := execute(helper, deps); err != nil {
			t.Fatal(err)
		}
		if *loads != 0 || calls.Load() != 1 {
			t.Fatal("help/completion performed operation")
		}
	}
	// Reuse the same real hermetic host/judge fixture for the canonical route;
	// it must produce fresh complete proof with no compatibility notice.
	canonical := append([]string{}, args...)
	canonical[0] = "test"
	canonical[len(canonical)-1] = filepath.Join(dir, "test-pass.json")
	out.Reset()
	diagnostics.Reset()
	if err := execute(canonical, deps); err != nil {
		t.Fatal(err)
	}
	if *loads != 0 || calls.Load() != 2 || judgeCalls.Load() != 4 || strings.Contains(diagnostics.String(), legacyEvalNotice) {
		t.Fatalf("canonical evaluation diverged: loads=%d calls=%d judges=%d stderr=%q", *loads, calls.Load(), judgeCalls.Load(), &diagnostics)
	}
	if _, err := os.Stat(canonical[len(canonical)-1]); err != nil {
		t.Fatal("no complete canonical CLI proof", err)
	}
}
