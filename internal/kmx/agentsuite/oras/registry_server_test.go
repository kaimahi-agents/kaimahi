package oras

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
)

type testRegistry struct {
	mu                    sync.RWMutex
	blobs                 map[string][]byte
	manifests             map[string]testManifest
	username              string
	password              string
	uploads               int
	authChallenges        int
	authenticatedRequests int
}

type testManifest struct {
	contentType string
	content     []byte
	digest      string
}

func newTestRegistryServer(username, password string) (*httptest.Server, *testRegistry) {
	registry := &testRegistry{
		blobs:     make(map[string][]byte),
		manifests: make(map[string]testManifest),
		username:  username,
		password:  password,
	}
	return httptest.NewServer(registry), registry
}

func (r *testRegistry) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if r.username != "" {
		username, password, ok := req.BasicAuth()
		if !ok || username != r.username || password != r.password {
			r.mu.Lock()
			r.authChallenges++
			r.mu.Unlock()
			w.Header().Set("WWW-Authenticate", `Basic realm="test-registry"`)
			r.writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
			return
		}
		r.mu.Lock()
		r.authenticatedRequests++
		r.mu.Unlock()
	}
	if req.URL.Path == "/v2" || req.URL.Path == "/v2/" {
		w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
		w.WriteHeader(http.StatusOK)
		return
	}
	if !strings.HasPrefix(req.URL.Path, "/v2/") {
		r.writeError(w, http.StatusNotFound, "NAME_UNKNOWN", "repository not found")
		return
	}

	path := strings.TrimPrefix(req.URL.Path, "/v2/")
	switch {
	case strings.Contains(path, "/blobs/uploads/"):
		r.serveUpload(w, req, path)
	case strings.Contains(path, "/blobs/"):
		r.serveBlob(w, req, path)
	case strings.Contains(path, "/manifests/"):
		r.serveManifest(w, req, path)
	default:
		r.writeError(w, http.StatusNotFound, "NAME_UNKNOWN", "repository not found")
	}
}

func (r *testRegistry) authenticationCounts() (int, int) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.authChallenges, r.authenticatedRequests
}

func (r *testRegistry) serveUpload(w http.ResponseWriter, req *http.Request, path string) {
	repository, target, ok := splitRegistryPath(path, "/blobs/uploads/")
	if !ok || repository == "" {
		r.writeError(w, http.StatusBadRequest, "NAME_INVALID", "invalid repository")
		return
	}
	switch req.Method {
	case http.MethodPost:
		if target != "" {
			r.writeUnsupported(w, req)
			return
		}
		r.mu.Lock()
		r.uploads++
		upload := strconv.Itoa(r.uploads)
		r.mu.Unlock()
		w.Header().Set("Location", "/v2/"+repository+"/blobs/uploads/"+upload)
		w.WriteHeader(http.StatusAccepted)
	case http.MethodPut:
		if target == "" {
			r.writeUnsupported(w, req)
			return
		}
		expected := req.URL.Query().Get("digest")
		content, err := io.ReadAll(req.Body)
		if err != nil {
			r.writeError(w, http.StatusInternalServerError, "UNKNOWN", err.Error())
			return
		}
		actual := contentDigest(content)
		if expected == "" || expected != actual {
			r.writeError(w, http.StatusBadRequest, "DIGEST_INVALID", "content digest does not match")
			return
		}
		r.mu.Lock()
		r.blobs[actual] = content
		r.mu.Unlock()
		w.Header().Set("Docker-Content-Digest", actual)
		w.Header().Set("Location", "/v2/"+repository+"/blobs/"+actual)
		w.WriteHeader(http.StatusCreated)
	default:
		r.writeUnsupported(w, req)
	}
}

func (r *testRegistry) serveBlob(w http.ResponseWriter, req *http.Request, path string) {
	_, digest, ok := splitRegistryPath(path, "/blobs/")
	if !ok || digest == "" {
		r.writeError(w, http.StatusBadRequest, "DIGEST_INVALID", "invalid digest")
		return
	}
	r.mu.RLock()
	content, found := r.blobs[digest]
	r.mu.RUnlock()
	if !found {
		r.writeError(w, http.StatusNotFound, "BLOB_UNKNOWN", "blob not found")
		return
	}

	w.Header().Set("Content-Length", strconv.Itoa(len(content)))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Docker-Content-Digest", digest)
	switch req.Method {
	case http.MethodHead:
		w.WriteHeader(http.StatusOK)
	case http.MethodGet:
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(content)
	default:
		r.writeUnsupported(w, req)
	}
}

func (r *testRegistry) serveManifest(w http.ResponseWriter, req *http.Request, path string) {
	repository, reference, ok := splitRegistryPath(path, "/manifests/")
	if !ok || repository == "" || reference == "" {
		r.writeError(w, http.StatusBadRequest, "MANIFEST_INVALID", "invalid manifest reference")
		return
	}
	key := registryKey(repository, reference)
	if req.Method == http.MethodPut {
		content, err := io.ReadAll(req.Body)
		if err != nil {
			r.writeError(w, http.StatusInternalServerError, "UNKNOWN", err.Error())
			return
		}
		digest := contentDigest(content)
		if strings.Contains(reference, ":") && reference != digest {
			r.writeError(w, http.StatusBadRequest, "DIGEST_INVALID", "manifest digest does not match")
			return
		}
		manifest := testManifest{
			contentType: req.Header.Get("Content-Type"),
			content:     content,
			digest:      digest,
		}
		r.mu.Lock()
		r.manifests[key] = manifest
		r.manifests[registryKey(repository, digest)] = manifest
		r.mu.Unlock()
		w.Header().Set("Docker-Content-Digest", digest)
		w.Header().Set("Location", "/v2/"+repository+"/manifests/"+reference)
		w.WriteHeader(http.StatusCreated)
		return
	}

	r.mu.RLock()
	manifest, found := r.manifests[key]
	r.mu.RUnlock()
	if !found {
		r.writeError(w, http.StatusNotFound, "MANIFEST_UNKNOWN", "manifest not found")
		return
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(manifest.content)))
	w.Header().Set("Content-Type", manifest.contentType)
	w.Header().Set("Docker-Content-Digest", manifest.digest)
	switch req.Method {
	case http.MethodHead:
		w.WriteHeader(http.StatusOK)
	case http.MethodGet:
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(manifest.content)
	default:
		r.writeUnsupported(w, req)
	}
}

func (r *testRegistry) writeUnsupported(w http.ResponseWriter, req *http.Request) {
	r.writeError(
		w,
		http.StatusMethodNotAllowed,
		"UNSUPPORTED",
		fmt.Sprintf("%s %s is not supported by the test registry", req.Method, req.URL.Path),
	)
}

func (r *testRegistry) writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"errors": []map[string]string{{
			"code":    code,
			"message": message,
		}},
	})
}

func splitRegistryPath(path, marker string) (string, string, bool) {
	repository, target, ok := strings.Cut(path, marker)
	return repository, target, ok
}

func registryKey(repository, reference string) string {
	return repository + "\x00" + reference
}

func contentDigest(content []byte) string {
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
}
