package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsessions"
)

func runProfileDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}
func runProfileSource(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../agentfile/testdata/greeter.agent.md")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(runProfileDir(t), "greeter.agent.md")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
func TestAgentRunProfileExportOfflineAndExclusive(t *testing.T) {
	source := runProfileSource(t)
	out := filepath.Join(runProfileDir(t), "profile.json")
	a := &App{} // No Cfg, Runner or preview/model provider is needed.
	summary, err := a.ExportAgentRunProfile(t.Context(), AgentRunProfileExportOptions{File: source, Out: out})
	if runtime.GOOS == "windows" {
		if !errors.Is(err, errors.ErrUnsupported) {
			t.Fatalf("unsafe native Windows publication: %v", err)
		}
		if _, err := os.Stat(out); !os.IsNotExist(err) {
			t.Fatal("native Windows created output")
		}
		return
	}
	if err != nil || summary.TargetExamples != "not-run" || summary.AuthoredModel != "fake" || summary.ExampleCount != 1 {
		t.Fatalf("export: %+v %v", summary, err)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"system_prompt"`)) || bytes.Contains(raw, []byte("sourcePath")) {
		t.Fatal("wrong exported content")
	}
	var profile struct {
		Instructions  string          `json:"instructions"`
		SessionConfig json.RawMessage `json:"sessionConfig"`
	}
	if err := json.Unmarshal(raw, &profile); err != nil {
		t.Fatal(err)
	}
	want, err := agentsessions.ConfigDigest(profile.Instructions)
	if err != nil {
		t.Fatal(err)
	}
	bound := append([]byte("kmx.agentsessions.chat-config/v1\x00"), profile.SessionConfig...)
	if got := fmt.Sprintf("sha256:%x", sha256.Sum256(bound)); got != want {
		t.Fatal("exported config differs from the existing Sessions client encoding")
	}
	if info, err := os.Stat(out); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("output not private")
	}
	_, err = a.ExportAgentRunProfile(t.Context(), AgentRunProfileExportOptions{File: source, Out: out})
	if err == nil {
		t.Fatal("existing output overwritten")
	}
	after, err := os.ReadFile(out)
	if err != nil || !bytes.Equal(raw, after) {
		t.Fatal("exclusive output changed")
	}
	entries, err := os.ReadDir(filepath.Dir(out))
	if err != nil || len(entries) != 1 {
		t.Fatal("unowned state/staging residue")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(source), "workspace.yaml")); !os.IsNotExist(err) {
		t.Fatal("discovered/created workspace")
	}
}
func TestAgentRunProfileExportRefusalsBeforeOutput(t *testing.T) {
	source := runProfileSource(t)
	for _, kind := range []string{"missing-source", "invalid-source", "same-path", "private-parent", "symlink-parent", "traversal", "cancel", "empty-out"} {
		t.Run(kind, func(t *testing.T) {
			if runtime.GOOS == "windows" && kind == "symlink-parent" {
				t.Skip("native Windows output already refuses")
			}
			dir := runProfileDir(t)
			out := filepath.Join(dir, "profile.json")
			input := source
			ctx := t.Context()
			switch kind {
			case "missing-source":
				input = filepath.Join(dir, "missing")
			case "invalid-source":
				input = filepath.Join(dir, "bad.agent.md")
				if err := os.WriteFile(input, []byte("PRIVATE malformed source"), 0600); err != nil {
					t.Fatal(err)
				}
			case "same-path":
				out = input
			case "private-parent":
				if err := os.Chmod(dir, 0755); err != nil {
					t.Fatal(err)
				}
			case "symlink-parent":
				alias := filepath.Join(runProfileDir(t), "alias")
				if err := os.Symlink(dir, alias); err != nil {
					t.Fatal(err)
				}
				out = filepath.Join(alias, "profile.json")
			case "traversal":
				out = dir + string(filepath.Separator) + ".." + string(filepath.Separator) + filepath.Base(dir) + string(filepath.Separator) + "profile.json"
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "empty-out":
				out = ""
			}
			before, _ := os.ReadFile(source)
			_, err := (&App{}).ExportAgentRunProfile(ctx, AgentRunProfileExportOptions{File: input, Out: out})
			if err == nil || strings.Contains(err.Error(), "PRIVATE") {
				t.Fatalf("unsafe refusal: %v", err)
			}
			if kind == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost: %v", err)
			}
			after, readErr := os.ReadFile(source)
			if readErr != nil || !bytes.Equal(before, after) {
				t.Fatal("source overwritten")
			}
			if out != "" && out != source {
				if _, err := os.Stat(out); !os.IsNotExist(err) {
					t.Fatal("refusal published output")
				}
			}
		})
	}
}
func TestAgentRunProfileExportPublicationUncertainty(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("native Windows publication refuses")
	}
	out := filepath.Join(runProfileDir(t), "profile.json")
	e, err := preflightDetachedExport(t.Context(), out, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.directory.Close(); err != nil {
		t.Fatal(err)
	}
	err = publishAgentRunProfile(t.Context(), e, []byte("complete\n"))
	if err == nil || !strings.Contains(err.Error(), "output may exist") || strings.Contains(err.Error(), "receipt") {
		t.Fatalf("incorrect uncertainty: %v", err)
	}
	raw, err := os.ReadFile(out)
	if err != nil || string(raw) != "complete\n" {
		t.Fatal("expected committed artifact after sync uncertainty")
	}
}
