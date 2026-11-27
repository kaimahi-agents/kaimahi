//go:build !windows

package workspacetest

import (
	"path/filepath"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
)

func TestFreezeOrkaMatchesPreparedIdentity(t *testing.T) {
	root := workspaceFixture(t)
	base := filepath.Dir(root)
	prepared, err := workspace.Prepare(t.Context(), root, filepath.Join(base, "cache"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := FreezeOrka(t.Context(), Summary{Workspace: root, Runtime: "orka", Context: "remote", Namespace: "orka-system", Inference: "provider:model"}, filepath.Join(base, "validation"))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	summary := p.Summary()
	if summary.SuiteDigest != prepared.Descriptor.Digest.String() || summary.SourceFingerprint != prepared.Fingerprint {
		t.Fatalf("frozen identity = %+v, prepared = %s/%s", summary, prepared.Descriptor.Digest, prepared.Fingerprint)
	}
}
