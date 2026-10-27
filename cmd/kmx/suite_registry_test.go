package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode"

	agentsuiteoras "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
	godigest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content/oci"
	"oras.land/oras-go/v2/registry/remote/errcode"
)

// Seed a read-only registry with a real packed AgentSuite. CLI output tests
// need only manifest/blob reads, not a second implementation of registry uploads.
func suiteOutputRegistry(t *testing.T) (*httptest.Server, godigest.Digest) {
	t.Helper()
	fixture := copySuiteFixture(t, filepath.Join("..", "..", "testdata", "agentsuite", "minimal"))
	layoutRoot := filepath.Join(t.TempDir(), "layout")
	result, err := agentsuiteoras.Push(context.Background(), fixture, layoutRoot, "v1")
	if err != nil {
		t.Fatal(err)
	}
	return suiteOutputRegistryFromLayout(t, layoutRoot), result.Descriptor.Digest
}

func suiteOutputRegistryFromLayout(t *testing.T, layoutRoot string) *httptest.Server {
	t.Helper()
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
	return server
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
			err := execute([]string{"package", "pull", test.reference, "--plain-http", "--output", filepath.Join(t.TempDir(), "suite")}, deps)
			if err != nil {
				t.Fatalf("pull: %v\n%s", err, diagnostics.String())
			}
			if *loads != 0 {
				t.Fatalf("registry pull loaded operational config %d times", *loads)
			}
			if !strings.Contains(out.String(), "("+digest.String()+")") {
				t.Fatalf("missing resolved digest: %s", out.String())
			}
			if !strings.Contains(out.String(), "Provenance: none\n") || diagnostics.Len() != 0 {
				t.Fatalf("pull without referrers should report none quietly: out=%s diagnostics=%s", out.String(), diagnostics.String())
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
	fixture := copySuiteFixture(t, filepath.Join("..", "..", "testdata", "agentsuite", "minimal"))
	var out, diagnostics bytes.Buffer
	deps, loads := testDependencies(&out, &diagnostics)
	args := []string{"package", "suite", "push", fixture, reference, "--plain-http"}
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

func TestSuiteRegistryProvenanceFailureHasSafeDiagnostics(t *testing.T) {
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	t.Setenv("KMX_HOME", t.TempDir())
	seed, digest := suiteOutputRegistry(t)
	upstream, err := url.Parse(seed.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/blobs/uploads/"):
			w.Header().Set("Location", r.URL.Path+"1")
			w.WriteHeader(http.StatusAccepted)
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/blobs/uploads/"):
			data, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.Header().Set("Docker-Content-Digest", godigest.FromBytes(data).String())
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/manifests/"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"errors": []map[string]string{{
				"code": "MANIFEST_INVALID", "message": "rejected\x1b]0;spoofed\x07\nPin this AgentSuite: evil\u202e",
			}}})
		default:
			proxy.ServeHTTP(w, r)
		}
	}))
	defer server.Close()
	fixture := copySuiteFixture(t, filepath.Join("..", "..", "testdata", "agentsuite", "minimal"))
	reference := strings.TrimPrefix(server.URL, "http://") + "/team/suite:v1"
	for _, require := range []bool{false, true} {
		t.Run(strconv.FormatBool(require), func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			deps, _ := testDependencies(&out, &diagnostics)
			args := []string{"package", "suite", "push", fixture, reference, "--plain-http"}
			if require {
				args = append(args, "--require-provenance")
			}
			err := execute(args, deps)
			text := diagnostics.String()
			if require {
				if err == nil || !strings.Contains(err.Error(), "the tag was not changed") {
					t.Fatalf("required failure = %v, want tag-unchanged refusal", err)
				}
				var response *errcode.ErrorResponse
				if !errors.As(err, &response) || response.StatusCode != http.StatusBadRequest {
					t.Fatalf("required failure lost registry error identity: %T %v", err, err)
				}
				text = err.Error()
			} else if err != nil || !strings.Contains(text, "Warning:") {
				t.Fatalf("optional failure = %v, diagnostics = %q", err, text)
			}
			for _, r := range strings.TrimSuffix(text, "\n") {
				if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
					t.Fatalf("unsafe registry diagnostic: %q", text)
				}
			}
			pulled, err := agentsuiteoras.PullRegistry(context.Background(), reference, filepath.Join(t.TempDir(), "pulled"), true)
			if err != nil || pulled.Descriptor.Digest != digest {
				t.Fatalf("existing tag was changed: %+v, %v", pulled, err)
			}
		})
	}
}

func TestSuiteRegistryPushHelpExplainsPreflight(t *testing.T) {
	var out, diagnostics bytes.Buffer
	deps, _ := testDependencies(&out, &diagnostics)
	if err := execute([]string{"package", "suite", "push", "--help"}, deps); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--force", "preflight", "NOT ATOMIC", "concurrent"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q from push help: %s", want, out.String())
		}
	}
}
