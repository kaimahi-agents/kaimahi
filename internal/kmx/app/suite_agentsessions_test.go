package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-primitives/sessions/api"
	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/harness/chatagent"
	"github.com/agent-primitives/sessions/placement"
	"github.com/agent-primitives/sessions/runtime/local"
	"github.com/agent-primitives/sessions/session"
	"github.com/agent-primitives/sessions/sqlitelog"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsessions"
	suiteoras "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
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

func TestSuiteSessionsJudgedRepeats(t *testing.T) {
	for _, outcome := range []string{"pass", "threshold", "late split", "model mismatch", "unavailable", "deadline", "host drift", "source drift"} {
		t.Run(outcome, func(t *testing.T) {
			a, opt, executions := suiteSessionsEvaluationFixture(t)
			caseYAML := suiteJudgeYAML + "runs: 3\nminPasses: 2\n"
			if outcome == "model mismatch" {
				caseYAML = strings.Replace(caseYAML, "judgeModel: judge-model", "judgeModel: other-model", 1)
			}
			writeFile(t, filepath.Join(opt.Workspace, "eval/example.yaml"), caseYAML)
			endpoint, samples := suiteJudgeFixtureServer(t, func(r *http.Request, n int) int {
				switch outcome {
				case "threshold":
					return 20
				case "pass":
					if n <= 2 {
						return 20
					}
				case "late split":
					if n == 5 {
						return 20
					}
				case "unavailable":
					return -1
				case "deadline":
					<-r.Context().Done()
				case "host drift":
					writeFile(t, opt.DevelopmentHost, "{}")
				case "source drift":
					writeFile(t, filepath.Join(opt.Workspace, "agents/greeter/instructions.md"), "changed")
				}
				return 90
			})
			opt.JudgeBaseURL, opt.ManagedPass = endpoint, true
			if outcome == "deadline" {
				opt.CaseTimeout = 2 * time.Second
			}
			p, err := PrepareSuiteWorkspaceEvaluation(t.Context(), opt)
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			err = a.EvaluateSuiteWorkspace(t.Context(), p)
			wantExecutions, wantSamples := int64(1), int32(2)
			if outcome == "pass" {
				wantExecutions, wantSamples = 4, 6
			}
			if outcome == "threshold" || outcome == "late split" {
				wantExecutions, wantSamples = 3, 6
			}
			if outcome == "deadline" {
				wantSamples = 1
			}
			if executions.Load() != wantExecutions || samples.Load() != wantSamples {
				t.Fatalf("unexpected work: executions=%d samples=%d error=%v", executions.Load(), samples.Load(), err)
			}
			if outcome != "pass" {
				if err == nil || len(p.completed) != 0 || p.record != "" {
					t.Fatal("non-pass registered evidence", err)
				}
				if outcome == "deadline" && !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal("lost judge deadline", err)
				}
				if _, e := os.Stat(opt.Out); !os.IsNotExist(e) {
					t.Fatal("non-pass exported evidence", e)
				}
				if strings.Contains(a.Out.(*bytes.Buffer).String(), "first run 1/3: pass") && strings.HasSuffix(outcome, "drift") {
					t.Fatal("drift credited pass")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			r, err := readSuitePassingReceipt(t.Context(), opt.Out)
			if err != nil {
				t.Fatal(err)
			}
			if len(r.Cases) != 4 || r.Cases[0].Verdict != "fail" || r.Cases[0].Run != 1 || r.Cases[2].Run != 3 || r.Cases[3].Run != 0 {
				t.Fatal("lost actual outcomes or ordinals")
			}
			seenSessions, seenExecutions := map[string]bool{}, map[string]bool{}
			for _, row := range r.Cases {
				e := row.SessionsExecution
				if seenSessions[e.SessionUID] || seenExecutions[e.ExecutionID] || e.HeadSeq != 5 {
					t.Fatal("reused identity or changed journal contract")
				}
				seenSessions[e.SessionUID], seenExecutions[e.ExecutionID] = true, true
			}
			if !strings.Contains(a.Err.(*bytes.Buffer).String(), "4 fresh Sessions") || !strings.Contains(a.Err.(*bytes.Buffer).String(), "6 independent requests") {
				t.Fatal("missing work disclosure")
			}
			if strings.Contains(fmt.Sprint(a.Out, a.Err), suiteJudgeCanary) {
				t.Fatal("judge payload leaked")
			}
			if err := p.Close(); err != nil {
				t.Fatal(err)
			}
			layout := filepath.Join(t.TempDir(), "layout")
			result, err := a.PushSuiteWorkspaceManaged(t.Context(), SuiteWorkspacePushOptions{Workspace: opt.Workspace, Reference: "suite:v1", Layout: layout})
			if err != nil || result.Descriptor.Digest.String() != p.suite.Digest {
				t.Fatal("managed exact-S push", err)
			}
			if _, err := a.PushSuiteWorkspaceEvaluated(t.Context(), opt.Workspace, "suite:v2", filepath.Join(t.TempDir(), "explicit"), false, false, SuitePushProvenance{}, opt.Out); err != nil {
				t.Fatal("explicit push", err)
			}
			if err := os.Rename(opt.Workspace, opt.Workspace+"-withheld"); err != nil {
				t.Fatal(err)
			}
			artifact, err := suiteoras.ReadArtifactLayout(t.Context(), layout, p.suite.Digest)
			if err != nil {
				t.Fatal(err)
			}
			suite, err := workspace.LoadArtifact(t.Context(), artifact)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := canonicalSuitePassingReceipt(t.Context(), r, suite)
			if err != nil {
				t.Fatal("source-free coherence", err)
			}
			for _, mutation := range []string{"missing", "ordinal", "threshold", "unknown", "session", "execution", "style", "model", "scores"} {
				t.Run(mutation, func(t *testing.T) {
					bad, e := parseSuitePassingReceipt(raw)
					if e != nil {
						t.Fatal(e)
					}
					switch mutation {
					case "missing":
						bad.Cases = bad.Cases[1:]
					case "ordinal":
						bad.Cases[1].Run = 1
					case "threshold":
						for i := 0; i < 3; i++ {
							bad.Cases[i].MinPasses = 3
						}
					case "unknown":
						bad.Cases[2].Verdict = "unknown"
					case "session":
						bad.Cases[1].SessionsExecution.SessionUID = bad.Cases[0].SessionsExecution.SessionUID
					case "execution":
						bad.Cases[1].SessionsExecution.ExecutionID = bad.Cases[0].SessionsExecution.ExecutionID
					case "style":
						bad.Cases[0].Assertions[0].Judge.RequestStyle = ""
					case "model":
						bad.Cases[0].Assertions[0].Judge.Model = "other-model"
					case "scores":
						bad.Cases[0].Assertions[0].Judge.Scores = []int{90, 90}
					}
					if _, e := canonicalSuitePassingReceipt(t.Context(), bad, suite); !errors.Is(e, errSuitePassingReceipt) {
						t.Fatal("tampered source-free proof accepted", e)
					}
				})
			}
			if executions.Load() != 4 || samples.Load() != 6 {
				t.Fatal("offline consumers executed work")
			}
		})
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
