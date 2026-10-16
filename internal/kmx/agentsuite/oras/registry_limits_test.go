package oras

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	godigest "github.com/opencontainers/go-digest"
	specs "github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/credentials"
)

// Replace only registry metadata responses, leaving suite transfer and tag
// writes to the real registry fixture and ORAS client.
func oversizedRegistryIndexFixture(t *testing.T, api bool) (string, string, *testRegistry, *atomic.Bool, *atomic.Int32) {
	t.Helper()
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	fixtureServer, registry := newTestRegistryServer("", "")
	fixtureServer.Close()
	registry.referrersAPI = api
	index, err := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"mediaType":     ocispec.MediaTypeImageIndex,
		"manifests":     []any{},
		"annotations":   map[string]string{"padding": strings.Repeat("x", 1<<20)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(index) <= 1<<20 || len(index) >= 4<<20 || !json.Valid(index) {
		t.Fatal("fixture must be valid JSON between 1 MiB and the ORAS 4 MiB default")
	}
	var oversized atomic.Bool
	var indexReads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiPage := api && strings.Contains(r.URL.Path, "/referrers/")
		fallbackIndex := !api && strings.Contains(r.URL.Path, "/manifests/sha256-")
		if oversized.Load() && (apiPage || fallbackIndex) && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
			indexReads.Add(1)
			w.Header().Set("Content-Type", ocispec.MediaTypeImageIndex)
			w.Header().Set("Content-Length", strconv.Itoa(len(index)))
			w.Header().Set("Docker-Content-Digest", contentDigest(index))
			if r.Method == http.MethodGet {
				_, _ = w.Write(index)
			}
			return
		}
		registry.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	source := filepath.Join(t.TempDir(), "source")
	copyDirectory(t, filepath.Join("..", "..", "..", "..", "testdata", "agentsuite", "minimal"), source)
	return source, strings.TrimPrefix(server.URL, "http://") + "/team/suite:v1", registry, &oversized, &indexReads
}

func TestRegistryPullOversizedReferrersRemainInformational(t *testing.T) {
	for _, api := range []bool{true, false} {
		t.Run(fmt.Sprintf("api=%t", api), func(t *testing.T) {
			source, reference, _, oversized, reads := oversizedRegistryIndexFixture(t, api)
			pushed, err := PushRegistry(context.Background(), source, reference, true, false, PushOptions{})
			if err != nil {
				t.Fatal(err)
			}
			oversized.Store(true)
			output := filepath.Join(t.TempDir(), "output")
			pulled, err := PullRegistry(context.Background(), reference, output, true)
			if err != nil {
				t.Fatalf("informational provenance failure blocked suite pull: %v", err)
			}
			if pulled.Descriptor.Digest != pushed.Descriptor.Digest || pulled.Report == nil {
				t.Fatal("pull did not return the validated suite")
			}
			if _, err := os.Stat(filepath.Join(output, "agentsuite.json")); err != nil {
				t.Fatalf("suite was not extracted: %v", err)
			}
			if reads.Load() == 0 {
				t.Fatal("pull never read the oversized index")
			}
			want := "MaxMetadataBytes 1048576"
			if api {
				want = "failed to decode response: unexpected EOF"
			}
			if pulled.ProvenanceChecked || len(pulled.Provenance) != 0 || len(pulled.Warnings) != 1 || !strings.Contains(pulled.Warnings[0], want) {
				t.Fatalf("oversized index: checked=%t provenance=%+v warnings=%q, want unchecked and %q", pulled.ProvenanceChecked, pulled.Provenance, pulled.Warnings, want)
			}
		})
	}
}

func TestRegistryPushOversizedFallbackIndex(t *testing.T) {
	for _, test := range []struct {
		name        string
		require     bool
		existingTag bool
		changeSuite bool
	}{
		{"optional replacement", false, true, true},
		{"required new tag", true, false, false},
		{"required replacement", true, true, true},
		{"required same-digest repair", true, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			source, reference, registry, oversized, reads := oversizedRegistryIndexFixture(t, false)
			var oldDigest string
			if test.existingTag {
				first, err := PushRegistry(context.Background(), source, reference, true, false, PushOptions{})
				if err != nil {
					t.Fatal(err)
				}
				oldDigest = first.Descriptor.Digest.String()
			}
			if test.changeSuite {
				if err := os.WriteFile(filepath.Join(source, "extra.txt"), []byte("changed suite content"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			oversized.Store(true)
			options := PushOptions{Provenance: &ProvenanceOptions{BuilderVersion: "v1", Require: test.require}}
			pushed, err := PushRegistry(context.Background(), source, reference, true, true, options)
			registry.mu.RLock()
			current := registry.manifests[registryKey("team/suite", "v1")].digest
			registry.mu.RUnlock()
			if reads.Load() == 0 {
				t.Fatal("attachment never read the oversized fallback index")
			}
			if test.require {
				if current != oldDigest {
					t.Errorf("required attachment failure moved tag from %q to %q", oldDigest, current)
				}
				if err == nil || !strings.Contains(err.Error(), "MaxMetadataBytes 1048576") || !strings.Contains(err.Error(), "tag was not changed") {
					t.Fatalf("required push error = %v, want metadata limit refusal with unchanged tag", err)
				}
				// Once the boundary failure clears, retry must repair even if the
				// first attempt stored the provenance manifest by digest.
				oversized.Store(false)
				repaired, err := PushRegistry(context.Background(), source, reference, true, true, options)
				if err != nil || repaired.Provenance == nil || len(repaired.Warnings) != 0 {
					t.Fatalf("required retry: provenance=%+v warnings=%q error=%v", repaired.Provenance, repaired.Warnings, err)
				}
				pulled, err := PullRegistry(context.Background(), reference, filepath.Join(t.TempDir(), "repaired"), true)
				if err != nil || !pulled.ProvenanceChecked || len(pulled.Provenance) != 1 || pulled.Provenance[0].Digest != repaired.Provenance.Digest {
					t.Fatalf("repair not discoverable: checked=%t provenance=%+v error=%v", pulled.ProvenanceChecked, pulled.Provenance, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("optional attachment failure blocked push: %v", err)
			}
			if current != pushed.Descriptor.Digest.String() || current == oldDigest {
				t.Errorf("optional push did not move tag: before=%s after=%s pushed=%s", oldDigest, current, pushed.Descriptor.Digest)
			}
			if pushed.Provenance != nil || len(pushed.Warnings) != 1 || !strings.Contains(pushed.Warnings[0], "not attached") || !strings.Contains(pushed.Warnings[0], "MaxMetadataBytes 1048576") {
				t.Fatalf("optional push: provenance=%+v warnings=%q, want metadata limit warning", pushed.Provenance, pushed.Warnings)
			}
		})
	}
}

func TestRegistryPushReferrerIndexGrowthPreservesExistingIndex(t *testing.T) {
	for _, test := range []struct {
		name        string
		require     bool
		changeSuite bool
	}{
		{"optional replacement", false, true},
		{"required replacement", true, true},
		{"required same-digest repair", true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("DOCKER_CONFIG", t.TempDir())
			fixtureServer, registry := newTestRegistryServer("", "")
			fixtureServer.Close()
			var indexPuts, deletes atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPut && r.Header.Get("Content-Type") == ocispec.MediaTypeImageIndex {
					indexPuts.Add(1)
				}
				if r.Method == http.MethodDelete {
					deletes.Add(1)
				}
				registry.ServeHTTP(w, r)
			}))
			defer server.Close()
			source := filepath.Join(t.TempDir(), "source")
			copyDirectory(t, filepath.Join("..", "..", "..", "..", "testdata", "agentsuite", "minimal"), source)
			reference := strings.TrimPrefix(server.URL, "http://") + "/team/suite:v1"
			previous, err := PushRegistry(context.Background(), source, reference, true, false, PushOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if test.changeSuite {
				if err := os.WriteFile(filepath.Join(source, "extra.txt"), []byte("changed suite content"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			packedRoot, _, packed, err := packDirectory(context.Background(), source)
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(packedRoot)
			// Descriptor annotations survive ORAS index regeneration. Padding the
			// index itself would disappear and would not reproduce output growth.
			index := ocispec.Index{
				Versioned: specs.Versioned{SchemaVersion: 2},
				MediaType: ocispec.MediaTypeImageIndex,
				Manifests: []ocispec.Descriptor{
					{MediaType: ocispec.MediaTypeImageManifest, ArtifactType: "application/vnd.example.foreign+json", Digest: godigest.FromString("foreign one"), Size: 1, Annotations: map[string]string{"padding": ""}},
					{MediaType: ocispec.MediaTypeImageManifest, ArtifactType: "application/vnd.example.foreign+json", Digest: godigest.FromString("foreign two"), Size: 1},
				},
			}
			indexBytes, err := json.Marshal(index)
			if err != nil {
				t.Fatal(err)
			}
			index.Manifests[0].Annotations["padding"] = strings.Repeat("x", 1048476-len(indexBytes))
			indexBytes, err = json.Marshal(index)
			if err != nil || len(indexBytes) != 1048476 || !json.Valid(indexBytes) {
				t.Fatalf("near-limit index: size=%d error=%v", len(indexBytes), err)
			}
			indexTag := "sha256-" + packed.Descriptor.Digest.Encoded()
			indexDigest := contentDigest(indexBytes)
			storedIndex := testManifest{contentType: ocispec.MediaTypeImageIndex, content: indexBytes, digest: indexDigest}
			registry.mu.Lock()
			registry.manifests[registryKey("team/suite", indexTag)] = storedIndex
			registry.manifests[registryKey("team/suite", indexDigest)] = storedIndex
			registry.mu.Unlock()
			options := PushOptions{Provenance: &ProvenanceOptions{BuilderVersion: "v1", Require: test.require}}
			pushed, pushErr := PushRegistry(context.Background(), source, reference, true, true, options)
			registry.mu.RLock()
			currentTag := registry.manifests[registryKey("team/suite", "v1")].digest
			currentIndex := registry.manifests[registryKey("team/suite", indexTag)]
			originalIndex := registry.manifests[registryKey("team/suite", indexDigest)]
			registry.mu.RUnlock()
			if currentIndex.digest != indexDigest || !bytes.Equal(currentIndex.content, indexBytes) || !bytes.Equal(originalIndex.content, indexBytes) {
				t.Errorf("existing foreign referrers index changed: tagged size=%d digest=%s, want size=1048476 digest=%s", len(currentIndex.content), currentIndex.digest, indexDigest)
			}
			if indexPuts.Load() != 0 || deletes.Load() != 0 {
				t.Errorf("overflow reached registry: index PUTs=%d DELETEs=%d", indexPuts.Load(), deletes.Load())
			}
			if test.require {
				if currentTag != previous.Descriptor.Digest.String() {
					t.Errorf("required provenance failure moved suite tag: %s -> %s", previous.Descriptor.Digest, currentTag)
				}
				if pushErr == nil || !strings.Contains(pushErr.Error(), "registry index size") || !strings.Contains(pushErr.Error(), "exceeds 1048576 bytes") || !strings.Contains(pushErr.Error(), "tag was not changed") {
					t.Errorf("required push error = %v, want index output size refusal with unchanged tag", pushErr)
				}
			} else {
				if pushErr != nil || currentTag != packed.Descriptor.Digest.String() || currentTag == previous.Descriptor.Digest.String() {
					t.Errorf("optional push: tag=%s error=%v, want suite tag moved", currentTag, pushErr)
				}
				if pushed.Provenance != nil || len(pushed.Warnings) != 1 || !strings.Contains(pushed.Warnings[0], "not attached") || !strings.Contains(pushed.Warnings[0], "exceeds 1048576 bytes") {
					t.Errorf("optional push: provenance=%+v warnings=%q, want index output size warning", pushed.Provenance, pushed.Warnings)
				}
			}
			// Check the prospective suite by digest even when its required tag
			// update was refused: the old index must remain readable and checked.
			digestReference := strings.TrimSuffix(reference, ":v1") + "@" + packed.Descriptor.Digest.String()
			pulled, err := PullRegistry(context.Background(), digestReference, filepath.Join(t.TempDir(), "output"), true)
			if err != nil || !pulled.ProvenanceChecked || len(pulled.Warnings) != 0 || len(pulled.Provenance) != 0 {
				t.Errorf("pull after refused index growth: checked=%t provenance=%+v warnings=%q error=%v", pulled.ProvenanceChecked, pulled.Provenance, pulled.Warnings, err)
			}
		})
	}
}

func TestRegistryIndexWriteSizeBoundary(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "remote.example"} {
		for _, test := range []struct {
			name      string
			mediaType string
			size      int
			refused   bool
		}{
			{"limit", ocispec.MediaTypeImageIndex, 1048576, false},
			{"overflow", ocispec.MediaTypeImageIndex, 1048577, true},
			{"non-index", ocispec.MediaTypeImageManifest, 1048577, false},
		} {
			t.Run(host+"/"+test.name, func(t *testing.T) {
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					if _, err := io.Copy(io.Discard, r.Body); err != nil {
						t.Error(err)
					}
					w.WriteHeader(http.StatusCreated)
				}))
				defer server.Close()
				repository, _, err := newRegistryRepository(host+"/team:v1", true, credentials.NewMemoryStore())
				if err != nil {
					t.Fatal(err)
				}
				routeRegistryClient(t, repository.Client, server)
				bodyPath := filepath.Join(t.TempDir(), "index-body")
				if err := os.WriteFile(bodyPath, bytes.Repeat([]byte("x"), test.size), 0600); err != nil {
					t.Fatal(err)
				}
				body, err := os.Open(bodyPath)
				if err != nil {
					t.Fatal(err)
				}
				defer body.Close()
				req, err := http.NewRequest(http.MethodPut, "http://"+host+"/v2/team/manifests/sha256-index", body)
				if err != nil {
					t.Fatal(err)
				}
				req.ContentLength = int64(test.size)
				req.Header.Set("Content-Type", test.mediaType)
				resp, err := repository.Client.Do(req)
				if resp != nil {
					resp.Body.Close()
				}
				if test.refused {
					if err == nil || !strings.Contains(err.Error(), "registry index size 1048577 exceeds 1048576 bytes") || requests.Load() != 0 {
						t.Errorf("index overflow: requests=%d error=%v, want size refusal before sending", requests.Load(), err)
					}
					if _, err := body.Stat(); !errors.Is(err, os.ErrClosed) {
						t.Errorf("refused request body was not closed: %v", err)
					}
				} else if err != nil || requests.Load() != 1 || resp == nil || resp.StatusCode != http.StatusCreated {
					t.Errorf("allowed write: requests=%d response=%v error=%v", requests.Load(), resp, err)
				}
			})
		}
	}
}

func TestRegistryReferrersRedirectLimit(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "remote.example"} {
		for _, test := range []struct {
			name         string
			redirects    int
			wantRequests int32
			wantError    bool
		}{
			{"no redirect", 0, 1, false},
			{"ordinary redirects", 2, 3, false},
			{"last allowed redirect", 9, 10, false},
			{"one redirect too many", 10, 10, true},
			{"same-origin loop", -1, 10, true},
		} {
			t.Run(host+"/"+test.name, func(t *testing.T) {
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					hop, _ := strconv.Atoi(r.URL.Query().Get("hop"))
					if test.redirects < 0 || hop < test.redirects {
						location := r.URL.Path + "?hop=" + strconv.Itoa(hop+1)
						if test.redirects < 0 {
							location = r.URL.RequestURI()
						}
						w.Header().Set("Location", location)
						w.WriteHeader(http.StatusTemporaryRedirect)
						return
					}
					w.Header().Set("Content-Type", ocispec.MediaTypeImageIndex)
					_, _ = w.Write([]byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[]}`))
				}))
				defer server.Close()
				repository, _, err := newRegistryRepository(host+"/team:v1", true, credentials.NewMemoryStore())
				if err != nil {
					t.Fatal(err)
				}
				routeRegistryClient(t, repository.Client, server)
				// The timeout is only an escape hatch if redirect bounding regresses;
				// a deadline error must never satisfy the refusal assertion.
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				err = repository.Referrers(ctx, ocispec.Descriptor{Digest: godigest.FromString("suite")}, InTotoMediaType, func([]ocispec.Descriptor) error { return nil })
				if got := requests.Load(); got != test.wantRequests {
					t.Errorf("requests = %d, want %d", got, test.wantRequests)
				}
				if test.wantError {
					if err == nil || !strings.Contains(err.Error(), "stopped after 10 redirects") {
						t.Fatalf("referrers error = %v, want redirect-limit refusal", err)
					}
				} else if err != nil {
					t.Fatalf("ordinary referrer redirects failed: %v", err)
				}
			})
		}
	}
}

func TestRegistryRedirectPreservesUpstreamPolicy(t *testing.T) {
	// No package tests run in parallel: replace the upstream default only long
	// enough to verify the repository copy retains its caller's redirect policy.
	original := auth.DefaultClient
	upstream := *original
	upstreamHTTP := *original.Client
	upstream.Client = &upstreamHTTP
	auth.DefaultClient = &upstream
	t.Cleanup(func() { auth.DefaultClient = original })
	refused := errors.New("upstream redirect refused")
	for _, host := range []string{"127.0.0.1", "remote.example"} {
		for _, permissive := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/permissive=%t", host, permissive), func(t *testing.T) {
				var callbacks, requests atomic.Int32
				upstreamHTTP.CheckRedirect = func(*http.Request, []*http.Request) error {
					callbacks.Add(1)
					if !permissive {
						return refused
					}
					return nil
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					w.Header().Set("Location", r.URL.RequestURI())
					w.WriteHeader(http.StatusTemporaryRedirect)
				}))
				defer server.Close()
				repository, _, err := newRegistryRepository(host+"/team:v1", true, credentials.NewMemoryStore())
				if err != nil {
					t.Fatal(err)
				}
				routeRegistryClient(t, repository.Client, server)
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				err = repository.Referrers(ctx, ocispec.Descriptor{Digest: godigest.FromString("suite")}, "", func([]ocispec.Descriptor) error { return nil })
				if permissive {
					if requests.Load() != 10 || err == nil || !strings.Contains(err.Error(), "stopped after 10 redirects") {
						t.Fatalf("permissive callback bypassed bound: requests=%d error=%v", requests.Load(), err)
					}
				} else if requests.Load() != 1 || !errors.Is(err, refused) {
					t.Fatalf("upstream refusal lost: requests=%d error=%v", requests.Load(), err)
				}
				if callbacks.Load() == 0 {
					t.Fatal("upstream redirect callback was not invoked")
				}
			})
		}
	}
}
