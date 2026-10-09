package oras

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"oras.land/oras-go/v2/errdef"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/credentials"
)

// Route a literal remote hostname to a local fixture without changing the
// hostname evaluated by the safety policy or mutating any default transports.
func routeRegistryClient(t *testing.T, client interface {
	Do(*http.Request) (*http.Response, error)
}, server *httptest.Server) {
	t.Helper()
	var httpClient *http.Client
	switch c := client.(type) {
	case *auth.Client:
		copyClient := *c.Client
		c.Client = &copyClient
		httpClient = c.Client
	case *anonymousRegistryClient:
		httpClient = c.Client
	default:
		t.Fatalf("unsupported registry client %T", client)
	}

	transport := server.Client().Transport.(*http.Transport).Clone()
	transport.Proxy = nil
	if transport.TLSClientConfig != nil {
		transport.TLSClientConfig.ServerName = "127.0.0.1"
	}
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	t.Cleanup(transport.CloseIdleConnections)
	// Keep the production boundary guard in place when replacing network I/O.
	guarded, ok := httpClient.Transport.(*registryTransport)
	if !ok {
		t.Fatalf("missing credential boundary transport: %T", httpClient.Transport)
	}
	guarded.base = transport
}

type responseRecordingTransport struct {
	base     http.RoundTripper
	response *http.Response
}

func (t *responseRecordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	t.response = resp
	return resp, err
}

func TestAnonymousRegistryUnauthorizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, "authentication required")
	}))
	defer server.Close()
	for _, layer := range []string{"transport", "Do"} {
		t.Run(layer, func(t *testing.T) {
			repository, _, err := newRegistryRepository("remote.example/team:v1", true, nil)
			if err != nil {
				t.Fatal(err)
			}
			routeRegistryClient(t, repository.Client, server)
			// Capture the real response beneath the guard to check body ownership.
			httpClient := repository.Client.(*anonymousRegistryClient).Client
			guard := httpClient.Transport.(*registryTransport)
			recorder := &responseRecordingTransport{base: guard.base}
			guard.base = recorder
			req, err := http.NewRequest(http.MethodGet, "http://remote.example/v2/", nil)
			if err != nil {
				t.Fatal(err)
			}
			if layer == "transport" {
				resp, err := guard.RoundTrip(req)
				if err != nil {
					t.Fatalf("RoundTrip converted HTTP status to error: %v", err)
				}
				defer resp.Body.Close()
				if resp.StatusCode != http.StatusUnauthorized {
					t.Fatalf("status = %d, want 401", resp.StatusCode)
				}
				body, err := io.ReadAll(resp.Body)
				if err != nil || string(body) != "authentication required" {
					t.Fatalf("response body = %q, %v, want open body", body, err)
				}
				return
			}
			resp, err := repository.Client.Do(req)
			if resp != nil || err == nil || !strings.Contains(err.Error(), "registry requires authentication; refusing authentication over plain HTTP to a non-loopback host; use HTTPS") {
				t.Fatalf("Do = %v, %v, want authentication refusal", resp, err)
			}
			if recorder.response == nil {
				t.Fatal("no HTTP response received")
			}
			if _, err := io.ReadAll(recorder.response.Body); err == nil {
				t.Fatal("refused response body was not closed")
			}
		})
	}
}

func TestRemotePlainHTTPNeverAuthenticates(t *testing.T) {
	for _, challenge := range []string{"basic", "bearer"} {
		t.Run(challenge, func(t *testing.T) {
			t.Setenv("KMX_HOME", t.TempDir())
			configRoot := t.TempDir()
			if err := os.WriteFile(filepath.Join(configRoot, "config.json"), []byte(`{"auths":{"remote.example":{"auth":"YWdlbnQ6c2VjcmV0"}}}`), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("DOCKER_CONFIG", configRoot)
			var authorization, tokenRequests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "" {
					authorization.Add(1)
				}
				if r.URL.Path == "/token" {
					tokenRequests.Add(1)
					_, _ = io.WriteString(w, `{"token":"anonymous-token"}`)
					return
				}
				if challenge == "basic" {
					w.Header().Set("WWW-Authenticate", `Basic realm="registry"`)
				} else {
					w.Header().Set("WWW-Authenticate", `Bearer realm="http://remote.example/token"`)
				}
				w.WriteHeader(http.StatusUnauthorized)
			}))
			defer server.Close()
			repository, _, err := newRegistryRepository("remote.example/team:v1", true, nil)
			if err != nil {
				t.Fatal(err)
			}
			routeRegistryClient(t, repository.Client, server)
			_, err = repository.Resolve(context.Background(), "v1")
			if err == nil || !strings.Contains(err.Error(), "plain HTTP") {
				t.Errorf("auth challenge error = %v, want plain HTTP refusal", err)
			}
			if got := authorization.Load(); got != 0 {
				t.Errorf("remote received %d Authorization headers", got)
			}
			if got := tokenRequests.Load(); got != 0 {
				t.Errorf("remote received %d token requests", got)
			}
		})
	}
}

func TestRemotePlainHTTPSkipsDockerConfig(t *testing.T) {
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	if err := os.WriteFile(filepath.Join(os.Getenv("DOCKER_CONFIG"), "config.json"), []byte(`invalid JSON`), 0600); err != nil {
		t.Fatal(err)
	}
	repository, _, err := newRegistryRepository("remote.example/team:v1", true, nil)
	if err != nil {
		t.Fatalf("anonymous remote HTTP opened Docker config: %v", err)
	}
	server, _ := newTestRegistryServer("", "")
	defer server.Close()
	routeRegistryClient(t, repository.Client, server)
	_, err = repository.Resolve(context.Background(), "absent")
	if !errors.Is(err, errdef.ErrNotFound) {
		t.Fatalf("anonymous remote access error = %v, want not found", err)
	}
}

func TestRegistryRefusesRemoteHTTPRealm(t *testing.T) {
	for _, tls := range []bool{false, true} {
		for _, credential := range []auth.Credential{{Username: "agent", Password: "secret"}, {RefreshToken: "refresh-secret"}} {
			t.Run(fmt.Sprintf("tls=%t/refresh=%t", tls, credential.RefreshToken != ""), func(t *testing.T) {
				var delivered atomic.Int32
				server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/token" {
						delivered.Add(1)
						_, _ = io.WriteString(w, `{"token":"token","access_token":"token"}`)
						return
					}
					w.Header().Set("WWW-Authenticate", `Bearer realm="http://remote.example/token"`)
					w.WriteHeader(http.StatusUnauthorized)
				}))
				if tls {
					server.StartTLS()
				} else {
					server.Start()
				}
				defer server.Close()
				host := strings.TrimPrefix(strings.TrimPrefix(server.URL, "http://"), "https://")
				store := credentials.NewMemoryStore()
				if err := store.Put(context.Background(), host, credential); err != nil {
					t.Fatal(err)
				}
				repository, _, err := newRegistryRepository(host+"/team:v1", !tls, store)
				if err != nil {
					t.Fatal(err)
				}
				routeRegistryClient(t, repository.Client, server)
				_, err = repository.Resolve(context.Background(), "v1")
				want := "refusing credentials over plain HTTP"
				if tls {
					want = "uses http but registry was contacted over https"
				}
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Errorf("realm error = %v, want %q", err, want)
				}
				if got := delivered.Load(); got != 0 {
					t.Errorf("remote realm received %d credential-bearing requests", got)
				}
			})
		}
	}
}

func TestHTTPSRegistryAuthenticationUnchanged(t *testing.T) {
	registry := &testRegistry{blobs: make(map[string][]byte), manifests: make(map[string]testManifest), username: "agent", password: "secret"}
	server := httptest.NewTLSServer(registry)
	defer server.Close()
	host := strings.TrimPrefix(server.URL, "https://")
	store := credentials.NewMemoryStore()
	if err := store.Put(context.Background(), host, auth.Credential{Username: "agent", Password: "secret"}); err != nil {
		t.Fatal(err)
	}
	repository, _, err := newRegistryRepository(host+"/team:v1", false, store)
	if err != nil {
		t.Fatal(err)
	}
	routeRegistryClient(t, repository.Client, server)
	_, err = repository.Resolve(context.Background(), "v1")
	if !errors.Is(err, errdef.ErrNotFound) {
		t.Fatalf("HTTPS resolution error = %v, want not found", err)
	}
	_, authenticated := registry.authenticationCounts()
	if authenticated == 0 {
		t.Fatal("HTTPS registry received no authenticated requests")
	}
}

func TestRemotePlainHTTPDoesNotReuseAuthentication(t *testing.T) {
	// Warm an authenticated client cache on the same host over HTTPS, then
	// ensure an anonymous HTTP client cannot reuse its Basic token.
	var leaked, authenticated atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.Header().Set("WWW-Authenticate", `Basic realm="registry"`)
			w.WriteHeader(401)
			return
		}
		authenticated.Add(1)
		w.WriteHeader(404)
	}))
	defer server.Close()
	store := credentials.NewMemoryStore()
	if err := store.Put(context.Background(), "remote.example", auth.Credential{Username: "agent", Password: "secret"}); err != nil {
		t.Fatal(err)
	}
	secure, _, err := newRegistryRepository("remote.example/team:v1", false, store)
	if err != nil {
		t.Fatal(err)
	}
	routeRegistryClient(t, secure.Client, server)
	_, err = secure.Resolve(context.Background(), "v1")
	if !errors.Is(err, errdef.ErrNotFound) || authenticated.Load() == 0 {
		t.Fatalf("cache warm-up error = %v, authenticated requests = %d", err, authenticated.Load())
	}
	insecureServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			leaked.Add(1)
		}
		w.WriteHeader(404)
	}))
	defer insecureServer.Close()
	insecure, _, err := newRegistryRepository("remote.example/team:v1", true, store)
	if err != nil {
		t.Fatal(err)
	}
	routeRegistryClient(t, insecure.Client, insecureServer)
	_, err = insecure.Resolve(context.Background(), "v1")
	if !errors.Is(err, errdef.ErrNotFound) {
		t.Fatalf("anonymous HTTP resolution error = %v, want not found", err)
	}
	if leaked.Load() != 0 {
		t.Fatal("HTTP registry received cached HTTPS credentials")
	}
}

func TestRegistryPushTagPreflight(t *testing.T) {
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	t.Setenv("KMX_HOME", t.TempDir())
	fixtureServer, registry := newTestRegistryServer("", "")
	fixtureServer.Close()
	var mutations atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			mutations.Add(1)
		}
		registry.ServeHTTP(w, r)
	}))
	defer server.Close()
	source := filepath.Join(t.TempDir(), "source")
	copyDirectory(t, filepath.Join("..", "testdata", "minimal"), source)
	reference := strings.TrimPrefix(server.URL, "http://") + "/team/suite:v1"
	first, err := PushRegistry(context.Background(), source, reference, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if mutations.Load() == 0 {
		t.Fatal("new tag caused no mutations")
	}
	mutations.Store(0)
	second, err := PushRegistry(context.Background(), source, reference, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if second.Descriptor.Digest != first.Descriptor.Digest {
		t.Fatal("same suite changed digest")
	}
	if mutations.Load() != 0 {
		t.Errorf("same-digest push caused %d mutations", mutations.Load())
	}
	// Add valid unreferenced content to change the archive without invalidating
	// the fixture's digest-bound manifest records.
	if err := os.WriteFile(filepath.Join(source, "extra.txt"), []byte("changed content"), 0644); err != nil {
		t.Fatal(err)
	}
	packedRoot, _, packed, err := packDirectory(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(packedRoot)
	mutations.Store(0)
	_, err = PushRegistry(context.Background(), source, reference, true, false)
	if err == nil || !strings.Contains(err.Error(), first.Descriptor.Digest.String()) || !strings.Contains(err.Error(), packed.Descriptor.Digest.String()) {
		t.Errorf("different-digest push error = %v, want both digests", err)
	}
	if mutations.Load() != 0 {
		t.Errorf("refused push caused %d mutations", mutations.Load())
	}
	registry.mu.RLock()
	current := registry.manifests[registryKey("team/suite", "v1")].digest
	registry.mu.RUnlock()
	if current != first.Descriptor.Digest.String() {
		t.Errorf("tag moved to %s after refusal", current)
	}
	forced, err := PushRegistry(context.Background(), source, reference, true, true)
	if err != nil {
		t.Fatalf("forced overwrite: %v", err)
	}
	if forced.Descriptor.Digest != packed.Descriptor.Digest {
		t.Fatalf("forced push digest = %s, want %s", forced.Descriptor.Digest, packed.Descriptor.Digest)
	}
	registry.mu.RLock()
	current = registry.manifests[registryKey("team/suite", "v1")].digest
	registry.mu.RUnlock()
	if current != packed.Descriptor.Digest.String() {
		t.Fatalf("force did not move tag: %s", current)
	}
	mutations.Store(0)
	if _, err := PushRegistry(context.Background(), source, reference, true, true); err != nil {
		t.Fatal(err)
	}
	if mutations.Load() != 0 {
		t.Errorf("forced same-digest push caused %d mutations", mutations.Load())
	}
}

func TestRegistryPushExistenceFailure(t *testing.T) {
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	source := filepath.Join(t.TempDir(), "source")
	copyDirectory(t, filepath.Join("..", "testdata", "minimal"), source)
	var mutations, tagChecks atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/team/manifests/v1" {
			tagChecks.Add(1)
		}
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet {
			mutations.Add(1)
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	for _, force := range []bool{false, true} {
		tagChecks.Store(0)
		_, err := PushRegistry(context.Background(), source, strings.TrimPrefix(server.URL, "http://")+"/team:v1", true, force)
		if err == nil || !strings.Contains(err.Error(), "check existing AgentSuite registry tag") || !strings.Contains(err.Error(), "403") {
			t.Errorf("existence failure (force=%t) = %v, want forbidden preflight", force, err)
		}
		if mutations.Load() != 0 {
			t.Errorf("existence failure caused %d mutations", mutations.Load())
		}
		if tagChecks.Load() != 1 {
			t.Errorf("preflight checked tag %d times, want 1", tagChecks.Load())
		}
	}
}

func TestRegistryPushValidatesBeforePreflight(t *testing.T) {
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	_, err := PushRegistry(context.Background(), t.TempDir(), strings.TrimPrefix(server.URL, "http://")+"/team:v1", true, false)
	if err == nil || !strings.Contains(err.Error(), "validate packed AgentSuite") || !strings.Contains(err.Error(), "required file agentsuite.json is missing") {
		t.Fatalf("invalid suite error = %v, want missing suite manifest validation failure", err)
	}
	if requests.Load() != 0 {
		t.Errorf("invalid suite caused %d registry requests", requests.Load())
	}
}
