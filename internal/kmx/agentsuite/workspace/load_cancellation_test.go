package workspace

import (
	"context"
	"errors"
	"strings"
	"testing"

	suitecore "github.com/agent-primitives/suite/core"
	"github.com/kaimahi-agents/kaimahi/pkg/suiteview"
)

func TestLoadNativeMemberPreservesCancellationFromPublicProjection(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	suite := suitecore.Suite{Agents: []suitecore.ManifestRef{{ID: "writer"}}}
	artifact := suiteview.ArtifactIdentity{Digest: "sha256:" + strings.Repeat("a", 64)}
	_, err := loadNativeMember(ctx, map[string][]byte{}, suite, nativeRecord{}, RuntimeProfile{}, artifact, "", 0)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation identity lost: %v", err)
	}
}
