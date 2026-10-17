package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-primitives/sessions/api"
	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/controller"
	"github.com/agent-primitives/sessions/eventlog"
	"github.com/agent-primitives/sessions/harness/chatagent"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsessions"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func sessionReferenceAppCase(t *testing.T, id, uid string) agentruntime.SessionReferenceCase {
	t.Helper()
	c, err := agentruntime.NewSessionReferenceCase(agentruntime.EvaluationCase{ID: id, ExpectContains: []string{"private-authored-operand"}}, agentruntime.SessionReference{
		Store: "127.0.0.1:8080", SessionUID: uid, Harness: "chat", HarnessVersion: agentsessions.ReferenceRevision, Model: "test-model",
		InputSeq: 2, PrefixSeq: 5, PrefixHash: strings.Repeat("a", 64), InputDigest: strings.Repeat("b", 64), AnswerDigest: strings.Repeat("c", 64), ConfigDigest: strings.Repeat("d", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func readSessionReferenceAppBytes(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// A single explicit source selector cannot evaluate a mixed-store manifest.
func TestSessionReferenceAppendRefusesMixedStoresWithoutChangingBytes(t *testing.T) {
	first := sessionReferenceAppCase(t, "first", "source-one")
	second := sessionReferenceAppCase(t, "second", "source-two")
	second.Source.Store = "127.0.0.1:8081"
	path := filepath.Join(diffTempDir(t), "manifest.json")
	if err := appendSessionReferenceManifest(path, []agentruntime.SessionReferenceCase{first}); err != nil {
		t.Fatal(err)
	}
	before := readSessionReferenceAppBytes(t, path)
	err := appendSessionReferenceManifest(path, []agentruntime.SessionReferenceCase{second})
	if err == nil || !strings.Contains(err.Error(), "one source store") {
		t.Fatalf("mixed-store append accepted: %v", err)
	}
	if !bytes.Equal(before, readSessionReferenceAppBytes(t, path)) {
		t.Fatal("mixed-store append changed manifest")
	}
}
func TestSessionReferenceExtractionRefusesDifferentStoreBeforeReads(t *testing.T) {
	a, opt, s := newSessionReferenceExtractionFixture(t)
	existing := sessionReferenceAppCase(t, "existing", "unrelated-uid")
	if err := appendSessionReferenceManifest(opt.Out, []agentruntime.SessionReferenceCase{existing}); err != nil {
		t.Fatal(err)
	}
	before := readSessionReferenceAppBytes(t, opt.Out)
	err := a.ExtractAgentSessionReferences(opt)
	if err == nil || !strings.Contains(err.Error(), "one source store") {
		t.Fatalf("wrong store accepted: %v", err)
	}
	if !bytes.Equal(before, readSessionReferenceAppBytes(t, opt.Out)) {
		t.Fatal("preflight changed manifest")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.requests) != 0 {
		t.Fatal("wrong-store append read sources")
	}
}

// Catch truncation/overwrite before duplicate detection, including duplicates
// among additions and duplicates by either source identity or authored case ID.
func TestSessionReferenceAppendRefusesDuplicatesWithoutChangingBytes(t *testing.T) {
	first := sessionReferenceAppCase(t, "first", "session-1")
	second := sessionReferenceAppCase(t, "second", "session-2")
	for _, kind := range []string{"source", "id", "batch source", "batch id"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(diffTempDir(t), "private-manifest.json")
			if err := appendSessionReferenceManifest(path, []agentruntime.SessionReferenceCase{first}); err != nil {
				t.Fatal(err)
			}
			before := readSessionReferenceAppBytes(t, path)
			duplicate := second
			additions := []agentruntime.SessionReferenceCase{duplicate}
			switch kind {
			case "source":
				duplicate.Source = first.Source
				additions[0] = duplicate
			case "id":
				duplicate.ID = first.ID
				additions[0] = duplicate
			case "batch source":
				duplicate.ID = "other-id"
				additions = append(additions, duplicate)
			case "batch id":
				duplicate.Source.SessionUID = "session-3"
				additions = append(additions, duplicate)
			}
			if err := appendSessionReferenceManifest(path, additions); !errors.Is(err, errSessionReferenceDuplicate) {
				t.Fatalf("want duplicate refusal, got %v", err)
			}
			if after := readSessionReferenceAppBytes(t, path); !bytes.Equal(before, after) {
				t.Fatal("duplicate refusal changed manifest")
			}
		})
	}
}

// Catch unsafe file/ancestor following, unbounded reads, ignored parser errors,
// and accepting manifests whose authored rubric is readable by other users.
func TestSessionReferenceReadAndAppendRefuseUnsafeFiles(t *testing.T) {
	first := sessionReferenceAppCase(t, "first", "session-1")
	for _, kind := range []string{"invalid", "oversize", "link", "ancestor link", "directory", "broad permissions", "unsafe directory"} {
		t.Run(kind, func(t *testing.T) {
			dir := diffTempDir(t)
			path := filepath.Join(dir, "private-path-canary.json")
			if err := appendSessionReferenceManifest(path, []agentruntime.SessionReferenceCase{first}); err != nil {
				t.Fatal(err)
			}
			var before []byte
			switch kind {
			case "invalid":
				if err := os.WriteFile(path, []byte("private-parser-error-canary"), 0600); err != nil {
					t.Fatal(err)
				}
			case "oversize":
				if err := os.WriteFile(path, bytes.Repeat([]byte("x"), (1<<20)+1), 0600); err != nil {
					t.Fatal(err)
				}
			case "link":
				original := path + ".original"
				if err := os.Rename(path, original); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(original, path); err != nil {
					t.Fatal(err)
				}
			case "ancestor link":
				link := filepath.Join(diffTempDir(t), "linked")
				if err := os.Symlink(dir, link); err != nil {
					t.Fatal(err)
				}
				path = filepath.Join(link, filepath.Base(path))
			case "directory":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "broad permissions":
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			case "unsafe directory":
				if err := os.Chmod(dir, 0777); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(dir, 0700) })
			}
			if kind != "directory" {
				before = readSessionReferenceAppBytes(t, path)
			}
			want := errSessionReferenceFile
			if kind == "invalid" {
				want = errSessionReferenceManifest
			} else if kind == "ancestor link" || kind == "unsafe directory" {
				want = errSessionReferenceDirectory
			}
			_, err := readSessionReferenceManifest(path)
			if !errors.Is(err, want) || strings.Contains(err.Error(), "canary") || strings.Contains(err.Error(), dir) {
				t.Fatalf("unsafe read diagnostic: %v", err)
			}
			err = appendSessionReferenceManifest(path, []agentruntime.SessionReferenceCase{sessionReferenceAppCase(t, "second", "session-2")})
			if !errors.Is(err, want) || strings.Contains(err.Error(), "canary") || strings.Contains(err.Error(), dir) {
				t.Fatalf("unsafe append diagnostic: %v", err)
			}
			if kind != "directory" && !bytes.Equal(before, readSessionReferenceAppBytes(t, path)) {
				t.Fatal("refusal modified bytes")
			}
		})
	}
}

// Catch nonprivate creation and lost updates when cooperative appenders overlap.
func TestSessionReferenceConcurrentAppendRetainsAllPrivateCases(t *testing.T) {
	path := filepath.Join(diffTempDir(t), "manifest.json")
	const count = 12
	cases := make([]agentruntime.SessionReferenceCase, count)
	for i := range cases {
		cases[i] = sessionReferenceAppCase(t, fmt.Sprintf("case-%d", i), fmt.Sprintf("session-%d", i))
	}
	start := make(chan struct{})
	errs := make(chan error, count)
	var writers sync.WaitGroup
	for _, c := range cases {
		writers.Add(1)
		go func(c agentruntime.SessionReferenceCase) {
			defer writers.Done()
			<-start
			errs <- appendSessionReferenceManifest(path, []agentruntime.SessionReferenceCase{c})
		}(c)
	}
	close(start)
	writers.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	manifest, err := readSessionReferenceManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Cases) != count {
		t.Fatalf("lost additions: %d", len(manifest.Cases))
	}
	seen := map[string]bool{}
	for _, c := range manifest.Cases {
		seen[c.Source.SessionUID] = true
	}
	for i := range count {
		if !seen[fmt.Sprintf("session-%d", i)] {
			t.Fatalf("missing addition %d", i)
		}
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("not private: %v %v", info, err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("left lock/staging files: %v", entries)
	}
}

// Exercise the real final replacement boundary with filesystem failures, not
// injected production hooks. Neither rename failure nor stale identity clobbers.
func TestSessionReferenceFailedReplacementPreservesExistingBytes(t *testing.T) {
	for _, kind := range []string{"rename failure", "identity changed", "content changed", "link changed"} {
		t.Run(kind, func(t *testing.T) {
			dir := diffTempDir(t)
			path := filepath.Join(dir, "manifest.json")
			if err := appendSessionReferenceManifest(path, []agentruntime.SessionReferenceCase{sessionReferenceAppCase(t, "first", "session-1")}); err != nil {
				t.Fatal(err)
			}
			before := readSessionReferenceAppBytes(t, path)
			original, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			stage := "stage.json"
			if err := os.WriteFile(filepath.Join(dir, stage), []byte("replacement"), 0600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "rename failure":
				// Missing staged file is an actual rename failure even when tests run as root.
				if err := os.Remove(filepath.Join(dir, stage)); err != nil {
					t.Fatal(err)
				}
			case "identity changed":
				if err := os.Rename(path, path+".original"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, before, 0600); err != nil {
					t.Fatal(err)
				}
			case "content changed":
				if err := os.WriteFile(path, append(before, ' '), 0600); err != nil {
					t.Fatal(err)
				}
			case "link changed":
				if err := os.Rename(path, path+".original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path+".original", path); err != nil {
					t.Fatal(err)
				}
			}
			current := readSessionReferenceAppBytes(t, path)
			want := errSessionReferenceChanged
			if kind == "rename failure" {
				want = errSessionReferenceAppend
			}
			if err := replaceSessionReferenceManifest(root, "manifest.json", stage, original, before); !errors.Is(err, want) {
				t.Fatalf("want %v, got %v", want, err)
			}
			if !bytes.Equal(current, readSessionReferenceAppBytes(t, path)) {
				t.Fatal("failed replacement changed bytes")
			}
		})
	}
}

// Catch over-budget append validation occurring after truncation or replacement.
func TestSessionReferenceAppendCombinedBudgetLeavesBytesUnchanged(t *testing.T) {
	path := filepath.Join(diffTempDir(t), "manifest.json")
	large := strings.Repeat("public expectation ", 34000)
	cases := make([]agentruntime.SessionReferenceCase, 2)
	for i := range cases {
		source := sessionReferenceAppCase(t, "seed", fmt.Sprintf("session-%d", i)).Source
		c, err := agentruntime.NewSessionReferenceCase(agentruntime.EvaluationCase{ID: fmt.Sprintf("large-%d", i), ExpectContains: []string{large}}, source)
		if err != nil {
			t.Fatal(err)
		}
		cases[i] = c
	}
	// This fixture tests the combined byte budget, not a wall-clock limit.
	// Large assertion validation can exceed the convenience timeout under race.
	if err := appendSessionReferenceManifestContext(t.Context(), path, cases[:1]); err != nil {
		t.Fatal(err)
	}
	before := readSessionReferenceAppBytes(t, path)
	if err := appendSessionReferenceManifest(path, cases[1:]); !errors.Is(err, errSessionReferenceManifest) {
		t.Fatalf("want combined budget refusal, got %v", err)
	}
	if !bytes.Equal(before, readSessionReferenceAppBytes(t, path)) {
		t.Fatal("over-budget append changed bytes")
	}
}

// Exercise lock refusal/cancellation through actual sidecar files and context.
// A lock not owned by this append must not be removed on any failure path.
func TestSessionReferenceAppendLockFailurePreservesManifestAndLock(t *testing.T) {
	for _, mode := range []string{"link", "broad", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(diffTempDir(t), "manifest.json")
			if err := appendSessionReferenceManifest(path, []agentruntime.SessionReferenceCase{sessionReferenceAppCase(t, "first", "session-1")}); err != nil {
				t.Fatal(err)
			}
			before := readSessionReferenceAppBytes(t, path)
			lock := path + ".lock"
			if mode == "link" {
				if err := os.Symlink(path, lock); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.WriteFile(lock, []byte("other writer"), 0600); err != nil {
					t.Fatal(err)
				}
				if mode == "broad" {
					if err := os.Chmod(lock, 0644); err != nil {
						t.Fatal(err)
					}
				}
			}
			ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
			defer cancel()
			if err := appendSessionReferenceManifestContext(ctx, path, []agentruntime.SessionReferenceCase{sessionReferenceAppCase(t, "second", "session-2")}); !errors.Is(err, errSessionReferenceAppend) {
				t.Fatalf("want lock refusal, got %v", err)
			}
			if !bytes.Equal(before, readSessionReferenceAppBytes(t, path)) {
				t.Fatal("lock failure changed bytes")
			}
			if _, err := os.Lstat(lock); err != nil {
				t.Fatalf("removed another writer's lock: %v", err)
			}
		})
	}
}

// The fake replaces only the external gRPC store. Source journals are generated
// by the pinned real controller/chat harness so input fingerprints are valid.
type sessionReferenceExtractionServer struct {
	v1.UnimplementedSessionsServer
	mu             sync.Mutex
	records        []*v1.LogRecord
	requests       []string
	failUID        string
	unsupportedUID string
	waitUID        string
	entered        chan struct{}
}

func (s *sessionReferenceExtractionServer) Replay(req *v1.ReplayRequest, stream v1.Sessions_ReplayServer) error {
	s.mu.Lock()
	s.requests = append(s.requests, req.GetSession())
	s.mu.Unlock()
	if req.GetSession() == s.failUID {
		return status.Error(codes.Unavailable, "private-remote-error-canary")
	}
	if req.GetSession() == s.waitUID {
		close(s.entered)
		<-stream.Context().Done()
		return stream.Context().Err()
	}
	records := s.records
	if req.GetSession() == s.unsupportedUID {
		records = records[:len(records)-1]
	}
	for _, r := range records {
		if r.Seq >= req.GetFromSeq() && (req.GetToSeq() == 0 || r.Seq <= req.GetToSeq()) {
			if err := stream.Send(r); err != nil {
				return err
			}
		}
	}
	return nil
}

func newSessionReferenceExtractionFixture(t *testing.T) (*App, SessionReferenceExtractionOptions, *sessionReferenceExtractionServer) {
	t.Helper()
	log := eventlog.New()
	c, err := controller.New(eventlog.AsStore(log), func(context.Context, api.ModelRequest) (api.ModelResponse, error) {
		return api.ModelResponse{Message: *api.TextMessage("assistant", "private-source-answer-canary")}, nil
	}, controller.WithStart([]byte(`{"system_prompt":"private-source-config-canary"}`), 0))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Exec(t.Context(), chatagent.Harness{Model: "test-model"}, []api.Message{*api.TextMessage("user", "private-source-input-canary")}, 0); err != nil {
		t.Fatal(err)
	}
	s := &sessionReferenceExtractionServer{}
	for _, r := range log.Snapshot() {
		s.records = append(s.records, eventlog.RecordToProto(r))
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	v1.RegisterSessionsServer(server, s)
	done := make(chan struct{})
	go func() { defer close(done); _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); <-done })
	dir := diffTempDir(t)
	assertions := filepath.Join(dir, "assertions.yaml")
	if err := os.WriteFile(assertions, []byte("id: authored\nexpectContains: [private-authored-operand]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return &App{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}, SessionReferenceExtractionOptions{Sessions: listener.Addr().String(), Assertions: assertions, Out: filepath.Join(dir, "manifest.json"), SessionUIDs: []string{"session-1", "session-2"}}, s
}

func assertSessionReferenceOutputPrivate(t *testing.T, a *App, err error) {
	t.Helper()
	text := a.Out.(*bytes.Buffer).String() + a.Err.(*bytes.Buffer).String()
	if err != nil {
		text += err.Error()
	}
	for _, canary := range []string{"private-authored-operand", "private-source-input-canary", "private-source-answer-canary", "private-source-config-canary", "private-remote-error-canary", "private-path-canary"} {
		if strings.Contains(text, canary) {
			t.Fatalf("leaked %s", canary)
		}
	}
}

func TestSessionReferenceExtractionAppendsAllUIDsWithoutPayloadOutput(t *testing.T) {
	a, opt, s := newSessionReferenceExtractionFixture(t)
	// Authored YAML may already be versioned/public; only manifests require
	// private permissions. Normalize the explicit source before recording it.
	if err := os.Chmod(opt.Assertions, 0644); err != nil {
		t.Fatal(err)
	}
	address := opt.Sessions
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	opt.Sessions = "[::ffff:127.0.0.1]:" + port
	if err := a.ExtractAgentSessionReferences(opt); err != nil {
		t.Fatal(err)
	}
	manifest, err := readSessionReferenceManifest(opt.Out)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.SchemaVersion != 1 || manifest.Policy != agentruntime.SessionReferencePolicy || len(manifest.Cases) != 2 {
		t.Fatalf("bad extracted manifest: %+v", manifest)
	}
	for i, c := range manifest.Cases {
		if c.Source.Store != address || c.Source.SessionUID != opt.SessionUIDs[i] || c.Source.InputSeq != 2 || c.Source.PrefixSeq != 5 {
			t.Fatalf("bad source: %+v", c.Source)
		}
		if c.Source.InputDigest != agentruntime.EvaluationInputDigest("private-source-input-canary") || c.Source.AnswerDigest != agentruntime.EvaluationAnswerDigest("private-source-answer-canary") {
			t.Fatal("not exact digests")
		}
		if !strings.Contains(a.Out.(*bytes.Buffer).String(), c.ID) || !strings.Contains(a.Out.(*bytes.Buffer).String(), c.Source.InputDigest) {
			t.Fatal("missing content-free case evidence")
		}
	}
	raw := readSessionReferenceAppBytes(t, opt.Out)
	for _, canary := range []string{"private-source-input-canary", "private-source-answer-canary", "private-source-config-canary"} {
		if bytes.Contains(raw, []byte(canary)) {
			t.Fatal("source payload persisted")
		}
	}
	assertSessionReferenceOutputPrivate(t, a, nil)
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.requests) != 2 {
		t.Fatalf("unexpected reads: %v", s.requests)
	}
}

// Catch normalization weakening transport: a DNS-spelled loopback requires
// TLS even when its logical store identity normalizes to a literal loopback.
func TestSessionReferenceExtractionPreservesRawTransportAddress(t *testing.T) {
	a, opt, s := newSessionReferenceExtractionFixture(t)
	_, port, err := net.SplitHostPort(opt.Sessions)
	if err != nil {
		t.Fatal(err)
	}
	opt.Sessions = net.JoinHostPort("127.0.0.1.", port)
	normalized, err := agentsessions.NormalizeAddress(opt.Sessions)
	if err != nil || normalized != net.JoinHostPort("127.0.0.1", port) {
		t.Fatalf("unexpected logical identity: %s %v", normalized, err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	a.Run = &run.Runner{Context: ctx}
	if err := a.ExtractAgentSessionReferences(opt); !errors.Is(err, errSessionReferenceExtraction) {
		t.Fatalf("DNS-spelled source reached plaintext server: %v", err)
	}
	if _, err := os.Lstat(opt.Out); !os.IsNotExist(err) {
		t.Fatalf("insecure extraction wrote manifest: %v", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.requests) != 0 {
		t.Fatalf("source selection weakened TLS policy: %v", s.requests)
	}
}

// Catch networking before assertions/UID/output validation and duplicate
// source reads. Errors must not quote supplied paths, operands or remote text.
func TestSessionReferenceExtractionPreflightRefusesBeforeNetwork(t *testing.T) {
	for _, kind := range []string{"unsafe uid", "duplicate uid", "authored input", "invalid assertions", "duplicate output", "duplicate output id", "linked output", "bad output parent", "unsafe address"} {
		t.Run(kind, func(t *testing.T) {
			a, opt, s := newSessionReferenceExtractionFixture(t)
			switch kind {
			case "unsafe uid":
				opt.SessionUIDs[1] = "private-uid\ncanary"
			case "duplicate uid":
				opt.SessionUIDs[1] = opt.SessionUIDs[0]
			case "authored input":
				if err := os.WriteFile(opt.Assertions, []byte("id: authored\ninput: private-source-input-canary\nexpectContains: [private-authored-operand]\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "invalid assertions":
				if err := os.WriteFile(opt.Assertions, []byte("id: authored\nunknown: private-authored-operand\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "duplicate output":
				c := sessionReferenceAppCase(t, "existing", "session-1")
				c.Source.Store = opt.Sessions
				if err := appendSessionReferenceManifest(opt.Out, []agentruntime.SessionReferenceCase{c}); err != nil {
					t.Fatal(err)
				}
			case "duplicate output id":
				c := sessionReferenceAppCase(t, "authored", "session-1")
				c.Source.Store = opt.Sessions
				c, err := agentruntime.NewSessionReferenceCase(agentruntime.EvaluationCase{ID: "authored", ExpectContains: []string{"private-authored-operand"}}, c.Source)
				if err != nil {
					t.Fatal(err)
				}
				// Keep the predicted case ID but attach a different source, so
				// source-UID duplicate preflight alone cannot catch the conflict.
				c.Source.SessionUID = "different-session"
				if err := appendSessionReferenceManifest(opt.Out, []agentruntime.SessionReferenceCase{c}); err != nil {
					t.Fatal(err)
				}
			case "linked output":
				if err := os.Symlink(opt.Assertions, opt.Out); err != nil {
					t.Fatal(err)
				}
			case "bad output parent":
				opt.Out = filepath.Join(opt.Out, "private-path-canary.json")
			case "unsafe address":
				opt.Sessions = "private-path-canary://invalid"
			}
			err := a.ExtractAgentSessionReferences(opt)
			if err == nil {
				t.Fatal("unsafe extraction accepted")
			}
			switch kind {
			case "duplicate uid", "duplicate output", "duplicate output id":
				if !errors.Is(err, errSessionReferenceDuplicate) {
					t.Fatalf("wrong duplicate refusal: %v", err)
				}
			case "authored input", "invalid assertions":
				if err.Error() != "invalid session reference assertions" {
					t.Fatalf("wrong assertion refusal: %v", err)
				}
			case "linked output":
				if !errors.Is(err, errSessionReferenceFile) {
					t.Fatalf("wrong linked-file refusal: %v", err)
				}
			case "bad output parent":
				if !errors.Is(err, errSessionReferenceDirectory) {
					t.Fatalf("wrong directory refusal: %v", err)
				}
			}
			assertSessionReferenceOutputPrivate(t, a, err)
			s.mu.Lock()
			defer s.mu.Unlock()
			if len(s.requests) != 0 {
				t.Fatalf("preflight read sources: %v", s.requests)
			}
		})
	}
}

func TestSessionReferencePartialExtractionFailureLeavesManifestUnchanged(t *testing.T) {
	for _, mode := range []string{"unavailable", "unsupported"} {
		for _, existing := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/existing=%v", mode, existing), func(t *testing.T) {
				a, opt, s := newSessionReferenceExtractionFixture(t)
				var before []byte
				if existing {
					existingCase := sessionReferenceAppCase(t, "existing", "session-other")
					existingCase.Source.Store = opt.Sessions
					if err := appendSessionReferenceManifest(opt.Out, []agentruntime.SessionReferenceCase{existingCase}); err != nil {
						t.Fatal(err)
					}
					before = readSessionReferenceAppBytes(t, opt.Out)
				}
				if mode == "unavailable" {
					s.failUID = "session-2"
				} else {
					s.unsupportedUID = "session-2"
				}
				err := a.ExtractAgentSessionReferences(opt)
				if !errors.Is(err, errSessionReferenceExtraction) {
					t.Fatalf("want unavailable/unsupported extraction, got %v", err)
				}
				assertSessionReferenceOutputPrivate(t, a, err)
				if existing {
					if !bytes.Equal(before, readSessionReferenceAppBytes(t, opt.Out)) {
						t.Fatal("partial extraction changed bytes")
					}
				} else if _, err := os.Lstat(opt.Out); !os.IsNotExist(err) {
					t.Fatalf("partial extraction wrote file: %v", err)
				}
				if a.Out.(*bytes.Buffer).Len() != 0 {
					t.Fatal("printed partially extracted cases")
				}
				s.mu.Lock()
				defer s.mu.Unlock()
				if len(s.requests) != 2 {
					t.Fatalf("did not exercise partial extraction: %v", s.requests)
				}
			})
		}
	}
}

func TestSessionReferenceCanceledExtractionLeavesManifestUnchanged(t *testing.T) {
	a, opt, s := newSessionReferenceExtractionFixture(t)
	existingCase := sessionReferenceAppCase(t, "existing", "session-other")
	existingCase.Source.Store = opt.Sessions
	if err := appendSessionReferenceManifest(opt.Out, []agentruntime.SessionReferenceCase{existingCase}); err != nil {
		t.Fatal(err)
	}
	before := readSessionReferenceAppBytes(t, opt.Out)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	a.Run = &run.Runner{Context: ctx}
	s.waitUID, s.entered = "session-2", make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- a.ExtractAgentSessionReferences(opt) }()
	select {
	case <-s.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("second extraction did not start")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled extraction succeeded")
		}
		assertSessionReferenceOutputPrivate(t, a, err)
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation not bounded")
	}
	if !bytes.Equal(before, readSessionReferenceAppBytes(t, opt.Out)) {
		t.Fatal("canceled extraction changed bytes")
	}
}
