package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aramase/agentsessions/api"
	v1 "github.com/aramase/agentsessions/api/genpb"
	"github.com/aramase/agentsessions/harness/chatagent"
	"github.com/aramase/agentsessions/placement"
	"github.com/aramase/agentsessions/runtime/local"
	"github.com/aramase/agentsessions/session"
	"github.com/aramase/agentsessions/sqlitelog"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsessions"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// A real pinned Sessions service and chat harness exercise config/journal semantics;
// the deterministic host-owned model is an instruction-sensitive test fixture.
func suiteSessionsEvaluationFixture(t *testing.T) (*App, SuiteWorkspaceEvaluationOptions, *atomic.Int64) {
	t.Helper()
	t.Setenv("KMX_HOME", privateSuiteDir(t))
	dir := privateSuiteDir(t)
	store, err := sqlitelog.Open(filepath.Join(dir, "journal.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	calls := new(atomic.Int64)
	backend := local.New(chatagent.Harness{Model: "fixture-model"})
	t.Cleanup(func() { backend.Close() })
	placer := placement.New(backend, func(_ context.Context, req api.ModelRequest) (api.ModelResponse, error) {
		calls.Add(1)
		answer := "5"
		if len(req.Messages) == 2 && req.Messages[0].Text() == "Answer only four." {
			answer = "4"
		}
		return api.ModelResponse{Message: *api.TextMessage("assistant", answer)}, nil
	})
	routes, err := placement.NewRegistry("chat", map[string]*placement.Placer{"chat": placer})
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
	t.Cleanup(server.Stop)
	root := filepath.Join(t.TempDir(), "workspace")
	input := filepath.Join(t.TempDir(), "instructions.md")
	writeFile(t, input, "Answer only four.")
	if err := workspace.Create(t.Context(), root, workspace.CreateOptions{Name: "greeter", Model: "fixture-model", InstructionsFile: input, Runtime: workspace.RuntimeProfile{ID: "agentsessions", Version: workspace.AgentSessionsVersion, Engine: "chat"}}); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "eval/example.yaml"), "id: first\ninput: Say four\nexpectContains: ['4']\n")
	writeFile(t, filepath.Join(root, "eval/second.yml"), "id: second\ninput: Say four again\nexpectContains: ['4']\n")
	host := map[string]any{"schemaVersion": 1, "address": listener.Addr().String(), "observation": map[string]any{
		"mode": "operator-observed-development", "hostID": "owned-host", "journalID": "owned-journal", "runtimeVersion": workspace.AgentSessionsVersion,
		"runtimeBinaryDigest": "sha256:" + strings.Repeat("a", 64), "runtimeConfigDigest": "sha256:" + strings.Repeat("b", 64), "inferenceID": "owned-inference", "inferenceRevision": "sha256:" + strings.Repeat("c", 64),
		"model": "fixture-model", "harness": "chat", "harnessVersion": "1", "retention": "operator-managed",
	}}
	data, err := json.Marshal(host)
	if err != nil {
		t.Fatal(err)
	}
	hostPath := filepath.Join(dir, "host.json")
	if err := os.WriteFile(hostPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	opt := SuiteWorkspaceEvaluationOptions{Workspace: root, DevelopmentHost: hostPath, Out: filepath.Join(privateSuiteDir(t), "pass.json"), CaseTimeout: 30 * time.Second}
	return &App{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}, opt, calls
}

func TestSuiteSessionsEvaluationFullPassAndExactPush(t *testing.T) {
	a, opt, calls := suiteSessionsEvaluationFixture(t)
	t.Setenv("PATH", t.TempDir())
	p, err := PrepareSuiteWorkspaceEvaluation(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err := a.EvaluateSuiteWorkspace(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatal("full inventory did not execute once", calls.Load())
	}
	r, err := readSuitePassingReceipt(t.Context(), opt.Out)
	if err != nil || r.Sessions == nil || len(r.Cases) != 2 {
		t.Fatal("missing complete runtime proof", err)
	}
	if r.Cases[0].SessionsExecution.SessionUID == r.Cases[1].SessionsExecution.SessionUID {
		t.Fatal("reused Session")
	}
	if !strings.Contains(a.Err.(*bytes.Buffer).String(), "Development: unauthenticated operator-observed host; not authenticated remote promotion.") {
		t.Fatal("missing trust warning")
	}
	result, err := a.pushSuiteWorkspaceEvaluated(t.Context(), opt.Workspace, "suite:v1", filepath.Join(t.TempDir(), "layout"), false, false, SuitePushProvenance{}, opt.Out)
	if err != nil || result.Descriptor.Digest.String() != p.suite.Digest {
		t.Fatal("evaluated push changed S", err)
	}
	client, err := agentsessions.Dial(agentsessions.Options{Address: p.sessions.host.Address})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	e := r.Cases[0].SessionsExecution
	proof, err := client.VerifyCase(ctx, agentsessions.VerifyRequest{SessionUID: e.SessionUID, Harness: e.Harness, Model: p.suite.Model, Head: agentsessions.JournalHead{Seq: e.HeadSeq, Hash: e.HeadHash}, AnswerSHA256: r.Cases[0].AnswerDigest})
	if err != nil || proof.Status != "equivalent" || proof.ModelCalls != 0 || calls.Load() != 2 {
		t.Fatal("replay reached model or changed proof", proof, err, calls.Load())
	}
}

func TestSuiteSessionsEvaluationStopsWithoutExport(t *testing.T) {
	for _, failure := range []string{"instruction", "host drift", "source drift", "warning", "case output", "cancel"} {
		t.Run(failure, func(t *testing.T) {
			a, opt, calls := suiteSessionsEvaluationFixture(t)
			if failure == "instruction" {
				writeFile(t, filepath.Join(opt.Workspace, "agents/greeter/instructions.md"), "Answer only five.")
			}
			p, err := PrepareSuiteWorkspaceEvaluation(t.Context(), opt)
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			if failure == "warning" {
				a.Err = evaluationDiffFailWriter{}
			}
			if failure == "case output" {
				a.Out = suiteCaseSummaryFailWriter{needle: "first: pass"}
			}
			if failure == "host drift" || failure == "source drift" {
				a.Out = suiteSummaryObserver(func(raw []byte) (int, error) {
					if bytes.Contains(raw, []byte("first: pass")) {
						if failure == "host drift" {
							writeFile(t, opt.DevelopmentHost, "{}")
						} else {
							writeFile(t, filepath.Join(opt.Workspace, "agents/greeter/instructions.md"), "changed")
						}
					}
					return len(raw), nil
				})
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if failure == "cancel" {
				cancel()
			}
			if err := a.EvaluateSuiteWorkspace(ctx, p); err == nil {
				t.Fatal("failure exported a pass")
			}
			if _, err := os.Stat(opt.Out); !os.IsNotExist(err) {
				t.Fatal("failure left passing output", err)
			}
			expected := int64(1)
			if failure == "warning" || failure == "cancel" {
				expected = 0
			}
			if calls.Load() != expected {
				t.Fatal("later case ran after failure", calls.Load())
			}
		})
	}
}

func TestSuiteSessionsLatePublicationFailureReportsExistingCompleteProof(t *testing.T) {
	a, opt, calls := suiteSessionsEvaluationFixture(t)
	p, err := PrepareSuiteWorkspaceEvaluation(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	// Exercise the shared publisher's documented post-link directory-sync error.
	if err := p.output.directory.Close(); err != nil {
		t.Fatal(err)
	}
	err = a.EvaluateSuiteWorkspace(t.Context(), p)
	if !errors.Is(err, errSuitePassingExport) || !strings.Contains(err.Error(), "output may exist") {
		t.Fatalf("publication ambiguity was hidden: %v", err)
	}
	r, readErr := readSuitePassingReceipt(t.Context(), opt.Out)
	if readErr != nil || r.Sessions == nil || len(r.Cases) != 2 || calls.Load() != 2 {
		t.Fatalf("post-link failure changed complete-pass semantics: %v", readErr)
	}
}

type suiteSessionsCauseWriter struct{ cause error }

func (w suiteSessionsCauseWriter) Write([]byte) (int, error) { return 0, w.cause }

func TestSuiteSessionsFailurePreservesCauseWithoutPrintingPayload(t *testing.T) {
	a, opt, calls := suiteSessionsEvaluationFixture(t)
	p, err := PrepareSuiteWorkspaceEvaluation(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	sentinel := errors.New("PRIVATE_FAILURE_CANARY")
	cause := errors.Join(sentinel, status.Error(codes.DeadlineExceeded, "PRIVATE_FAILURE_CANARY"))
	a.Err = suiteSessionsCauseWriter{cause}
	err = a.EvaluateSuiteWorkspace(t.Context(), p)
	if !errors.Is(err, sentinel) || status.Code(err) != codes.DeadlineExceeded || strings.Contains(err.Error(), "PRIVATE_FAILURE_CANARY") {
		t.Fatalf("failure lost cause/status or exposed payload: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatal("warning failure reached model")
	}
	if _, err := os.Stat(opt.Out); !os.IsNotExist(err) {
		t.Fatal("warning failure exported proof")
	}
}

func TestSuiteSessionsDevelopmentSelectionRefusesBeforeEffects(t *testing.T) {
	for _, failure := range []string{"missing selection", "cluster", "inference", "bindings", "port", "namespace", "remote", "hostname", "model", "pin", "unknown", "symlink"} {
		t.Run(failure, func(t *testing.T) {
			_, opt, calls := suiteSessionsEvaluationFixture(t)
			raw, err := os.ReadFile(opt.DevelopmentHost)
			if err != nil {
				t.Fatal(err)
			}
			var host map[string]any
			if err := json.Unmarshal(raw, &host); err != nil {
				t.Fatal(err)
			}
			observation := host["observation"].(map[string]any)
			switch failure {
			case "missing selection":
				opt.DevelopmentHost = ""
			case "cluster":
				opt.ToContext = "kind-other"
			case "inference":
				opt.Inference = "provider:other"
			case "bindings":
				opt.Bindings = "missing"
			case "port":
				opt.ResultPort = "19180"
			case "namespace":
				opt.ToNamespace = "orka-system"
			case "remote":
				host["address"] = "192.0.2.1:8080"
			case "hostname":
				host["address"] = "localhost:8080"
			case "model":
				observation["model"] = "other-model"
			case "pin":
				observation["runtimeVersion"] = "main"
			case "unknown":
				host["private"] = "unrecognized"
			case "symlink":
				alias := filepath.Join(filepath.Dir(opt.DevelopmentHost), "alias.json")
				if err := os.Symlink(opt.DevelopmentHost, alias); err != nil {
					t.Fatal(err)
				}
				opt.DevelopmentHost = alias
			}
			if failure == "remote" || failure == "hostname" || failure == "model" || failure == "pin" || failure == "unknown" {
				raw, err = json.Marshal(host)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(opt.DevelopmentHost, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			p, err := PrepareSuiteWorkspaceEvaluation(t.Context(), opt)
			if p != nil {
				p.Close()
				t.Fatal("unsupported selector prepared execution")
			}
			if err == nil || calls.Load() != 0 {
				t.Fatal("unsupported mode reached host", err)
			}
			if _, err := os.Stat(opt.Out); !os.IsNotExist(err) {
				t.Fatal("unsupported mode wrote evidence")
			}
		})
	}
}
