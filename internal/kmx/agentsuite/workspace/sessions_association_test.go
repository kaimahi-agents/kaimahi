package workspace_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentsuite "github.com/agent-primitives/suite"
	suiteoras "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
)

const declaredHarness = "ghcr.io/orka-agents/agentkit/serve-pydantic-ai@sha256:8c4c17dc3d778c02097ad7e1ea88e9084b3f1aad6c36458829ff9cec9b4d043c"

// Deliberately synthetic/unqualified implementations: only declarations are
// checked, not support or image bytes. The image is never fetched or inspected.
func associationFixture(t *testing.T, change func(string)) (*workspace.NativeSuite, *workspace.NativeSuite) {
	t.Helper()
	root, cache := profiledWorkspace(t)
	name := filepath.Join(root, "runtime-profiles/sessions.json")
	writeFile(t, name, strings.ReplaceAll(string(readFile(t, name)), `"id":"sessions"`, `"id":"agentsessions"`))
	// Keep the suite-local profile ID distinct from its runtime family.
	var rp agentsuite.RuntimeProfile
	if err := agentsuite.Unmarshal(readFile(t, name), &rp); err != nil {
		t.Fatal(err)
	}
	rp.ID = "sessions"
	raw, err := agentsuite.Marshal(rp)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, name, string(raw))
	if change != nil {
		change(root)
	}
	p, err := workspace.Prepare(t.Context(), root, cache)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := suiteoras.ReadArtifactLayout(t.Context(), p.Path, p.Descriptor.Digest.String())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(cache); err != nil {
		t.Fatal(err)
	}
	s, err := workspace.LoadArtifactSelected(t.Context(), artifact, workspace.Selection{RuntimeProfile: "sessions"})
	if err != nil {
		t.Fatal(err)
	}
	orka, err := workspace.LoadArtifactSelected(t.Context(), artifact, workspace.Selection{RuntimeProfile: "orka"})
	if err != nil {
		t.Fatal(err)
	}
	return s, orka
}

func TestSessionsAssociationDeclaredOnlyAndFrozen(t *testing.T) {
	s, orka := associationFixture(t, nil)
	raw, err := workspace.NewSessionsAssociation(t.Context(), s, declaredHarness)
	if err != nil {
		t.Fatal(err)
	}
	var record workspace.SessionsAssociationV1
	if err := agentsuite.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	if record.SuiteDigest != s.Digest || record.MemberID != "greeter" || record.RuntimeProfile.ID != "sessions" || record.RuntimeProfile.Digest != s.ProfileDigest || record.CompositionDigest != s.CompositionDigest || record.BuildProfileDigest != s.BuildProfileDigest || record.Platform != (agentsuite.EvidencePlatform{OS: "linux", Architecture: "amd64"}) || record.HarnessImage != declaredHarness || record.ConfigDigest != "sha256:8da08f54e4d3d40f3bf9cf959998ee82e1f32eb3edc0ba0cc2f72037b57fb235" || record.ImageVerification != "declared-unverified" || record.ExecutionKind != "harnesswire-chat-v1" || record.Version != 1 || record.Kind != "kmx.sessions-association" {
		t.Fatal("declaration identity/config changed")
	}
	if err := workspace.CheckSessionsAssociation(t.Context(), raw, s); err != nil {
		t.Fatal(err)
	}
	if err := workspace.CheckSessionsAssociation(t.Context(), raw, orka); err == nil {
		t.Fatal("foreign profile accepted")
	}
	// No mutable selected-view field may manufacture a different association.
	s.Digest, s.MemberID, s.CompositionDigest = "foreign", "other", orka.CompositionDigest
	s.Profile, s.Platform, s.Policy = orka.Profile, orka.Platform, &workspace.NativePolicy{}
	s.Instructions[0] = 'X'
	again, err := workspace.NewSessionsAssociation(t.Context(), s, declaredHarness)
	if err != nil || !bytes.Equal(raw, again) {
		t.Fatal("mutable view replaced immutable selection", err)
	}
	if !errors.Is(s.RequireUnprofiled(), workspace.ErrProfileExecutionUnsupported) {
		t.Fatal("association authorized execution")
	}
	for _, value := range []*workspace.NativeSuite{nil, {}, {Profile: orka.Profile}} {
		if _, err := workspace.NewSessionsAssociation(t.Context(), value, declaredHarness); err == nil {
			t.Fatal("unanchored selection accepted")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := workspace.NewSessionsAssociation(ctx, s, declaredHarness); !errors.Is(err, context.Canceled) {
		t.Fatal("lost cancellation", err)
	}
}

func TestSessionsAssociationClosedCanonicalInput(t *testing.T) {
	s, _ := associationFixture(t, nil)
	raw, err := workspace.NewSessionsAssociation(t.Context(), s, declaredHarness)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"version", "kind", "suiteDigest", "memberID", "runtimeProfile", "compositionDigest", "buildProfileDigest", "platform", "harnessImage", "executionKind", "configDigest", "imageVerification", "runtimeProfile.id", "runtimeProfile.digest", "platform.os", "platform.architecture", "platform.variant"} {
		for _, mode := range []string{"missing", "null", "mismatch"} {
			t.Run(field+"/"+mode, func(t *testing.T) {
				var record map[string]any
				if err := json.Unmarshal(raw, &record); err != nil {
					t.Fatal(err)
				}
				parts := strings.Split(field, ".")
				target := record
				if len(parts) == 2 {
					target = record[parts[0]].(map[string]any)
				}
				key := parts[len(parts)-1]
				switch mode {
				case "missing":
					delete(target, key)
				case "null":
					target[key] = nil
				default:
					target[key] = "verified"
				}
				bad, err := agentsuite.Marshal(record)
				if err != nil {
					t.Fatal(err)
				}
				if err := workspace.CheckSessionsAssociation(t.Context(), bad, s); err == nil {
					t.Fatal("malformed or mismatched declaration admitted")
				}
			})
		}
	}
	for name, bad := range map[string][]byte{
		"unknown":        []byte(strings.Replace(string(raw), `{`, `{"untrusted-canary":true,`, 1)),
		"duplicate":      []byte(strings.Replace(string(raw), `{`, `{"kind":"kmx.sessions-association",`, 1)),
		"nested unknown": []byte(strings.Replace(string(raw), `"variant":""`, `"unknown":true,"variant":""`, 1)),
		"space":          append([]byte(" "), raw...), "trailing": append(bytes.Clone(raw), []byte(`{}`)...),
		"oversized": bytes.Repeat([]byte("x"), 16385), "invalid UTF8": {0xff},
	} {
		t.Run(name, func(t *testing.T) {
			if err := workspace.CheckSessionsAssociation(t.Context(), bad, s); err == nil || strings.Contains(err.Error(), "untrusted-canary") {
				t.Fatal("unsafe association decode")
			}
		})
	}
	for _, image := range []string{declaredHarness + ":tag", "image:latest", "image@sha256:" + strings.Repeat("0", 64), strings.Replace(declaredHarness, "8c4c", "0c4c", 1), strings.Replace(declaredHarness, "ghcr.io", "mirror.example", 1)} {
		if _, err := workspace.NewSessionsAssociation(t.Context(), s, image); err == nil {
			t.Fatal("image declaration mismatch accepted")
		}
	}
}

func TestSessionsAssociationRefusesPolicy(t *testing.T) {
	s, _ := associationFixture(t, func(root string) {
		name := filepath.Join(root, "agents/greeter/member.yaml")
		writeFile(t, name, string(readFile(t, name))+"policy: policy.json\n")
		writeFile(t, filepath.Join(root, "agents/greeter/policy.json"), networkPolicy(""))
	})
	if _, err := workspace.NewSessionsAssociation(t.Context(), s, declaredHarness); err == nil {
		t.Fatal("declared policy accepted")
	}
}
