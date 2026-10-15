package workspace_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	suiteoras "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
)

const sessionsVersion = "ca85f47a2ec17ca6e16029cfa69419cfe29442b3"
const sessionsDeclaration = "runtime: {id: agentsessions, version: " + sessionsVersion + ", engine: chat}\n"

func TestAgentSessionsProfileBindsSAndTransfersExactInventory(t *testing.T) {
	root, cache := createWorkspace(t)
	before, err := workspace.Prepare(t.Context(), root, cache)
	if err != nil {
		t.Fatal(err)
	}
	orka, err := workspace.Load(t.Context(), before)
	if err != nil {
		t.Fatal(err)
	}
	setMemberRuntime(t, root, sessionsDeclaration)
	writeFile(t, filepath.Join(root, "eval/second.yml"), "id: second\ninput: Question\nexpectContains: [answer]\n")
	if workspace.CheckCurrent(t.Context(), root, before) == nil {
		t.Fatal("runtime edit retained old fingerprint")
	}
	p, err := workspace.Prepare(t.Context(), root, cache)
	if err != nil {
		t.Fatal(err)
	}
	member, err := workspace.Load(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	want := workspace.RuntimeProfile{ID: "agentsessions", Version: sessionsVersion, Engine: "chat"}
	if member.Runtime != want || member.RendererVersion != "kmx.native-agentsessions/v2" || member.ExecutionProfile != "kmx.agentsessions-chat-system-prompt-tool-free-openai/v1" || member.RequiredImages == nil || len(member.RequiredImages) != 0 || len(member.Cases) != 2 {
		t.Fatalf("wrong profile: %+v", member)
	}
	if p.Fingerprint == before.Fingerprint || p.Descriptor.Digest == before.Descriptor.Digest || member.GraphDigest != orka.GraphDigest {
		t.Fatal("runtime did not bind separate packaged identity")
	}
	layout := filepath.Join(t.TempDir(), "copy")
	copied, err := suiteoras.PushLayout(t.Context(), p.Path, layout, "copy", p.Descriptor)
	if err != nil || copied.Descriptor.Digest != p.Descriptor.Digest {
		t.Fatal("substituted S", err)
	}
	artifact, err := suiteoras.ReadArtifactLayout(t.Context(), layout, p.Descriptor.Digest.String())
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := workspace.LoadArtifact(t.Context(), artifact)
	if err != nil || !reflect.DeepEqual(member, loaded) {
		t.Fatal("source-free transfer changed input", err)
	}
}

func TestAgentSessionsProfileRefusesCorruption(t *testing.T) {
	for _, change := range []string{"orka profile", "kagent profile", "unknown file", "runtime", "version", "engine", "contract", "renderer", "null images", "missing images", "nonempty images", "case omission", "case reorder", "case digest", "unknown field"} {
		t.Run(change, func(t *testing.T) {
			root, cache := createWorkspace(t)
			setMemberRuntime(t, root, sessionsDeclaration)
			writeFile(t, filepath.Join(root, "eval/second.yml"), "id: second\ninput: Question\nexpectContains: [answer]\n")
			p, err := workspace.Prepare(t.Context(), root, cache)
			if err != nil {
				t.Fatal(err)
			}
			out := extract(t, p)
			name := filepath.Join(out, "metadata/kmx/native-agentsessions.json")
			b := readFile(t, name)
			var record map[string]any
			if err := json.Unmarshal(b, &record); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "orka profile", "kagent profile":
				writeFile(t, filepath.Join(out, "metadata/kmx/native-"+strings.Fields(change)[0]+".json"), string(b))
			case "unknown file":
				writeFile(t, filepath.Join(out, "metadata/kmx/native-unknown.json"), string(b))
			case "runtime":
				record["runtime"] = nil
			case "version":
				record["runtime"].(map[string]any)["version"] = "main"
			case "engine":
				record["runtime"].(map[string]any)["engine"] = "custom"
			case "contract":
				record["executionProfile"] = "other/v1"
			case "renderer":
				record["rendererVersion"] = "kmx.native-orka/v1"
			case "null images":
				record["requiredImages"] = nil
			case "missing images":
				delete(record, "requiredImages")
			case "nonempty images":
				record["requiredImages"] = []string{"image"}
			case "case omission":
				record["cases"] = []any{}
			case "case reorder":
				c := record["cases"].([]any)
				c[0], c[1] = c[1], c[0]
			case "case digest":
				record["cases"].([]any)[0].(map[string]any)["digest"] = "sha256:" + strings.Repeat("0", 64)
			case "unknown field":
				record["unknown"] = true
			}
			b, err = json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, name, string(b))
			layout := filepath.Join(t.TempDir(), "layout")
			pushed, err := suiteoras.Push(t.Context(), out, layout, "changed")
			if err != nil {
				t.Fatal(err)
			}
			a, err := suiteoras.ReadArtifactLayout(t.Context(), layout, pushed.Descriptor.Digest.String())
			if err != nil {
				t.Fatal(err)
			}
			if got, err := workspace.LoadArtifact(t.Context(), a); err == nil || got != nil {
				t.Fatal("accepted corrupted profile", change)
			}
		})
	}
}

func TestAgentSessionsSourceRequiresExactChatPin(t *testing.T) {
	for _, declaration := range []string{"{id: agentsessions}", "{id: agentsessions, version: main, engine: chat}", "{id: agentsessions, version: " + sessionsVersion + ", engine: custom}", "{id: agentsessions, version: " + sessionsVersion + ", engine: chat, endpoint: localhost}"} {
		t.Run(declaration, func(t *testing.T) {
			root, _ := createWorkspace(t)
			setMemberRuntime(t, root, "runtime: "+declaration+"\n")
			cache := filepath.Join(t.TempDir(), "absent")
			if _, err := workspace.Prepare(t.Context(), root, cache); err == nil {
				t.Fatal("accepted unsupported runtime")
			}
			if _, err := os.Stat(cache); !os.IsNotExist(err) {
				t.Fatal("invalid runtime created cache", err)
			}
		})
	}
}
