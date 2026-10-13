package app

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestApplyReleaseLiftRegistrySourceFree(t *testing.T) {
	a, _, opt, dir := releaseApplyFixture(t, "both")
	layout := opt.FromLayout
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/" {
			w.WriteHeader(http.StatusOK)
			return
		}
		_, digest, ok := strings.Cut(r.URL.Path, "/manifests/")
		manifest := ok
		if !ok {
			_, digest, ok = strings.Cut(r.URL.Path, "/blobs/")
		}
		if !ok || !strings.HasPrefix(digest, "sha256:") {
			http.NotFound(w, r)
			return
		}
		raw, err := os.ReadFile(filepath.Join(layout, "blobs", "sha256", strings.TrimPrefix(digest, "sha256:")))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if manifest {
			w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
		} else {
			w.Header().Set("Content-Type", "application/octet-stream")
		}
		w.Header().Set("Docker-Content-Digest", digest)
		w.Header().Set("Content-Length", strconv.Itoa(len(raw)))
		if r.Method != "HEAD" {
			_, _ = w.Write(raw)
		}
	}))
	defer server.Close()
	_, digest, _ := strings.Cut(opt.Reference, "@")
	opt.Reference = strings.TrimPrefix(server.URL, "http://") + "/suite@" + digest
	opt.FromLayout, opt.PlainHTTP = "", true
	p, err := a.PrepareReleaseLift(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	result, err := a.ApplyReleaseLift(t.Context(), p, p.Summary().PlanDigest)
	if err != nil || result.State != "ready" {
		t.Fatalf("registry apply: %+v %v", result, err)
	}
	if len(persistentOrkaCalls(t, dir)) != 2 {
		t.Fatal("registry lift changed write behavior")
	}
}
