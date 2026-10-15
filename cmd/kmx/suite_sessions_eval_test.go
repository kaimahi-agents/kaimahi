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
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aramase/agentsessions/api"
	v1 "github.com/aramase/agentsessions/api/genpb"
	"github.com/aramase/agentsessions/harness/chatagent"
	"github.com/aramase/agentsessions/placement"
	"github.com/aramase/agentsessions/runtime/local"
	"github.com/aramase/agentsessions/session"
	"github.com/aramase/agentsessions/sqlitelog"
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
	bindingDoc := "apiVersion: kmx.kaimahi.dev/v1alpha1\nkind: AgentSessionsBinding\ntarget: {platform: kubernetes, id: demo}\nplacement: {backend: substrate}\natespace: demo\nsandbox: {class: microvm, configName: microvm}\nsnapshot: {fidelity: MEMORY, storageLocation: 'gs://snapshots/demo/'}\nresources: {cpu: '1', memory: 512Mi}\nmodel: {name: fixture-model, provider: openai, secretRef: {name: model-credentials, key: api-key}}\n"
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
	if err := execute(args, deps); err != nil {
		t.Fatal(err)
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
	// Plan-only AgentSessions lift from the evaluated package and evidence.
	layout := filepath.Join(t.TempDir(), "layout")
	diagnostics.Reset()
	if err := execute([]string{"push", "--workspace", root, "--evaluation", filepath.Join(dir, "pass.json"), "--to-layout", layout, "suite:v1"}, deps); err != nil {
		t.Fatal(err)
	}
	digest := regexp.MustCompile(`sha256:[0-9a-f]{64}`).FindString(diagnostics.String() + out.String())
	if digest == "" {
		t.Fatalf("push did not report its digest: %s", diagnostics.String())
	}
	image := "registry.example.invalid/harnessnode@sha256:" + strings.Repeat("e", 64)
	lift := []string{"lift", "suite@" + digest, "--from-layout", layout, "--harness-image", image, "--bindings", binding, "--evidence", filepath.Join(dir, "pass.json")}
	for _, tc := range []struct {
		extra []string
		want  string
	}{
		{nil, "plan-only; pass --plan"},
		{[]string{"--plan", "--to-context", "kind-other"}, "--harness-image refuses --to-context"},
		{[]string{"--plan", "--inference", "provider:inference"}, "--harness-image refuses --inference"},
		{[]string{"--plan", "--promote"}, "--harness-image refuses --promote"},
		{[]string{"--plan", "--harness-image", "registry.example.invalid/harnessnode:v1"}, "--harness-image: harness image must be a digest-pinned reference"},
		{[]string{"--plan", "--bindings", mismatch}, `serves model "other-model" but the suite pins "fixture-model"`},
		{[]string{"--plan", "--case-timeout", "1m"}, "explicit-reference lift refuses --case-timeout"},
		{[]string{"--plan", "--result-port", "19180"}, "explicit-reference lift refuses --result-port"},
		{[]string{"--plan", "--judge-base-url", "http://127.0.0.1:1"}, "explicit-reference lift refuses --judge-base-url"},
		{[]string{"--plan", "--judge-key-env", "JUDGE_KEY"}, "explicit-reference lift refuses --judge-key-env"},
		{[]string{"--plan", "--provenance=false"}, "explicit-reference lift refuses --provenance"},
		{[]string{"--plan", "--require-provenance"}, "explicit-reference lift refuses --require-provenance"},
	} {
		if err := execute(append(append([]string{}, lift...), tc.extra...), deps); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("lift %v: %v, want %q", tc.extra, err, tc.want)
		}
	}
	noBindings := []string{"lift", "suite@" + digest, "--from-layout", layout, "--harness-image", image, "--evidence", filepath.Join(dir, "pass.json"), "--plan"}
	if err := execute(noBindings, deps); err == nil || !strings.Contains(err.Error(), "requires --bindings FILE") {
		t.Fatalf("lift without bindings: %v", err)
	}
	noEvidence := []string{"lift", "suite@" + digest, "--from-layout", layout, "--harness-image", image, "--bindings", binding, "--plan"}
	if err := execute(noEvidence, deps); err == nil || !strings.Contains(err.Error(), "explicit-reference lift requires --evidence FILE") {
		t.Fatalf("lift without evidence: %v", err)
	}
	// Workspace Lift composes Orka only: it refuses --harness-image before
	// reading arguments, and refuses an AgentSessions workspace before any
	// evaluation, publication or cluster configuration.
	if err := os.WriteFile(filepath.Join(root, "kmx.yaml"), []byte(composedDestinationConfig), 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"lift", "--to", "prod", "--harness-image", image, "--plan"}, "workspace lift refuses --harness-image"},
		{[]string{"lift", "--to", "prod", "--plan"}, "native Orka only"},
		{[]string{"lift", "--to", "prod"}, "native Orka only"},
	} {
		if err := execute(tc.args, deps); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("workspace %v: %v, want %q", tc.args, err, tc.want)
		}
	}
	out.Reset()
	if err := execute(append(append([]string{}, lift...), "--plan", "-o", "json"), deps); err != nil {
		t.Fatal(err)
	}
	var plan struct {
		Runtime       string `json:"runtime"`
		Gate          string `json:"gate"`
		AgentSessions struct {
			HarnessImage  string `json:"harnessImage"`
			ActorTemplate struct {
				Metadata struct{ Atespace, Name string }
			} `json:"actorTemplate"`
			HarnessRegistration json.RawMessage `json:"harnessRegistration"`
		} `json:"agentsessions"`
	}
	if err := json.Unmarshal(out.Bytes(), &plan); err != nil {
		t.Fatalf("lift plan is not JSON: %v\n%s", err, out.String())
	}
	if plan.Runtime != "agentsessions" || plan.Gate != "plan-only" || plan.AgentSessions.HarnessImage != image || plan.AgentSessions.ActorTemplate.Metadata.Atespace != "demo" || !strings.Contains(string(plan.AgentSessions.HarnessRegistration), plan.AgentSessions.ActorTemplate.Metadata.Name) {
		t.Fatalf("unexpected lift plan: %s", out.String())
	}
	if *loads != 0 || calls.Load() != 1 {
		t.Fatal("AgentSessions lift plan loaded cluster configuration or reached the model")
	}
	for _, helper := range [][]string{{"eval", "--help"}, {"__complete", "eval", "--development-host", ""}, {"lift", "--help"}} {
		if err := execute(helper, deps); err != nil {
			t.Fatal(err)
		}
		if *loads != 0 || calls.Load() != 1 {
			t.Fatal("help/completion performed operation")
		}
	}
}
