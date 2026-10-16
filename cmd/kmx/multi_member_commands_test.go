package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
)

func TestMultiMemberCommandsRefuseBeforeOperationalSelection(t *testing.T) {
	for _, args := range [][]string{
		{"eval", "--to-context", "kind-dev", "--inference", "provider:inference"},
		{"eval", "--to", "dev"},
		{"eval", "--development-host", "missing-host.json"},
		{"push", "registry.example/team:v1"},
		{"push", "--evaluation", "missing-proof.json", "registry.example/team:v1"},
		{"push", "--unevaluated", "registry.example/team:v1"},
		{"push"},
		{"push", "--unevaluated", "--to-layout", "output", "team:v1"},
		{"lift", "--to", "prod"},
		{"lift", "--to", "prod", "--plan"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			root := t.TempDir()
			// Unsupported operations need only the bounded descriptor; they must
			// not read member source, credentials, destinations or receipt paths.
			for name, data := range map[string]string{
				"workspace.yaml": "schemaVersion: 2\nname: pair\nmembers: [agents/first/member.yaml, agents/second/member.yaml]\nbuildProfile: build-profiles/default.json\n",
				"kmx.yaml":       "broken: [\n",
			} {
				if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			t.Chdir(root)
			state := filepath.Join(t.TempDir(), "state")
			t.Setenv("KMX_HOME", state)
			var out, diagnostics bytes.Buffer
			deps, loads := testDependencies(&out, &diagnostics)
			err := execute(args, deps)
			if !errors.Is(err, workspace.ErrMultiMemberUnsupported) || !strings.Contains(err.Error(), "multi-member") || !strings.Contains(err.Error(), "offline") {
				t.Fatalf("expected multi-member offline-only refusal before downstream errors: %v", err)
			}
			if *loads != 0 || out.Len() != 0 || diagnostics.Len() != 0 {
				t.Fatalf("refusal loaded configuration or emitted output: loads=%d, %q, %q", *loads, out.String(), diagnostics.String())
			}
			for _, absent := range []string{state, filepath.Join(root, "output")} {
				if _, err := os.Lstat(absent); !os.IsNotExist(err) {
					t.Fatalf("refusal wrote %s: %v", absent, err)
				}
			}
		})
	}
}
