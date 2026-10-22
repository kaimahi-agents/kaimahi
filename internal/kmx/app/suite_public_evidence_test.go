package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	suitecore "github.com/agent-primitives/suite/core"
	suiteevidence "github.com/agent-primitives/suite/evidence"
	suiteoras "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
)

func publicCommitFixture(t *testing.T, store *suitePassStore, suite *workspace.NativeSuite, raw []byte) {
	t.Helper()
	if err := store.reserve(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := store.publishPublicMetadata(t.Context(), raw, suite, publicMetadataFixture()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.commit(t.Context(), raw, suite); err != nil {
		t.Fatal(err)
	}
}
func TestSuitePublicExportOfflineAndPrivacy(t *testing.T) {
	store, suite, raw := passStoreFixture(t)
	root := store.workspace
	publicCommitFixture(t, store, suite, raw)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "evidence.json")
	var summary bytes.Buffer
	a := &App{Out: &summary, Err: &summary}
	if err := a.ExportSuiteEvaluation(t.Context(), SuiteEvaluationExportOptions{Workspace: root, Member: suite.MemberID, Out: out}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var e suiteevidence.EvaluationEvidence
	if err := suitecore.Unmarshal(data, &e); err != nil {
		t.Fatal(err)
	}
	if e.Execution.Runtime != (suitecore.EvidenceImplementation{ID: "orka", Version: "0.2.0", Digest: "sha256:7c1727f92d5c0cf05d464c6eb9e70b8342cb5a7a87c2e35338f051d7b85aa370"}) {
		t.Fatalf("runtime: %#v", e.Execution.Runtime)
	}
	if e.Execution.Harness != (suitecore.EvidenceImplementation{ID: "orka", Version: "harness-v2", Digest: "sha256:814d7149308e62730df59759c5137667cad04520c94cc50ff56e5028fdc8bcd4"}) {
		t.Fatalf("harness: %#v", e.Execution.Harness)
	}
	if e.Execution.Renderer.ID != "kmx.native-orka" || e.Execution.Renderer.Version != "v2" || e.Execution.Profile.ID != "kmx.orka-harness-v2-tool-free-openai" {
		t.Fatal("identity split", e.Execution)
	}
	// Independently hand-framed source-provenance projection, NOT a binary digest.
	source := []byte(`{"domain":"kmx.go-build-source/v1","producer":{"modified":false,"module":"github.com/kaimahi-agents/kaimahi","revision":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","version":"(devel)"}}`)
	if e.Execution.Renderer.Digest != suitePublicHash(source) || e.Execution.Profile.Digest != suitePublicHash(source) {
		t.Fatal("wrong source identity")
	}
	if e.Evaluator.Identity.Version != "devel-git-"+strings.Repeat("a", 40) || e.Evaluator.StartedAt != "2026-01-01T00:00:00Z" || len(e.Results) != len(suite.Cases) {
		t.Fatal("metadata or coverage", e)
	}
	for _, canary := range []string{nativeCanary, root, "owned-provider", "owned-agent", "task-", "uid-", "localhost", "secretRef", "binaryDigest", strings.TrimPrefix(suite.InstructionsDigest, "sha256:") + "raw"} {
		if strings.Contains(string(data)+summary.String(), canary) {
			t.Fatalf("private data leaked: %q", canary)
		}
	}
	info, _ := os.Stat(out)
	if info.Mode().Perm() != 0600 {
		t.Fatal("public permissions", info.Mode())
	}
	if err := a.ExportSuiteEvaluation(t.Context(), SuiteEvaluationExportOptions{Workspace: root, Member: suite.MemberID, Out: out}); err == nil {
		t.Fatal("clobbered output")
	}
	after, _ := os.ReadFile(out)
	if !bytes.Equal(data, after) {
		t.Fatal("output changed")
	}
}

func TestSuitePublicExportRefusalZeroOutput(t *testing.T) {
	for _, variant := range []string{"legacy", "member", "blank", "workspace-output", "managed-output", "symlink", "malformed", "cancelled"} {
		t.Run(variant, func(t *testing.T) {
			store, suite, raw := passStoreFixture(t)
			root := store.workspace
			if variant == "legacy" {
				commitStoreReceipt(t, store, suite, raw)
			} else {
				publicCommitFixture(t, store, suite, raw)
			}
			if variant == "malformed" {
				entries, _ := os.ReadDir(store.partition)
				for _, e := range entries {
					if strings.HasPrefix(e.Name(), "public-") {
						if err := os.WriteFile(filepath.Join(store.partition, e.Name()), []byte(`{"privateEndpoint":"https://private-canary.invalid/secret"}`), 0600); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			store.Close()
			out := filepath.Join(t.TempDir(), "evidence.json")
			member := suite.MemberID
			switch variant {
			case "member":
				member = "other"
			case "blank":
				member = ""
			case "workspace-output":
				out = filepath.Join(root, "evidence.json")
			case "managed-output":
				out = filepath.Join(store.path, "evidence.json")
			case "symlink":
				target := filepath.Join(t.TempDir(), "target")
				if err := os.Symlink(target, out); err != nil {
					t.Fatal(err)
				}
			}
			ctx := t.Context()
			if variant == "cancelled" {
				c, cancel := context.WithCancel(ctx)
				cancel()
				ctx = c
			}
			var summary bytes.Buffer
			err := (&App{Out: &summary}).ExportSuiteEvaluation(ctx, SuiteEvaluationExportOptions{Workspace: root, Member: member, Out: out})
			if err == nil {
				t.Fatal("invalid export accepted")
			}
			if strings.Contains(err.Error(), root) || strings.Contains(err.Error(), "private-canary") || summary.Len() != 0 {
				t.Fatal("unsafe refusal", err, summary.String())
			}
			if variant != "symlink" {
				if _, err := os.Lstat(out); !os.IsNotExist(err) {
					t.Fatal("refusal created output", err)
				}
			}
		})
	}
}

func TestSuitePublicRuntimeIdentityMappings(t *testing.T) {
	// The kagent fixture uses qualified inventory and actual private constructors.
	receipts := suiteDiffFixtures(t)
	for _, mode := range []string{"native-kagent", "native-orka"} {
		t.Run(mode, func(t *testing.T) {
			r := receipts[mode]
			runtime, harness, err := suitePublicRuntime(r)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "native-kagent" {
				if runtime.ID != "kagent" || runtime.Version != "v0.10.3" || runtime.Digest != "sha256:b2572a59386b6bac1f5e652e135fdad54c0ee603d3f9ee60eb7c80bbaedf9b76" || harness.ID != "kagent-go" || harness.Digest != "sha256:5ee9592cb7d959538d0f0fe36acf31ba3a06de084f84048b1459c3bdfafcda1f" {
					t.Fatal(runtime, harness)
				}
				r.Kagent.Observation.Images[0].Digest = strings.Repeat("f", 64)
				if _, _, err := suitePublicRuntime(r); err == nil {
					t.Fatal("unqualified runtime accepted")
				}
			}
		})
	}
}

func TestSuitePublicSessionsRuntime(t *testing.T) {
	p, subject, observations := sessionsPassingFixture(t)
	r, err := newSuiteSessionsPassingReceipt(t.Context(), p, subject, observations)
	if err != nil {
		t.Fatal(err)
	}
	runtime, harness, err := suitePublicRuntime(r)
	if err != nil || runtime.Version != "ca85f47a2ec17ca6e16029cfa69419cfe29442b3" || runtime.Digest != "sha256:"+strings.Repeat("a", 64) || harness.ID != "chat" || harness.Version != "1" || harness.Digest != runtime.Digest {
		t.Fatal(runtime, harness, err)
	}
	artifact, err := suiteoras.ReadArtifactLayout(t.Context(), p.prepared.Path, p.suite.Digest)
	if err != nil {
		t.Fatal(err)
	}
	e, expected, err := suitePublicEvidence(t.Context(), artifact, p.suite, r, publicMetadataFixture())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := suitecore.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := suiteevidence.VerifyEvaluation(p.prepared.Path, expected, bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
}

func TestSuitePublicImplementationLabels(t *testing.T) {
	for _, label := range []string{"kmx.native-orka/v2", "kmx.native-kagent/v2", "kmx.native-agentsessions/v2", "kmx.kagent-v0.10.3-go-linux-amd64-tool-free-openai/v1", "kmx.agentsessions-chat-system-prompt-tool-free-openai/v1", "kmx.orka-harness-v2-tool-free-openai/v1"} {
		impl, err := suitePublicImplementation(label, "sha256:"+strings.Repeat("a", 64))
		if err != nil {
			t.Fatal(err)
		}
		parts := strings.Split(label, "/")
		if impl.ID != parts[0] || impl.Version != parts[1] {
			t.Fatal(impl)
		}
	}
	for _, label := range []string{"no-version", "a/b/c", "host.invalid/v1", "model:private/v1", "a/v1+meta"} {
		if _, err := suitePublicImplementation(label, "sha256:"+strings.Repeat("a", 64)); err == nil {
			t.Fatal("unsupported label accepted", label)
		}
	}
}

func TestSuitePublicPublisherFinalFenceSwap(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "parent")
	if err := os.Mkdir(parent, 0755); err != nil {
		t.Fatal(err)
	}
	e, err := preflightSuitePublicExport(t.Context(), filepath.Join(parent, "evidence.json"), t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	e.beforeLink = func() error {
		if err := os.Rename(parent, parent+"-old"); err != nil {
			return err
		}
		return os.Mkdir(parent, 0755)
	}
	if err := e.publishBytes(t.Context(), []byte("public")); err == nil {
		t.Fatal("final fence parent swap published into stale anchor")
	}
	if _, err := os.Stat(filepath.Join(parent+"-old", "evidence.json")); !os.IsNotExist(err) {
		t.Fatal("output visible after final fence swap")
	}
}

func TestSuitePublicPublisherMode(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0755)
	out := filepath.Join(dir, "public.json")
	if e, err := preflightSuitePassingExport(t.Context(), out, t.TempDir(), t.TempDir()); err == nil {
		e.Close()
		t.Fatal("private default weakened")
	}
	e, err := preflightSuitePublicExport(t.Context(), out, t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if err := e.publishBytes(t.Context(), []byte("public")); err != nil {
		t.Fatal(err)
	}
	// A directory rename after preflight must not publish to the stale anchor.
	swapped := filepath.Join(t.TempDir(), "parent")
	os.Mkdir(swapped, 0755)
	e2, err := preflightSuitePublicExport(t.Context(), filepath.Join(swapped, "evidence.json"), t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer e2.Close()
	os.Rename(swapped, swapped+"-old")
	os.Mkdir(swapped, 0755)
	if err := e2.publishBytes(t.Context(), []byte("public")); err == nil {
		t.Fatal("swapped parent accepted")
	}
	if _, err := os.Stat(filepath.Join(swapped+"-old", "evidence.json")); !os.IsNotExist(err) {
		t.Fatal("published into stale parent")
	}
}
