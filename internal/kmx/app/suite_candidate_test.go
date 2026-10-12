package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
)

func candidateWorkspace(t *testing.T) (string, string, *workspace.Prepared) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "workspace")
	state := filepath.Join(t.TempDir(), "state")
	t.Setenv("KMX_HOME", state)
	input := filepath.Join(t.TempDir(), "instructions.md")
	if e := os.WriteFile(input, []byte("public instructions"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := workspace.Create(t.Context(), root, workspace.CreateOptions{Name: "sample", Model: "model-one", InstructionsFile: input}); e != nil {
		t.Fatal(e)
	}
	p, e := workspace.Prepare(t.Context(), root, filepath.Join(state, "suites"))
	if e != nil {
		t.Fatal(e)
	}
	return root, state, p
}

func pinCandidate(t *testing.T, root, state string, p *workspace.Prepared) {
	t.Helper()
	lock, e := lockSuiteCandidate(root, state)
	if e != nil {
		t.Fatal(e)
	}
	defer lock.Close()
	if e := lock.pin(t.Context(), p); e != nil {
		t.Fatal(e)
	}
}

// A restarted publisher must never Prepare a replacement for a pinned subject.
func TestSuiteCandidateRestartDriftAndExplicitRepin(t *testing.T) {
	for _, field := range []string{"instructions", "case", "model"} {
		t.Run(field, func(t *testing.T) {
			root, state, p := candidateWorkspace(t)
			pinCandidate(t, root, state, p)
			var out bytes.Buffer
			a := &App{Out: &out}
			layout := filepath.Join(t.TempDir(), "layout")
			r, e := a.PushSuiteWorkspace(t.Context(), root, "team:v1", layout, false, false, SuitePushProvenance{})
			if e != nil || r.Descriptor.Digest != p.Descriptor.Digest {
				t.Fatalf("same S publication %v %v", r, e)
			}
			switch field {
			case "instructions":
				if e := os.WriteFile(filepath.Join(root, "agents/sample/instructions.md"), []byte("changed instructions"), 0600); e != nil {
					t.Fatal(e)
				}
			case "case":
				if e := os.WriteFile(filepath.Join(root, "eval/example.yaml"), []byte("id: edited\ninput: public edited\nexpectContains: [edited]\n"), 0600); e != nil {
					t.Fatal(e)
				}
			case "model":
				path := filepath.Join(root, "agents/sample/member.yaml")
				b, _ := os.ReadFile(path)
				if e := os.WriteFile(path, bytes.ReplaceAll(b, []byte("model-one"), []byte("model-two")), 0600); e != nil {
					t.Fatal(e)
				}
			}
			t.Run("new-process-refusal", func(t *testing.T) {
				c := exec.Command(os.Args[0], "-test.run=TestSuiteCandidateRestartPublisherHelper", "-test.count=1")
				c.Env = append(os.Environ(), "KMX_CANDIDATE_RESTART_HELPER=1", "KMX_CANDIDATE_ROOT="+root)
				if b, e := c.CombinedOutput(); e != nil {
					t.Fatalf("restarted publisher: %v %s", e, b)
				}
			})
			freshLayout := filepath.Join(t.TempDir(), "layout")
			if _, e := (&App{Out: &out}).PushSuiteWorkspace(t.Context(), root, "team:v2", freshLayout, false, false, SuitePushProvenance{}); e == nil || !strings.Contains(e.Error(), "rerun evaluation") {
				t.Fatalf("drift %v", e)
			}
			if _, e := os.Stat(freshLayout); !os.IsNotExist(e) {
				t.Fatal("drift transferred")
			}
			next, e := workspace.Prepare(t.Context(), root, filepath.Join(state, "suites"))
			if e != nil {
				t.Fatal(e)
			}
			pinCandidate(t, root, state, next)
			r, e = a.PushSuiteWorkspace(t.Context(), root, "team:v2", freshLayout, false, false, SuitePushProvenance{})
			if e != nil || r.Descriptor.Digest != next.Descriptor.Digest || r.Descriptor.Digest == p.Descriptor.Digest {
				t.Fatalf("repin %v %v", r, e)
			}
		})
	}
}

func TestSuiteCandidateCorruptionAndWriterExclusion(t *testing.T) {
	root, state, p := candidateWorkspace(t)
	pinCandidate(t, root, state, p)
	lock, e := lockSuiteCandidate(root, state)
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var out bytes.Buffer
			_, e := (&App{Out: &out}).PushSuiteWorkspace(context.Background(), root, "team:v1", filepath.Join(state, "transfer"), false, false, SuitePushProvenance{})
			if e == nil {
				t.Error("concurrent writer bypassed candidate lock")
			}
		}()
	}
	wg.Wait()
	if e := lock.Close(); e != nil {
		t.Fatal(e)
	}
	lock, e = lockSuiteCandidate(root, state)
	if e != nil {
		t.Fatal(e)
	}
	got, e := lock.load(t.Context())
	if e != nil || got == nil || got.Descriptor.Digest != p.Descriptor.Digest {
		t.Fatalf("pin %v %v", got, e)
	}
	pinPath := filepath.Join(lock.path, lock.key+".json")
	_ = lock.Close()
	raw, e := os.ReadFile(pinPath)
	if e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(raw, []byte(root)) || bytes.Contains(raw, []byte("public instructions")) {
		t.Fatal("pin stores source path/content")
	}
	for _, corruption := range []string{"json", "artifact", "permissions", "descriptor"} {
		t.Run(corruption, func(t *testing.T) {
			if e := os.WriteFile(pinPath, raw, 0600); e != nil {
				t.Fatal(e)
			}
			if e := os.Chmod(pinPath, 0600); e != nil {
				t.Fatal(e)
			}
			switch corruption {
			case "json":
				_ = os.WriteFile(pinPath, []byte(`{"unknown":"PRIVATE_PIN_CANARY"}`), 0600)
			case "permissions":
				_ = os.Chmod(pinPath, 0644)
			case "descriptor":
				var value map[string]any
				_ = json.Unmarshal(raw, &value)
				value["descriptor"].(map[string]any)["size"] = 1
				bad, _ := json.Marshal(value)
				_ = os.WriteFile(pinPath, bad, 0600)
			case "artifact":
				blob := filepath.Join(p.Path, "blobs/sha256", p.Descriptor.Digest.Encoded())
				original, e := os.ReadFile(blob)
				if e != nil {
					t.Fatal(e)
				}
				if e := os.Chmod(blob, 0600); e != nil {
					t.Fatal(e)
				}
				if e := os.WriteFile(blob, []byte("corrupt"), 0600); e != nil {
					t.Fatal(e)
				}
				defer func() {
					if e := os.WriteFile(blob, original, 0600); e != nil {
						t.Fatal(e)
					}
				}()
			}
			var out bytes.Buffer
			layout := filepath.Join(t.TempDir(), "layout")
			_, e := (&App{Out: &out}).PushSuiteWorkspace(t.Context(), root, "team:v1", layout, false, false, SuitePushProvenance{})
			if e == nil || strings.Contains(e.Error(), "PRIVATE_PIN_CANARY") {
				t.Fatalf("corruption %v", e)
			}
			if _, e := os.Stat(layout); !os.IsNotExist(e) {
				t.Fatal("corrupt pin transferred")
			}
		})
	}
}

func TestSuiteCandidateRestartPublisherHelper(t *testing.T) {
	if os.Getenv("KMX_CANDIDATE_RESTART_HELPER") != "1" {
		return
	}
	root := os.Getenv("KMX_CANDIDATE_ROOT")
	layout := filepath.Join(t.TempDir(), "layout")
	_, e := (&App{Out: io.Discard}).PushSuiteWorkspace(t.Context(), root, "team:v1", layout, false, false, SuitePushProvenance{})
	if e == nil || !strings.Contains(e.Error(), "rerun evaluation") {
		t.Fatalf("new process failed to honor pin: %v", e)
	}
	if _, e := os.Stat(layout); !os.IsNotExist(e) {
		t.Fatal("new process transferred drifted source")
	}
}

func TestSuiteCandidatePublicationHoldsWriterThroughRegistryOperation(t *testing.T) {
	root, state, p := candidateWorkspace(t)
	pinCandidate(t, root, state, p)
	manifest, e := os.ReadFile(filepath.Join(p.Path, "blobs/sha256", p.Descriptor.Digest.Encoded()))
	if e != nil {
		t.Fatal(e)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	var writes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/" {
			w.WriteHeader(200)
			return
		}
		if r.Method != "HEAD" && r.Method != "GET" {
			writes.Add(1)
			w.WriteHeader(500)
			return
		}
		once.Do(func() { close(entered); <-release })
		w.Header().Set("Content-Type", p.Descriptor.MediaType)
		w.Header().Set("Docker-Content-Digest", p.Descriptor.Digest.String())
		w.Header().Set("Content-Length", fmt.Sprint(len(manifest)))
		if r.Method == "GET" {
			_, _ = w.Write(manifest)
		} else {
			w.WriteHeader(200)
		}
	}))
	defer server.Close()
	done := make(chan error, 1)
	go func() {
		_, e := (&App{Out: io.Discard}).PushSuiteWorkspace(context.Background(), root, strings.TrimPrefix(server.URL, "http://")+"/team/suite:v1", "", true, false, SuitePushProvenance{})
		done <- e
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		close(release)
		t.Fatal("publication never reached registry")
	}
	lock, e := lockSuiteCandidate(root, state)
	if e == nil {
		_ = lock.Close()
		close(release)
		t.Fatal("publication released candidate lock before registry completion")
	}
	close(release)
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("publisher did not finish")
	}
	if writes.Load() != 0 {
		t.Fatal("same-S publication unexpectedly wrote tag")
	}
	lock, e = lockSuiteCandidate(root, state)
	if e != nil {
		t.Fatal(e)
	}
	defer lock.Close()
	got, e := lock.load(t.Context())
	if e != nil || got.Descriptor.Digest != p.Descriptor.Digest {
		t.Fatal("publication spliced candidate")
	}
}

func TestSuiteCandidatePublisherReportsLateLockFailure(t *testing.T) {
	root, state, p := candidateWorkspace(t)
	pinCandidate(t, root, state, p)
	manifest, e := os.ReadFile(filepath.Join(p.Path, "blobs/sha256", p.Descriptor.Digest.Encoded()))
	if e != nil {
		t.Fatal(e)
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(root)))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/" {
			w.WriteHeader(200)
			return
		}
		if e := os.Chmod(filepath.Join(state, "suite-candidates", key+".lock"), 0644); e != nil {
			t.Error(e)
		}
		w.Header().Set("Content-Type", p.Descriptor.MediaType)
		w.Header().Set("Docker-Content-Digest", p.Descriptor.Digest.String())
		w.Header().Set("Content-Length", fmt.Sprint(len(manifest)))
		if r.Method == "GET" {
			_, _ = w.Write(manifest)
		} else {
			w.WriteHeader(200)
		}
	}))
	defer server.Close()
	var out bytes.Buffer
	_, e = (&App{Out: &out}).PushSuiteWorkspace(t.Context(), root, strings.TrimPrefix(server.URL, "http://")+"/team/suite:v1", "", true, false, SuitePushProvenance{})
	if e == nil || !strings.Contains(e.Error(), "writer exclusion changed") || out.Len() != 0 {
		t.Fatalf("late writer failure reported success: %v %s", e, &out)
	}
	if _, e := os.Stat(filepath.Join(state, "suite-candidates", key+".lock")); e != nil {
		t.Fatal("unsafe lock was destructively removed")
	}
}

// The same private on-disk exclusion works in another process, not just a Go mutex.
func TestSuiteCandidateProcessLock(t *testing.T) {
	if os.Getenv("KMX_CANDIDATE_LOCK_HELPER") == "1" {
		lock, e := lockSuiteCandidate(os.Getenv("KMX_CANDIDATE_ROOT"), os.Getenv("KMX_HOME"))
		if e == nil {
			_ = lock.Close()
			t.Fatal("child acquired active lock")
		}
		return
	}
	root, state, _ := candidateWorkspace(t)
	lock, e := lockSuiteCandidate(root, state)
	if e != nil {
		t.Fatal(e)
	}
	defer lock.Close()
	c := exec.Command(os.Args[0], "-test.run=TestSuiteCandidateProcessLock", "-test.count=1")
	c.Env = append(os.Environ(), "KMX_CANDIDATE_LOCK_HELPER=1", "KMX_CANDIDATE_ROOT="+root)
	if b, e := c.CombinedOutput(); e != nil {
		t.Fatalf("process exclusion %v %s", e, b)
	}
}
