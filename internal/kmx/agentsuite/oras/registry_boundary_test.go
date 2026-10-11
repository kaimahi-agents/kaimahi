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
	"strings"
	"sync/atomic"
	"testing"

	"oras.land/oras-go/v2/errdef"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/credentials"
)

func TestPlainHTTPAuthenticationUsesLiteralLoopbackOnly(t *testing.T) {
	for _, test := range []struct {
		host          string
		authenticated bool
	}{
		{"localhost", true}, {"LOCALHOST", true}, {"127.0.0.1", true}, {"127.42.3.4", true}, {"[::1]", true},
		{"localhost.example", false}, {"localhost.", false}, {"192.0.2.1", false}, {"[::2]", false},
	} {
		t.Run(test.host, func(t *testing.T) {
			server, registry := newTestRegistryServer("agent", "secret")
			defer server.Close()
			store := credentials.NewMemoryStore()
			if err := store.Put(context.Background(), test.host, auth.Credential{Username: "agent", Password: "secret"}); err != nil {
				t.Fatal(err)
			}
			repository, _, err := newRegistryRepository(test.host+"/team:v1", true, store)
			if err != nil {
				t.Fatal(err)
			}
			routeRegistryClient(t, repository.Client, server)
			_, err = repository.Resolve(context.Background(), "v1")
			if !test.authenticated && (err == nil || !strings.Contains(err.Error(), "plain HTTP")) {
				t.Errorf("remote HTTP error = %v, want policy refusal", err)
			}
			challenges, authenticated := registry.authenticationCounts()
			if challenges == 0 || (authenticated > 0) != test.authenticated {
				t.Errorf("challenges = %d, authenticated requests = %d, want authenticated=%t", challenges, authenticated, test.authenticated)
			}
		})
	}
}

func TestRegistryOAuthRedirectCannotDeliverCredentialBodyOverRemoteHTTP(t *testing.T) {
	var delivered, allowedTokenRequests atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		delivered.Add(1)
		_, _ = io.WriteString(w, `{"access_token":"token"}`)
	}))
	defer remote.Close()
	var realm string
	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			if r.Method != http.MethodPost {
				t.Errorf("token method = %s, want POST", r.Method)
			}
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			if r.Form.Get("refresh_token") != "refresh-secret" {
				t.Errorf("expected OAuth refresh credential on allowed HTTPS request")
			}
			allowedTokenRequests.Add(1)
			w.Header().Set("Location", "http://remote.example/token")
			w.WriteHeader(http.StatusTemporaryRedirect)
			return
		}
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm=%q`, realm))
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer secure.Close()
	realm = secure.URL + "/token"
	host := strings.TrimPrefix(secure.URL, "https://")
	store := credentials.NewMemoryStore()
	if err := store.Put(context.Background(), host, auth.Credential{RefreshToken: "refresh-secret"}); err != nil {
		t.Fatal(err)
	}
	repository, _, err := newRegistryRepository(host+"/team:v1", false, store)
	if err != nil {
		t.Fatal(err)
	}
	routeRegistryClient(t, repository.Client, secure)
	transport := repository.Client.(*auth.Client).Client.Transport.(*registryTransport).base.(*http.Transport)
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		target := secure.Listener.Addr().String()
		if strings.HasPrefix(address, "remote.example:") {
			target = remote.Listener.Addr().String()
		}
		return (&net.Dialer{}).DialContext(ctx, network, target)
	}
	_, err = repository.Resolve(context.Background(), "v1")
	if err == nil || !strings.Contains(err.Error(), "plain HTTP") {
		t.Errorf("OAuth redirect error = %v, want plain HTTP refusal", err)
	}
	if allowedTokenRequests.Load() != 1 {
		t.Errorf("allowed token requests = %d, want 1", allowedTokenRequests.Load())
	}
	if delivered.Load() != 0 {
		t.Errorf("remote HTTP received %d credential body requests", delivered.Load())
	}
}

func TestAnonymousRegistryRejectsInjectedAuthorization(t *testing.T) {
	var delivered atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { delivered.Add(1); w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	repository, _, err := newRegistryRepository("remote.example/team:v1", true, credentials.NewMemoryStore())
	if err != nil {
		t.Fatal(err)
	}
	routeRegistryClient(t, repository.Client, server)
	body, err := os.CreateTemp(t.TempDir(), "request-body")
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	req, err := http.NewRequest(http.MethodPost, "http://remote.example/v2/", body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer injected-token")
	_, err = repository.Client.Do(req)
	if err == nil || !strings.Contains(err.Error(), "plain HTTP") {
		t.Errorf("credential injection error = %v, want plain HTTP refusal", err)
	}
	if delivered.Load() != 0 {
		t.Errorf("remote received %d injected credential requests", delivered.Load())
	}
	if _, err := body.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Errorf("refused request body was not closed: %v", err)
	}
}

func TestAuthenticatedRegistryUsesFreshCache(t *testing.T) {
	server, registry := newTestRegistryServer("agent", "secret")
	defer server.Close()
	host := strings.TrimPrefix(server.URL, "http://")
	store := credentials.NewMemoryStore()
	if err := store.Put(context.Background(), host, auth.Credential{Username: "agent", Password: "secret"}); err != nil {
		t.Fatal(err)
	}
	first, _, err := newRegistryRepository(host+"/team:v1", true, store)
	if err != nil {
		t.Fatal(err)
	}
	_, err = first.Resolve(context.Background(), "v1")
	if !errors.Is(err, errdef.ErrNotFound) {
		t.Fatalf("cache warm-up error = %v, want not found", err)
	}
	_, before := registry.authenticationCounts()
	if before == 0 {
		t.Fatal("cache warm-up did not authenticate")
	}
	second, _, err := newRegistryRepository(host+"/team:v1", true, credentials.NewMemoryStore())
	if err != nil {
		t.Fatal(err)
	}
	_, err = second.Resolve(context.Background(), "v1")
	if !errors.Is(err, auth.ErrBasicCredentialNotFound) {
		t.Errorf("fresh cache error = %v, want missing credentials", err)
	}
	_, after := registry.authenticationCounts()
	if after != before {
		t.Errorf("fresh client reused another client's auth token: authenticated requests %d -> %d", before, after)
	}
}
