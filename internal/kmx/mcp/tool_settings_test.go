package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
)

func TestToolStateResourceRevisionAndSDKCall(t *testing.T) {
	files, root, source := canonicalFiles(t)
	source = strings.Replace(source, "## Examples", "## Tools\n- ping\n\n## Examples", 1)
	saved, err := files.Create(t.Context(), "greeter.agent.md", []byte(source))
	if err != nil {
		t.Fatal(err)
	}
	cs := connectFixture(t, Operations(files, &app.App{}, nil), func(s *sdk.Server) { AddAgentResource(s, files) })
	view := readView(t, cs, saved.Path)
	if view.ToolSettingsRevision != "absent" {
		t.Fatalf("missing settings revision: %+v", view)
	}
	result, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: "set_tool_state", Arguments: map[string]any{"schemaVersion": 1, "path": saved.Path, "expectedRevision": saved.Revision, "expectedSettingsRevision": view.ToolSettingsRevision, "name": "ping", "state": "Disabled"}})
	if err != nil || result.IsError {
		t.Fatalf("setter: %+v %v", result, err)
	}
	var out ToolStateResult
	raw, _ := json.Marshal(result.StructuredContent)
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	current := readView(t, cs, saved.Path)
	if current.ToolSettingsRevision != out.ToolSettingsRevision || current.ToolSettings[0].State != localtools.Disabled {
		t.Fatalf("readback: %+v %+v", current, out)
	}
	body, err := os.ReadFile(filepath.Join(root, "kmx.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "kmx.yaml"), append([]byte("# unrelated change\n"), body...), 0600); err != nil {
		t.Fatal(err)
	}
	if readView(t, cs, saved.Path).ToolSettingsRevision == current.ToolSettingsRevision {
		t.Fatal("comment edit did not change byte token")
	}
	// The child never advertises the fifth operation and refuses a malicious direct call.
	ops := Operations(files, &app.App{}, nil)
	delete(ops, "set_tool_state")
	server, err := NewServer("fixture", ops, PreviewServer)
	if err != nil {
		t.Fatal(err)
	}
	a, b := sdk.NewInMemoryTransports()
	ss, err := server.Connect(t.Context(), a, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	client := sdk.NewClient(&sdk.Implementation{Name: "fixture", Version: "1"}, nil)
	child, err := client.Connect(t.Context(), b, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer child.Close()
	list, err := child.ListTools(t.Context(), nil)
	if err != nil || len(list.Tools) != 4 {
		t.Fatalf("child discovery: %+v %v", list, err)
	}
	if _, err := child.CallTool(t.Context(), &sdk.CallToolParams{Name: "set_tool_state", Arguments: map[string]any{"schemaVersion": 1}}); err == nil {
		t.Fatal("child dispatched writer")
	}
}

func TestSetToolStateRefusesInvalidStaleAndUndeclared(t *testing.T) {
	files, root, source := canonicalFiles(t)
	source = strings.Replace(source, "## Examples", "## Tools\n- ping\n\n## Examples", 1)
	saved, err := files.Create(t.Context(), "greeter.agent.md", []byte(source))
	if err != nil {
		t.Fatal(err)
	}
	base := ToolStateInput{SchemaVersion: 1, Path: saved.Path, ExpectedRevision: saved.Revision, ExpectedSettingsRevision: "absent", Name: "ping", State: localtools.Disabled}
	for _, tc := range []struct {
		name, code string
		mutate     func(*ToolStateInput)
	}{
		{"source", "stale_revision", func(i *ToolStateInput) { i.ExpectedRevision = "sha256:" + strings.Repeat("0", 64) }},
		{"settings", "stale_settings_revision", func(i *ToolStateInput) { i.ExpectedSettingsRevision = "sha256:" + strings.Repeat("0", 64) }},
		{"undeclared", "undeclared_tool", func(i *ToolStateInput) { i.Name = "other" }},
		{"ask", "invalid_options", func(i *ToolStateInput) { i.State = "Ask" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i := base
			tc.mutate(&i)
			out := files.SetToolState(t.Context(), i)
			if out.ReasonCode != tc.code || out.MayHaveWritten {
				t.Fatalf("refusal: %+v", out)
			}
		})
	}
	cfg := filepath.Join(root, "kmx.yaml")
	for _, body := range []string{"tools: {ping: Ask}", "tools: {ping: Disabled, ping: Enabled}", ""} {
		if err := os.WriteFile(cfg, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		in := base
		in.ExpectedSettingsRevision = revision([]byte(body))
		out := files.SetToolState(t.Context(), in)
		after, _ := os.ReadFile(cfg)
		if out.ReasonCode != "invalid_tool_settings" || out.MayHaveWritten || string(after) != body {
			t.Fatalf("invalid changed: %+v %s", out, after)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	out := files.SetToolState(ctx, base)
	if out.ReasonCode != "cancelled" || out.MayHaveWritten {
		t.Fatalf("cancel: %+v", out)
	}
}

func TestSetToolStateUnknownPublicationIsTruthful(t *testing.T) {
	files, root, source := canonicalFiles(t)
	source = strings.Replace(source, "## Examples", "## Tools\n- ping\n\n## Examples", 1)
	saved, err := files.Create(t.Context(), "greeter.agent.md", []byte(source))
	if err != nil {
		t.Fatal(err)
	}
	in := ToolStateInput{SchemaVersion: 1, Path: saved.Path, ExpectedRevision: saved.Revision, ExpectedSettingsRevision: "absent", Name: "ping", State: localtools.Disabled}
	out := files.setToolState(t.Context(), in, func(parent *os.Root, staged, target string, existing bool) error {
		// Exercise visibility followed by an error, including the partial-copy possibility.
		if err := parent.WriteFile(target, []byte("tools:"), 0600); err != nil {
			t.Fatal(err)
		}
		return errors.New("private raw failure must never escape")
	})
	if out.State != "unknown" || !out.MayHaveWritten || out.Changed != nil || out.ToolSettingsRevision != "" || out.ToolSettings != nil || out.ReasonCode != "settings_write_unknown" || strings.Contains(out.Reason, "private raw") {
		t.Fatalf("invented outcome: %+v", out)
	}
	leftovers, _ := filepath.Glob(filepath.Join(root, ".kmx-mcp-*.tmp"))
	if len(leftovers) != 0 {
		t.Fatal("left staged data")
	}
}

type settingsRecheckContext struct {
	context.Context
	staged func() bool
	mutate func()
	once   atomic.Bool
}

func (c *settingsRecheckContext) Err() error {
	if c.staged() && c.once.CompareAndSwap(false, true) {
		c.mutate()
	}
	return c.Context.Err()
}

func TestSetToolStateRechecksSourceConfigAndUnboundParent(t *testing.T) {
	for _, change := range []string{"source", "config", "parent"} {
		t.Run(change, func(t *testing.T) {
			original, root, source := canonicalFiles(t)
			source = strings.Replace(source, "## Examples", "## Tools\n- ping\n\n## Examples", 1)
			saved, err := original.Create(t.Context(), "greeter.agent.md", []byte(source))
			if err != nil {
				t.Fatal(err)
			}
			files, err := OpenFiles("", original.validate)
			if err != nil {
				t.Fatal(err)
			}
			defer files.Close()
			in := ToolStateInput{SchemaVersion: 1, Path: filepath.Join(root, saved.Path), ExpectedRevision: saved.Revision, ExpectedSettingsRevision: "absent", Name: "ping", State: localtools.Disabled}
			parentPinned := false
			ctx := &settingsRecheckContext{Context: t.Context(), staged: func() bool {
				matches, _ := filepath.Glob(filepath.Join(root, ".kmx-mcp-*.tmp"))
				return len(matches) > 0
			}, mutate: func() {
				switch change {
				case "source":
					if err := os.WriteFile(in.Path, []byte(source+"\n"), 0600); err != nil {
						t.Fatal(err)
					}
				case "config":
					if err := os.WriteFile(filepath.Join(root, "kmx.yaml"), []byte("# other edit\ntools: {ping: Enabled}\n"), 0600); err != nil {
						t.Fatal(err)
					}
				case "parent":
					if err := os.Rename(root, root+"-moved"); err != nil {
						// Windows OpenRoot uses a handle without FILE_SHARE_DELETE.
						// Assert that protection instead of requiring an impossible swap.
						const sharingViolation = syscall.Errno(32)
						if runtime.GOOS == "windows" && errors.Is(err, sharingViolation) {
							parentPinned = true
							return
						}
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = os.Rename(root+"-moved", root) })
					if err := os.Mkdir(root, 0700); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = os.Remove(root) })
				}
			}}
			out := files.SetToolState(ctx, in)
			if parentPinned {
				if !ctx.once.Load() || out.State != "complete" || out.Changed == nil || !*out.Changed {
					t.Fatalf("pinned parent blocked valid write: %+v", out)
				}
				if _, err := os.Stat(root + "-moved"); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("blocked parent moved: %v", err)
				}
				return
			}
			want := "stale_revision"
			if change == "config" {
				want = "stale_settings_revision"
			}
			if !ctx.once.Load() || out.ReasonCode != want || out.MayHaveWritten {
				t.Fatalf("missed %s recheck: %+v", change, out)
			}
		})
	}
}

func TestSetToolStateUnboundAndHardlinkRefusal(t *testing.T) {
	original, root, source := canonicalFiles(t)
	source = strings.Replace(source, "## Examples", "## Tools\n- ping\n\n## Examples", 1)
	saved, err := original.Create(t.Context(), "greeter.agent.md", []byte(source))
	if err != nil {
		t.Fatal(err)
	}
	files, err := OpenFiles("", original.validate)
	if err != nil {
		t.Fatal(err)
	}
	defer files.Close()
	in := ToolStateInput{SchemaVersion: 1, Path: filepath.Join(root, saved.Path), ExpectedRevision: saved.Revision, ExpectedSettingsRevision: "absent", Name: "ping", State: localtools.Disabled}
	out := files.SetToolState(t.Context(), in)
	if out.State != "complete" {
		t.Fatalf("unbound: %+v", out)
	}
	cfg := filepath.Join(root, "kmx.yaml")
	if err := os.Link(cfg, filepath.Join(root, "copy.yaml")); err != nil {
		t.Skip("hard links unavailable")
	}
	in.ExpectedSettingsRevision = out.ToolSettingsRevision
	in.State = localtools.Enabled
	out = files.SetToolState(t.Context(), in)
	if out.ReasonCode != "settings_unavailable" || out.MayHaveWritten {
		t.Fatalf("hardlink accepted: %+v", out)
	}
}

func TestSetToolStateConcurrentAbsentCAS(t *testing.T) {
	files, _, source := canonicalFiles(t)
	source = strings.Replace(source, "## Examples", "## Tools\n- ping\n\n## Examples", 1)
	saved, err := files.Create(t.Context(), "greeter.agent.md", []byte(source))
	if err != nil {
		t.Fatal(err)
	}
	in := ToolStateInput{SchemaVersion: 1, Path: saved.Path, ExpectedRevision: saved.Revision, ExpectedSettingsRevision: "absent", Name: "ping", State: localtools.Disabled}
	results := make(chan ToolStateResult, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; results <- files.SetToolState(t.Context(), in) }()
	}
	close(start)
	wg.Wait()
	close(results)
	complete := 0
	for out := range results {
		if out.State == "complete" {
			complete++
		} else if out.ReasonCode != "stale_settings_revision" || out.MayHaveWritten {
			t.Fatalf("wrong loser: %+v", out)
		}
	}
	if complete != 1 {
		t.Fatalf("%d writers won", complete)
	}
}

func TestToolStateOriginalAndPreviewRolesAreClosed(t *testing.T) {
	ops := protocolFixture()
	delete(ops, "set_tool_state")
	if _, err := NewServer("fixture", ops, PreviewServer); err != nil {
		t.Fatal(err)
	}
	if _, err := NewServer("fixture", ops, OriginalServer); err == nil {
		t.Fatal("original silently omitted writer")
	}
	ops["set_tool_state"] = ToolStateOperation(nil)
	if _, err := NewServer("fixture", ops, OriginalServer); err != nil {
		t.Fatal(err)
	}
	if _, err := NewServer("fixture", ops, PreviewServer); err == nil {
		t.Fatal("preview accepted writer authority")
	}
	if _, err := NewServer("fixture", ops, ServerRole(99)); err == nil {
		t.Fatal("unknown role accepted")
	}
}

func TestSetToolStateCASCreationNoopAndPreservation(t *testing.T) {
	files, root, source := canonicalFiles(t)
	source = strings.Replace(source, "## Examples", "## Tools\n- ping\n\n## Examples", 1)
	agent, err := files.Create(t.Context(), "greeter.agent.md", []byte(source))
	if err != nil {
		t.Fatal(err)
	}
	in := ToolStateInput{SchemaVersion: 1, Path: agent.Path, ExpectedRevision: agent.Revision, ExpectedSettingsRevision: "absent", Name: "ping", State: localtools.Enabled}
	out := files.SetToolState(t.Context(), in)
	if out.State != "complete" || out.Changed == nil || *out.Changed || out.MayHaveWritten || out.ToolSettingsRevision != "absent" {
		t.Fatalf("noop: %+v", out)
	}
	cfg := filepath.Join(root, "kmx.yaml")
	if _, err := os.Stat(cfg); !os.IsNotExist(err) {
		t.Fatal("noop created config")
	}
	in.State = localtools.Disabled
	out = files.SetToolState(t.Context(), in)
	if out.State != "complete" || out.Changed == nil || !*out.Changed || !out.MayHaveWritten || out.ToolSettings[0].State != localtools.Disabled {
		t.Fatalf("create: %+v", out)
	}
	if _, err := localtools.LoadSettings(t.Context(), filepath.Join(root, agent.Path), []string{"ping"}); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if _, err := config.LoadDestinations(root); err != nil {
			t.Fatalf("created incompatible config: %v", err)
		}
	}
	stale := files.SetToolState(t.Context(), in)
	if stale.ReasonCode != "stale_settings_revision" || stale.MayHaveWritten {
		t.Fatalf("stale: %+v", stale)
	}
	body := []byte("# keep\nversion: 1\nother: {nested: [one, two]}\ntools: {ping: Disabled, another_agent: Disabled}\n")
	if err := os.WriteFile(cfg, body, 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(cfg, 0640); err != nil {
		t.Fatal(err)
	}
	in.ExpectedSettingsRevision = revision(body)
	in.State = localtools.Enabled
	out = files.SetToolState(t.Context(), in)
	if out.State != "complete" || out.ToolSettings[0].State != localtools.Enabled {
		t.Fatalf("edit: %+v", out)
	}
	after, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"# keep", "nested: [one, two]", "another_agent: Disabled", "ping: Enabled"} {
		if !strings.Contains(string(after), s) {
			t.Fatalf("lost %s: %s", s, after)
		}
	}
	info, err := os.Stat(cfg)
	if err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0640 {
		t.Fatalf("lost mode: %v %v", info, err)
	}
	saved, err := os.ReadFile(filepath.Join(root, agent.Path))
	if err != nil || string(saved) != source {
		t.Fatal("agent source mutated")
	}
	in.ExpectedSettingsRevision = out.ToolSettingsRevision
	again := files.SetToolState(t.Context(), in)
	now, err := os.ReadFile(cfg)
	if err != nil || again.State != "complete" || again.Changed == nil || *again.Changed || string(now) != string(after) {
		t.Fatalf("noop changed bytes: %+v %v", again, err)
	}
}
