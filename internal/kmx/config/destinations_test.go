package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
)

func TestDestinationsStrictClosedDocument(t *testing.T) {
	secret := secretshapes.All()[0].Example
	encoded := strings.ReplaceAll(secret, string(secret[0]), fmtYAMLEscape(secret[0]))
	valid := "version: 1\ntargets:\n  dev: {context: kind-demo, inference: 'provider:inference'}\n"
	for name, raw := range map[string]string{
		"unknown":                   valid + "password: PRIVATE_CANARY\n",
		"duplicate":                 valid + "version: 1\n",
		"alias":                     "version: 1\ntargets:\n  dev: &target {context: demo}\n  prod: *target\n",
		"merge":                     "version: 1\ntargets: {dev: {<<: {context: demo}}}\n",
		"null":                      "version: 1\ntargets: null\n",
		"bool context":              "version: 1\ntargets: {dev: {context: true}}\n",
		"int context":               "version: 1\ntargets: {dev: {context: 123}}\n",
		"string version":            "version: '1'\n",
		"multi":                     valid + "---\nversion: 1\n",
		"empty":                     "",
		"unknown runtime":           "version: 1\ntargets: {dev: {context: demo, runtime: magic}}\n",
		"xor":                       "version: 1\ntargets: {dev: {context: demo, inference: 'provider:inference', bindings: file}}\n",
		"missing context":           "version: 1\ntargets: {dev: {namespace: agents}}\n",
		"missing eval target":       valid + "evalTarget: missing\n",
		"missing evaluation target": "version: 1\ntargets: {dev: {context: demo, evaluationTarget: missing}}\n",
		"registry credentials":      valid + "publishRepository: user:password@registry.example/agents\n",
		"registry scheme":           valid + "registries: {'http://registry.example': {plainHTTP: true}}\n",
		"raw secret":                valid + "# " + secret + "\n",
		"decoded secret":            "version: 1\ntargets: {dev: {context: \"" + encoded + "\"}}\n",
		"oversize":                  valid + strings.Repeat("# padding\n", 10000),
		"text bound":                "version: 1\ntargets: {dev: {context: '" + strings.Repeat("a", 1025) + "'}}\n",
		"depth":                     "version: 1\ntargets: {dev: {context: [[[[[[[[[demo]]]]]]]]]}}\n",
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "kmx.yaml"), []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := LoadDestinations(root)
			if !errors.Is(err, ErrDestinationsInvalid) || strings.Contains(err.Error(), "PRIVATE_CANARY") || strings.Contains(err.Error(), secret) {
				t.Fatalf("unsafe/accepted config: %v", err)
			}
		})
	}
	root := t.TempDir()
	if doc, err := LoadDestinations(root); err != nil || doc != nil {
		t.Fatalf("optional missing: %v %v", doc, err)
	}
}

func fmtYAMLEscape(b byte) string {
	const hex = "0123456789abcdef"
	return "\\x" + string([]byte{hex[b>>4], hex[b&15]})
}

func TestDestinationsRejectLinksFIFOAndRootLinksWithoutBlocking(t *testing.T) {
	for _, kind := range []string{"symlink", "fifo", "directory", "root link"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "kmx.yaml")
			switch kind {
			case "symlink":
				other := filepath.Join(t.TempDir(), "config")
				os.WriteFile(other, []byte("version: 1\n"), 0600)
				if err := os.Symlink(other, path); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				testFIFO(t, path)
			case "directory":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "root link":
				os.WriteFile(path, []byte("version: 1\n"), 0600)
				link := filepath.Join(t.TempDir(), "link")
				os.Symlink(root, link)
				root = link
			}
			done := make(chan error, 1)
			go func() { _, err := LoadDestinations(root); done <- err }()
			select {
			case err := <-done:
				if !errors.Is(err, ErrDestinationsInvalid) {
					t.Fatalf("unsafe config accepted: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("blocked opening config")
			}
		})
	}
}

func TestDestinationsResolveCopiedSelectors(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "kmx.yaml")
	raw := `version: 1
evalTarget: dev
publishRepository: '127.0.0.1:5001/agents'
targets:
  dev: {context: kind-demo, namespace: agents, runtime: kagent, bindings: bindings/dev.yaml}
registries:
  '127.0.0.1:5001': {plainHTTP: true}
`
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	doc, err := LoadDestinations(root)
	if err != nil {
		t.Fatal(err)
	}
	target, err := doc.Target("dev")
	if err != nil {
		t.Fatal(err)
	}
	if target.Context != "kind-demo" || target.Namespace != "agents" || target.Runtime != "kagent" || target.Bindings != filepath.Join(root, "bindings/dev.yaml") {
		t.Fatalf("wrong selectors: %+v", target)
	}
	if !doc.RegistryHTTP("127.0.0.1:5001/other:v1") || doc.RegistryHTTP("127.0.0.1:5002/agents:v1") {
		t.Fatal("HTTP policy must be exact host")
	}
	if err := os.WriteFile(path, []byte("invalid: replacement\n"), 0600); err != nil {
		t.Fatal(err)
	}
	again, err := doc.Target("dev")
	if err != nil || again != target {
		t.Fatalf("resolved config reread: %+v %v", again, err)
	}
	if _, err := doc.Target("missing"); err == nil {
		t.Fatal("unknown target fell back")
	}
}
