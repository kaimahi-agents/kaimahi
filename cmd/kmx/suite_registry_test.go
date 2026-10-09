package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	agentsuiteoras "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
	godigest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content/oci"
)

// Seed a read-only registry with a real packed AgentSuite. CLI output tests
// need only manifest/blob reads, not a second implementation of registry uploads.
func suiteOutputRegistry(t *testing.T) (*httptest.Server, godigest.Digest) {
	t.Helper()
	fixture := copySuiteFixture(t, filepath.Join("..", "..", "internal", "kmx", "agentsuite", "testdata", "minimal"))
	layoutRoot := filepath.Join(t.TempDir(), "layout")
	result, err := agentsuiteoras.Push(context.Background(), fixture, layoutRoot, "v1")
	if err != nil {
		t.Fatal(err)
	}
	store, err := oci.New(layoutRoot)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead && r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var descriptor ocispec.Descriptor
		var err error
		if strings.Contains(r.URL.Path, "/manifests/") {
			_, reference, _ := strings.Cut(r.URL.Path, "/manifests/")
			descriptor, err = store.Resolve(r.Context(), reference)
		} else if strings.Contains(r.URL.Path, "/blobs/") {
			_, digest, _ := strings.Cut(r.URL.Path, "/blobs/")
			descriptor = ocispec.Descriptor{Digest: godigest.Digest(digest)}
		} else {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		reader, err := store.Fetch(r.Context(), descriptor)
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		defer reader.Close()
		data, err := io.ReadAll(reader)
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", descriptor.MediaType)
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.Header().Set("Docker-Content-Digest", descriptor.Digest.String())
		if r.Method == http.MethodGet {
			_, _ = w.Write(data)
		}
	}))
	t.Cleanup(server.Close)
	return server, result.Descriptor.Digest
}

func TestSuiteRegistryPullPrintsPinForTagsOnly(t *testing.T) {
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	t.Setenv("KMX_HOME", t.TempDir())
	server, digest := suiteOutputRegistry(t)
	repository := strings.TrimPrefix(server.URL, "http://") + "/team/suite"
	for _, test := range []struct {
		name, reference string
		wantPin         bool
	}{
		{name: "tag", reference: repository + ":v1", wantPin: true},
		{name: "digest", reference: repository + "@" + digest.String()},
	} {
		t.Run(test.name, func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			deps, loads := testDependencies(&out, &diagnostics)
			err := execute([]string{"suite", "pull", test.reference, "--plain-http", "--output", filepath.Join(t.TempDir(), "suite")}, deps)
			if err != nil {
				t.Fatalf("pull: %v\n%s", err, diagnostics.String())
			}
			if *loads != 0 {
				t.Fatalf("registry pull loaded operational config %d times", *loads)
			}
			if !strings.Contains(out.String(), "("+digest.String()+")") {
				t.Fatalf("missing resolved digest: %s", out.String())
			}
			pinLine := "Pin this AgentSuite: " + repository + "@" + digest.String() + "\n"
			if test.wantPin && !strings.Contains(out.String(), pinLine) {
				t.Errorf("missing repository digest pin %q: %s", pinLine, out.String())
			}
			if !test.wantPin && strings.Contains(out.String(), "Pin this AgentSuite:") {
				t.Errorf("digest pull printed unnecessary pin hint: %s", out.String())
			}
		})
	}
}

func TestSuiteRegistryPushPrintsDigestAndPassesForce(t *testing.T) {
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	t.Setenv("KMX_HOME", t.TempDir())
	server, digest := suiteOutputRegistry(t)
	reference := strings.TrimPrefix(server.URL, "http://") + "/team/suite:v1"
	fixture := copySuiteFixture(t, filepath.Join("..", "..", "internal", "kmx", "agentsuite", "testdata", "minimal"))
	var out, diagnostics bytes.Buffer
	deps, loads := testDependencies(&out, &diagnostics)
	args := []string{"suite", "push", fixture, reference, "--plain-http"}
	if err := execute(args, deps); err != nil {
		t.Fatal(err)
	}
	if *loads != 0 {
		t.Fatalf("registry push loaded operational config %d times", *loads)
	}
	if !strings.Contains(out.String(), "Pushed AgentSuite minimal to "+reference+" ("+digest.String()+")") {
		t.Fatalf("missing pushed digest: %s", out.String())
	}
	if err := os.WriteFile(filepath.Join(fixture, "extra.txt"), []byte("changed content"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := execute(args, deps); err == nil || !strings.Contains(err.Error(), "without --force") {
		t.Fatalf("different digest push error = %v, want tag refusal", err)
	}
	// This read-only server rejects writes. A 405 on forced push proves the
	// CLI/app force option passed the tag preflight and reached upload.
	if err := execute(append(args, "--force"), deps); err == nil || !strings.Contains(err.Error(), "405") {
		t.Fatalf("forced push error = %v, want read-only registry write refusal", err)
	}
}

func TestSuiteRegistryPushHelpExplainsPreflight(t *testing.T) {
	var out, diagnostics bytes.Buffer
	deps, _ := testDependencies(&out, &diagnostics)
	if err := execute([]string{"suite", "push", "--help"}, deps); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--force", "preflight", "NOT ATOMIC", "concurrent"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q from push help: %s", want, out.String())
		}
	}
}
