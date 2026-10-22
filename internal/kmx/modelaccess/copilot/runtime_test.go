package copilot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func TestRuntimeCredentialsAuthenticateWithoutPersistence(t *testing.T) {
	for _, source := range []string{"environment", "environment-over-keychain", "gh"} {
		t.Run(source, func(t *testing.T) {
			calls, sends := 0, 0
			p, store := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer "+canary {
					t.Error("selected runtime credential did not reach the model endpoint")
				}
				switch r.URL.Path {
				case "/models":
					io.WriteString(w, catalog)
				case "/chat/completions":
					sends++
					io.WriteString(w, `{"choices":[]}`)
				default:
					t.Error("unexpected OAuth or exchange request")
				}
			})
			store.fail = ErrKeychain
			if source == "environment-over-keychain" {
				store.fail, store.token = nil, "other-account"
			}
			if source != "gh" {
				t.Setenv("COPILOT_GITHUB_TOKEN", canary)
			} else {
				t.Setenv("COPILOT_GITHUB_TOKEN", "")
				os.Unsetenv("COPILOT_GITHUB_TOKEN")
			}
			p.runStoredGH = func(ctx context.Context) ([]byte, error) {
				calls++
				if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 5*time.Second {
					t.Error("runtime gh capture has no bounded deadline")
				}
				return []byte(canary + "\n"), nil
			}
			dir := t.TempDir()
			for _, name := range []string{"HOME", "XDG_CONFIG_HOME", "KMX_HOME"} {
				t.Setenv(name, dir)
			}
			p.source, p.bearer, p.legacy = "previous-source", "previous-bearer", true
			p.expires = time.Now().Add(time.Hour)
			routes, err := p.List(t.Context())
			if err != nil || len(routes) != 1 || routes[0] != selected() {
				t.Fatalf("runtime catalog: %v", err)
			}
			safe(t, routes)
			transport, err := p.Client(t.Context(), routes[0])
			if err != nil {
				t.Fatal(err)
			}
			response, err := transport.RoundTrip(chatRequest(t, `{"model":"gpt-test","messages":[]}`))
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if sends != 1 || store.writes != 0 || (calls > 0) != (source == "gh") || source != "gh" && store.reads != 0 {
				t.Fatalf("sends=%d writes=%d reads=%d gh=%d", sends, store.writes, store.reads, calls)
			}
			if files, err := os.ReadDir(dir); err != nil || len(files) != 0 {
				t.Fatal("runtime credentials created files")
			}
		})
	}
}

func TestRuntimeCredentialsFailClosed(t *testing.T) {
	for _, token := range []string{"", "bad token", "bad\ntoken", strings.Repeat("x", credentialLimit+1)} {
		p, store := fixture(t, func(http.ResponseWriter, *http.Request) { t.Error("invalid environment reached network") })
		store.token = canary
		t.Setenv("COPILOT_GITHUB_TOKEN", token)
		p.runStoredGH = func(context.Context) ([]byte, error) { t.Fatal("invalid environment switched to gh"); return nil, nil }
		_, err := p.List(t.Context())
		if !errors.Is(err, ErrAuthRevoked) || store.reads != 0 {
			t.Fatalf("invalid explicit environment did not fail closed: %v", err)
		}
		safe(t, err)
	}
	p, store := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `{"error":{"code":"policy_denied"}}`)
	})
	t.Setenv("COPILOT_GITHUB_TOKEN", "")
	os.Unsetenv("COPILOT_GITHUB_TOKEN")
	p.runStoredGH = func(context.Context) ([]byte, error) {
		t.Fatal("missing credential or refusal switched to gh")
		return nil, nil
	}
	if _, err := p.List(t.Context()); !errors.Is(err, ErrAuthRequired) {
		t.Fatalf("missing item: %v", err)
	}
	store.token = canary
	if _, err := p.List(t.Context()); !errors.Is(err, ErrPolicy) {
		t.Fatalf("policy refusal: %v", err)
	}
}

func TestRuntimeGHFailureClearsCachedCredentials(t *testing.T) {
	for _, failure := range []struct{ cause, want error }{
		{errors.New(canary), ErrKeychain},
		{context.Canceled, context.Canceled},
		{context.DeadlineExceeded, context.DeadlineExceeded},
	} {
		p, store := fixture(t, func(http.ResponseWriter, *http.Request) { t.Error("failed credential capture reached network") })
		store.fail = ErrKeychain
		p.source, p.bearer = canary, canary
		p.runStoredGH = func(context.Context) ([]byte, error) { return []byte(canary), failure.cause }
		_, err := p.List(t.Context())
		if !errors.Is(err, failure.want) || p.source != "" || p.bearer != "" || store.writes != 0 {
			t.Fatalf("runtime capture failure retained a credential or lost error identity: %v", err)
		}
		safe(t, err)
	}
}

func TestWrappedMissingCredentialNeverLeaksBackendDetails(t *testing.T) {
	p, store := fixture(t, func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, catalog) })
	store.token = canary
	transport, err := p.Client(t.Context(), selected())
	if err != nil {
		t.Fatal(err)
	}
	missing := fmt.Errorf("%s: %w", canary, ErrAuthRequired)
	for _, failure := range []error{missing, errors.Join(missing, ErrKeychain)} {
		store.fail = failure
		for _, lookup := range []func() error{
			func() error { _, err := p.List(t.Context()); return err },
			func() error { _, err := p.Client(t.Context(), selected()); return err },
			func() error {
				_, err := transport.RoundTrip(chatRequest(t, `{"model":"gpt-test","messages":[]}`))
				return err
			},
		} {
			err := lookup()
			safe(t, err)
			if err != ErrAuthRequired {
				t.Fatalf("missing credential was not normalized: %v", err)
			}
		}
	}
}

func TestRevokedRuntimeGHCredentialExplainsSafeRecovery(t *testing.T) {
	p, store := fixture(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) })
	store.fail = ErrKeychain
	p.runStoredGH = func(context.Context) ([]byte, error) { return []byte(canary), nil }
	_, err := p.List(t.Context())
	if !errors.Is(err, ErrAuthRevoked) {
		t.Fatalf("revocation: %v", err)
	}
	for _, action := range []string{"COPILOT_GITHUB_TOKEN", "gh auth login --hostname github.com", "kmx model login", "usable OS keychain"} {
		if !strings.Contains(err.Error(), action) {
			t.Errorf("revocation has no recovery guidance for %s", action)
		}
	}
	safe(t, err)
}

func TestUnavailableKeychainDoesNotStartLogin(t *testing.T) {
	p, store := fixture(t, func(http.ResponseWriter, *http.Request) { t.Error("unavailable keychain initiated OAuth") })
	store.fail = ErrKeychain
	p.runGH = func(context.Context) ([]byte, error) {
		t.Fatal("unavailable keychain captured a token to save")
		return nil, nil
	}
	if err := p.Login(t.Context(), func(DevicePrompt) error { t.Error("unexpected device prompt"); return nil }); !errors.Is(err, ErrKeychain) {
		t.Fatalf("login: %v", err)
	}
	if err := p.LoginGH(t.Context()); !errors.Is(err, ErrKeychain) {
		t.Fatalf("gh login: %v", err)
	}
	if store.writes != 0 {
		t.Fatal("unavailable keychain received a credential")
	}
}
