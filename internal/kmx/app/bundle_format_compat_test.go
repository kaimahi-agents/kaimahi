package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// Compatibility fixtures pin what the current reader parses and renders.
// Literal digests identify exact fixture and rendered bytes, not values
// calculated by the renderer under test.
func TestBundleFormatAcrossWriterReleases(t *testing.T) {
	for _, tc := range []struct {
		writer, portable, rendered string
	}{
		{"v0.3.0", "5c897b9421c61cb72da17bb90eb299eddf30a5536479a21fff323e632c49060d", "4beab3f5a0ae376146483f187f3d4f3793cc1c002e790c29073693f45810576b"},
		{"main", "229e0e17a20e0744f490beaedd0207ab833e292ab51d30a698815d63db2b8f42", "4b558a76e357caac30007d861a34656635a69af7967bf0e6cc734ad81fedaaae"},
	} {
		t.Run(tc.writer, func(t *testing.T) {
			bundle := filepath.Join("testdata", "bundle-format", tc.writer)
			source, err := os.ReadFile(filepath.Join(bundle, "agent.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := agentruntime.ParsePortableAgent(source)
			if err != nil {
				t.Fatalf("%s bundle no longer parses: %v", tc.writer, err)
			}
			if !bytes.Equal(parsed.Source(), source) {
				t.Fatal("parser did not retain exact fixture bytes")
			}
			if got := agentruntime.PortableBundleDigest(source); got != tc.portable {
				t.Fatalf("portable digest changed: %s, want %s", got, tc.portable)
			}
			bindingsSource, err := os.ReadFile(filepath.Join(bundle, "bindings.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			bindings, err := agentruntime.ParseOrkaBindings(bindingsSource)
			if err != nil {
				t.Fatalf("%s bindings no longer parse: %v", tc.writer, err)
			}
			rendered, err := RenderOrkaBundleFile(bundle, bindings)
			if err != nil {
				t.Fatalf("%s bundle no longer renders: %v", tc.writer, err)
			}
			if rendered.PortableDigest() != tc.portable || rendered.RenderedDigest() != tc.rendered {
				t.Fatalf("rendered bundle digests = %s / %s; want %s / %s", rendered.PortableDigest(), rendered.RenderedDigest(), tc.portable, tc.rendered)
			}
			docs := rendered.Documents()
			if len(docs) != 3 || len(rendered.DeployDocuments()) != 2 {
				t.Fatalf("rendered %d documents, %d deployable; want 3 and 2", len(docs), len(rendered.DeployDocuments()))
			}
			golden, err := os.ReadFile(filepath.Join("testdata", "bundle-format", tc.writer+".rendered.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			actual := append([]byte("---\n"), bytes.Join(docs, []byte("---\n"))...)
			if !bytes.Equal(actual, golden) {
				t.Fatalf("%s rendered documents differ from pinned bytes", tc.writer)
			}
			caseSource, err := os.ReadFile(filepath.Join(bundle, "eval", "example.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := agentruntime.ParseEvaluationCase(caseSource); err != nil {
				t.Fatalf("%s evaluation case no longer parses: %v", tc.writer, err)
			}
		})
	}
}

func TestBundleFormatRefusesNewerVersionsAndUnknownFields(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("testdata", "bundle-format", "main", "agent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, before, after, want string
	}{
		{"future document version", "kmx.kaimahi.dev/v1alpha1", "kmx.kaimahi.dev/v99", "apiVersion must be \"kmx.kaimahi.dev/v1alpha1\" (found \"kmx.kaimahi.dev/v99\")"},
		{"future extension version", "core.orka.ai/v1alpha1", "core.orka.ai/v99", "apiVersion must be \"core.orka.ai/v1alpha1\" for the \"orka\" runtime (found \"core.orka.ai/v99\")"},
		{"unknown common field", "    description: Current fixture\n", "    description: Current fixture\n    futureFeature: true\n", "field futureFeature not found"},
		{"unknown extension field", "        apiVersion: core.orka.ai/v1alpha1\n", "        apiVersion: core.orka.ai/v1alpha1\n        coordination: true\n", "field coordination not found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !bytes.Contains(source, []byte(tc.before)) {
				t.Fatalf("fixture missing %q", tc.before)
			}
			_, err := agentruntime.ParsePortableAgent(bytes.Replace(source, []byte(tc.before), []byte(tc.after), 1))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected refusal containing %q, got %v", tc.want, err)
			}
		})
	}
}
