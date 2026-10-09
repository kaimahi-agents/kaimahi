package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aramase/agentsessions/api"
	v1 "github.com/aramase/agentsessions/api/genpb"
	"github.com/aramase/agentsessions/controller"
	"github.com/aramase/agentsessions/eventlog"
	"github.com/aramase/agentsessions/harness/chatagent"
	"github.com/aramase/agentsessions/wire"
	"google.golang.org/grpc"
)

type verifyJournalServer struct {
	v1.UnimplementedSessionsServer
	records []eventlog.Record
	reads   atomic.Int64
}

func (s *verifyJournalServer) Replay(req *v1.ReplayRequest, stream grpc.ServerStreamingServer[v1.LogRecord]) error {
	s.reads.Add(1)
	if req.GetSession() != "recorded-session" || req.GetFromSeq() != 1 || req.GetToSeq() != int64(len(s.records)) {
		return fmt.Errorf("incorrect journal request")
	}
	for _, r := range s.records {
		if err := stream.Send(&v1.LogRecord{Seq: r.Seq, PrevHash: r.PrevHash, ContentHash: r.Hash, Fence: r.Fence, Event: wire.EventToProto(r.Event)}); err != nil {
			return err
		}
	}
	return nil
}

func verifyAppFixture(t *testing.T) (*App, VerifyAgentSessionsOptions, sessionsEvaluationReceipt, *verifyJournalServer) {
	t.Helper()
	t.Setenv("KMX_HOME", t.TempDir())
	log := eventlog.New()
	c, err := controller.New(eventlog.AsStore(log), func(context.Context, api.ModelRequest) (api.ModelResponse, error) {
		return api.ModelResponse{Message: *api.TextMessage("assistant", "private-answer-canary")}, nil
	}, controller.WithStart([]byte(`{"system_prompt":"private-prompt-canary"}`), 0))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Exec(context.Background(), chatagent.Harness{Model: "test-model"}, []api.Message{*api.TextMessage("user", "private-input-canary")}, 0); err != nil {
		t.Fatal(err)
	}
	s := &verifyJournalServer{records: log.Read(1)}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	v1.RegisterSessionsServer(server, s)
	done := make(chan struct{})
	go func() { defer close(done); _ = server.Serve(lis) }()
	t.Cleanup(func() { server.Stop(); <-done })
	head := s.records[len(s.records)-1]
	answer := sha256.Sum256([]byte("private-answer-canary"))
	receipt := sessionsEvaluationReceipt{Bundle: "sample", PortableDigest: strings.Repeat("a", 64), CasesDigest: strings.Repeat("b", 64), FullCaseSet: true, Result: "pass", Target: sessionsEvaluationTarget{Runtime: "agentsessions", Identity: sessionsHostIdentity{Version: 1, Provenance: "host-reported", Address: lis.Addr().String(), Harness: "chat", Model: "test-model"}}, Cases: []sessionsEvaluationResult{{ID: "one", Verdict: "pass", SessionUID: "recorded-session", Harness: "chat", Model: "test-model", JournalHead: sessionsJournalHead{Seq: head.Seq, Hash: head.Hash}, AnswerSHA256: fmt.Sprintf("%x", answer)}}}
	dir := filepath.Join(t.TempDir(), "receipts")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "eval-recorded.json")
	saveVerifyInput(t, path, receipt)
	return &App{Out: &bytes.Buffer{}}, VerifyAgentSessionsOptions{ReceiptPath: path, Sessions: lis.Addr().String(), Timeout: 10 * time.Second}, receipt, s
}
func saveVerifyInput(t *testing.T, path string, r sessionsEvaluationReceipt) {
	t.Helper()
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

// Catch accepting an unacknowledged destination, including DNS aliases, before any RPC.
func TestVerifySessionsPreflightNeverConnects(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		change     func(*VerifyAgentSessionsOptions, *sessionsEvaluationReceipt)
	}{
		{"missing destination", "--sessions is required", func(o *VerifyAgentSessionsOptions, _ *sessionsEvaluationReceipt) { o.Sessions = "" }},
		{"different port", "does not match", func(_ *VerifyAgentSessionsOptions, r *sessionsEvaluationReceipt) {
			r.Target.Identity.Address = "127.0.0.1:1"
		}},
		{"alias", "does not match", func(_ *VerifyAgentSessionsOptions, r *sessionsEvaluationReceipt) {
			_, p, _ := net.SplitHostPort(r.Target.Identity.Address)
			r.Target.Identity.Address = "localhost:" + p
		}},
		{"Orka receipt", "agentsessions receipt", func(_ *VerifyAgentSessionsOptions, r *sessionsEvaluationReceipt) { r.Target.Runtime = "orka" }},
		{"unknown identity version", "identity", func(_ *VerifyAgentSessionsOptions, r *sessionsEvaluationReceipt) { r.Target.Identity.Version = 2 }},
		{"invalid head", "case evidence", func(_ *VerifyAgentSessionsOptions, r *sessionsEvaluationReceipt) { r.Cases[0].JournalHead.Hash = "bad" }},
		{"unknown outcome", "case evidence", func(_ *VerifyAgentSessionsOptions, r *sessionsEvaluationReceipt) { r.Cases[0].Verdict = "unknown" }},
		{"duplicate case", "case evidence", func(_ *VerifyAgentSessionsOptions, r *sessionsEvaluationReceipt) {
			r.Cases = append(r.Cases, r.Cases[0])
		}},
		{"invalid case identity", "case evidence", func(_ *VerifyAgentSessionsOptions, r *sessionsEvaluationReceipt) { r.Cases[0].ID = "private\ncase" }},
		{"mixed model", "case evidence", func(_ *VerifyAgentSessionsOptions, r *sessionsEvaluationReceipt) { r.Cases[0].ModelMixed = true }},
		{"bad timeout", "--timeout must be", func(o *VerifyAgentSessionsOptions, _ *sessionsEvaluationReceipt) { o.Timeout = time.Nanosecond }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, o, r, s := verifyAppFixture(t)
			tc.change(&o, &r)
			saveVerifyInput(t, o.ReceiptPath, r)
			err := a.VerifyAgentSessions(o)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%v, want %q", err, tc.want)
			}
			if s.reads.Load() != 0 {
				t.Fatal("invalid receipt caused network read")
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(o.ReceiptPath), "verify-"+filepath.Base(o.ReceiptPath)+".json")); !os.IsNotExist(err) {
				t.Fatal("preflight wrote a report")
			}
		})
	}
}

// Catch bypassing replay, changing evaluation verdicts, payload leaks, or polluting gate filenames.
func TestVerifySessionsWritesSeparatePrivateEvidence(t *testing.T) {
	for _, verdict := range []string{"pass", "fail"} {
		t.Run(verdict, func(t *testing.T) {
			a, o, r, s := verifyAppFixture(t)
			r.Result = verdict
			r.Cases[0].Verdict = verdict
			saveVerifyInput(t, o.ReceiptPath, r)
			before, _ := os.ReadFile(o.ReceiptPath)
			// An equivalent address spelling must be accepted without DNS lookup.
			host, port, _ := net.SplitHostPort(o.Sessions)
			o.Sessions = net.JoinHostPort(host, "0"+port)
			if err := a.VerifyAgentSessions(o); err != nil {
				t.Fatal(err)
			}
			after, _ := os.ReadFile(o.ReceiptPath)
			if !bytes.Equal(before, after) {
				t.Fatal("verification changed evaluation receipt")
			}
			path := filepath.Join(filepath.Dir(o.ReceiptPath), "verify-"+filepath.Base(o.ReceiptPath)+".json")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var report sessionsVerificationReceipt
			if err := json.Unmarshal(raw, &report); err != nil {
				t.Fatal(err)
			}
			expectedReceiptHash := sha256.Sum256(before)
			if report.Result != "equivalent" || report.SourceReceiptSHA256 != fmt.Sprintf("%x", expectedReceiptHash) || len(report.Cases) != 1 || report.Cases[0].Status != "equivalent" || report.Cases[0].ModelCalls != 0 || report.ReferenceRevision == "" || report.HostImplementation != "unknown" {
				t.Fatalf("report: %+v", report)
			}
			for _, canary := range []string{"private-answer-canary", "private-prompt-canary", "private-input-canary"} {
				if bytes.Contains(raw, []byte(canary)) || strings.Contains(a.Out.(*bytes.Buffer).String(), canary) {
					t.Fatal("verification leaked payload")
				}
			}
			info, _ := os.Stat(path)
			if info.Mode().Perm() != 0600 {
				t.Fatalf("permissions %o", info.Mode().Perm())
			}
			if s.reads.Load() != 1 {
				t.Fatalf("reads %d", s.reads.Load())
			}
			evidence, err := readBundleGateEvidence(filepath.Dir(filepath.Dir(o.ReceiptPath)))
			if err != nil || len(evidence) != 1 || evidence[0].File != filepath.Base(o.ReceiptPath) {
				t.Fatalf("gate read report: %v %v", evidence, err)
			}
		})
	}
}

func TestVerifySessionsRecomputesMismatch(t *testing.T) {
	a, o, r, s := verifyAppFixture(t)
	if err := a.VerifyAgentSessions(o); err != nil {
		t.Fatal(err)
	}
	r.Cases[0].AnswerSHA256 = strings.Repeat("c", 64)
	saveVerifyInput(t, o.ReceiptPath, r)
	if err := a.VerifyAgentSessions(o); err == nil || !strings.Contains(err.Error(), "did not verify") {
		t.Fatalf("mismatch passed: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(o.ReceiptPath), "verify-"+filepath.Base(o.ReceiptPath)+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var report sessionsVerificationReceipt
	_ = json.Unmarshal(raw, &report)
	if report.Result != "mismatch" || report.Cases[0].Status != "mismatch" || s.reads.Load() != 2 {
		t.Fatalf("stale pass retained: %+v reads=%d", report, s.reads.Load())
	}
}

// Comparison normalization must not turn a DNS spelling into plaintext permission.
func TestVerifySessionsPreservesOperatorTransportSecurity(t *testing.T) {
	a, o, _, s := verifyAppFixture(t)
	host, port, _ := net.SplitHostPort(o.Sessions)
	o.Sessions = net.JoinHostPort(host+".", port)
	if err := a.VerifyAgentSessions(o); err == nil || !strings.Contains(err.Error(), "did not verify: unknown") {
		t.Fatalf("DNS spelling bypassed TLS: %v", err)
	}
	if s.reads.Load() != 0 {
		t.Fatal("plaintext journal read allowed for non-literal host")
	}
}

func TestVerifySessionsUnsupportedHarnessNeverPasses(t *testing.T) {
	a, o, r, s := verifyAppFixture(t)
	r.Cases[0].Harness = "custom"
	saveVerifyInput(t, o.ReceiptPath, r)
	if err := a.VerifyAgentSessions(o); err == nil || !strings.Contains(err.Error(), "did not verify: unsupported") {
		t.Fatalf("unsupported harness: %v", err)
	}
	if s.reads.Load() != 0 {
		t.Fatal("unsupported harness caused a journal read")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(o.ReceiptPath), "verify-"+filepath.Base(o.ReceiptPath)+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var report sessionsVerificationReceipt
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	if report.Result != "unsupported" || report.Cases[0].Status != "unsupported" {
		t.Fatalf("report: %+v", report)
	}
}

// Catch the Go decoder's last-key-wins behavior hiding cases or nested evidence.
func TestVerifySessionsRejectsDuplicateMembers(t *testing.T) {
	for _, tc := range []struct{ name, original, replacement string }{
		{"hidden case", `"cases":`, `"cases":[{"id":"hidden","verdict":"fail"}],"cases":`},
		{"case-folded hidden case", `"cases":`, `"CASES":[{"id":"hidden","verdict":"fail"}],"cases":`},
		{"nested digest", `"answerSHA256":`, `"answerSHA256":"bad","answerSHA256":`},
		{"escaped member", `"answerSHA256":`, `"answer\u0053HA256":"bad","answerSHA256":`},
		{"nested address", `"address":`, `"address":"unacknowledged.invalid:8080","address":`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, o, _, s := verifyAppFixture(t)
			raw, err := os.ReadFile(o.ReceiptPath)
			if err != nil {
				t.Fatal(err)
			}
			ambiguous := strings.Replace(string(raw), tc.original, tc.replacement, 1)
			if err := os.WriteFile(o.ReceiptPath, []byte(ambiguous), 0600); err != nil {
				t.Fatal(err)
			}
			if err := a.VerifyAgentSessions(o); err == nil || !strings.Contains(err.Error(), "invalid sessions receipt JSON") {
				t.Fatalf("ambiguous receipt: %v", err)
			}
			if s.reads.Load() != 0 {
				t.Fatal("ambiguous receipt connected")
			}
		})
	}
}

func TestVerifySessionsRejectsMalformedReceipt(t *testing.T) {
	for _, tc := range []struct{ name, raw, want string }{
		{"invalid JSON", `{`, "invalid sessions receipt JSON"},
		{"oversized", strings.Repeat(" ", maxSessionsReceiptBytes+1), "exceeds 1 MiB"},
		{"trailing JSON", `{} {}`, "invalid sessions receipt JSON"},
		{"unknown field", `{"unexpected":true}`, "invalid sessions receipt JSON"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, o, _, s := verifyAppFixture(t)
			if err := os.WriteFile(o.ReceiptPath, []byte(tc.raw), 0600); err != nil {
				t.Fatal(err)
			}
			if err := a.VerifyAgentSessions(o); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%v, want %q", err, tc.want)
			}
			if s.reads.Load() != 0 {
				t.Fatal("malformed receipt connected")
			}
		})
	}
}

func TestVerifySessionsReplacesReportLinkWithoutFollowingIt(t *testing.T) {
	a, o, _, _ := verifyAppFixture(t)
	canary := filepath.Join(t.TempDir(), "canary")
	if err := os.WriteFile(canary, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(filepath.Dir(o.ReceiptPath), "verify-"+filepath.Base(o.ReceiptPath)+".json")
	if err := os.Symlink(canary, path); err != nil {
		t.Fatal(err)
	}
	if err := a.VerifyAgentSessions(o); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(canary)
	if err != nil || string(raw) != "unchanged" {
		t.Fatalf("followed report link: %q %v", raw, err)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		t.Fatalf("report: %v %v", info, err)
	}
}

func TestVerifySessionsRefusesLinkedInput(t *testing.T) {
	a, o, _, s := verifyAppFixture(t)
	original := o.ReceiptPath
	o.ReceiptPath = filepath.Join(filepath.Dir(original), "linked.json")
	if err := os.Symlink(original, o.ReceiptPath); err != nil {
		t.Fatal(err)
	}
	if err := a.VerifyAgentSessions(o); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("linked receipt: %v", err)
	}
	if s.reads.Load() != 0 {
		t.Fatal("linked receipt connected")
	}
}
