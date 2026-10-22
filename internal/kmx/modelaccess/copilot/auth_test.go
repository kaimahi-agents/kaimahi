package copilot

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
	"strings"
	"testing"
	"time"
)

const canary = "gho_TEST_ONLY_COPILOT_CANARY_7f13"

type memoryStore struct {
	token   string
	fail    error
	deleted int
	reads   int
	writes  int
}

func (s *memoryStore) Get() (string, error) {
	s.reads++
	if s.fail != nil {
		return "", s.fail
	}
	if s.token == "" {
		return "", ErrAuthRequired
	}
	return s.token, nil
}
func (s *memoryStore) Set(v string) error {
	s.writes++
	if s.fail != nil {
		return s.fail
	}
	s.token = v
	return nil
}
func (s *memoryStore) Delete() error { s.deleted++; s.token = ""; return s.fail }
func fixture(t *testing.T, handler http.HandlerFunc) (*Provider, *memoryStore) {
	t.Helper()
	t.Setenv("COPILOT_GITHUB_TOKEN", "")
	if err := os.Unsetenv("COPILOT_GITHUB_TOKEN"); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	store := &memoryStore{}
	p := New(store)
	p.runGH = func(context.Context) ([]byte, error) { return nil, ErrAuthRequired }
	p.runStoredGH = p.runGH
	p.http = server.Client()
	p.api = server.URL
	p.deviceURL = server.URL + "/device"
	p.tokenURL = server.URL + "/oauth"
	p.exchangeURL = server.URL + "/exchange"
	p.wait = func(ctx context.Context, _ time.Duration) error { return ctx.Err() }
	return p, store
}
func safe(t *testing.T, values ...any) {
	t.Helper()
	for _, v := range values {
		b, _ := json.Marshal(v)
		if strings.Contains(fmt.Sprint(v), canary) || bytes.Contains(b, []byte(canary)) {
			t.Fatal("credential leaked")
		}
	}
}
func TestDeviceLoginStoresOnlyReusableCredential(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("KMX_HOME", dir)
	// Missing OAuth form fields or printing the device secret must fail this test.
	polls := 0
	shown := 0
	p, store := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			io.WriteString(w, `{}`)
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.Method != "POST" || r.Form.Get("client_id") != "Iv1.b507a08c87ecfe98" || r.Header.Get("Accept") != "application/json" {
			t.Error("incorrect OAuth request")
		}
		switch r.URL.Path {
		case "/device":
			if r.Form.Get("scope") != "read:user" {
				t.Error("scope")
			}
			fmt.Fprintf(w, `{"device_code":%q,"user_code":"ABCD-EFGH","verification_uri":"https://github.com/login/device","expires_in":900,"interval":1}`, canary)
		case "/oauth":
			if r.Form.Get("device_code") != canary || r.Form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:device_code" {
				t.Error("poll form")
			}
			polls++
			switch polls {
			case 1:
				io.WriteString(w, `{"error":"authorization_pending"}`)
			case 2:
				io.WriteString(w, `{"error":"slow_down"}`)
			default:
				fmt.Fprintf(w, `{"access_token":%q,"token_type":"bearer","scope":"read:user"}`, canary)
			}
		default:
			t.Errorf("unexpected %s", r.URL.Path)
		}
	})
	var waits []time.Duration
	p.wait = func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil }
	err := p.Login(context.Background(), func(v DevicePrompt) error {
		shown++
		if v.VerificationURI != "https://github.com/login/device" || v.UserCode != "ABCD-EFGH" {
			t.Errorf("bad prompt")
		}
		safe(t, v)
		return nil
	})
	if err != nil || store.token != canary || shown != 1 || polls != 3 || len(waits) != 3 || waits[0] != time.Second || waits[2] != 6*time.Second {
		t.Fatalf("login=%v shown=%d polls=%d waits=%v", err, shown, polls, waits)
	}
	// Real successful flow must not write credentials into any test-owned state.
	if _, err := p.List(context.Background()); err == nil {
		t.Fatal("malformed catalog accepted")
	}
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			t.Fatal(err)
		}
		if !d.IsDir() {
			b, e := os.ReadFile(path)
			if e != nil {
				t.Fatal(e)
			}
			safe(t, string(b))
		}
		return nil
	})
	if err := p.Logout(); err != nil || store.token != "" || store.deleted != 1 {
		t.Fatalf("logout: %v", err)
	}
	if _, err := p.List(context.Background()); !errors.Is(err, ErrAuthRequired) {
		t.Fatalf("list after logout: %v", err)
	}
}
func TestDeviceLoginRefusalsAreSafe(t *testing.T) {
	for _, body := range []string{
		`{"device_code":"secret","user_code":"bad\ncode","verification_uri":"https://github.com/login/device","expires_in":900,"interval":1}`,
		`{"device_code":"secret","user_code":"CODE","verification_uri":"https://user:` + canary + `@github.com/login/device","expires_in":900,"interval":1}`,
		`{"error_description":"` + canary + `"}`, strings.Repeat("x", (1<<20)+1),
	} {
		t.Run(fmt.Sprint(len(body)), func(t *testing.T) {
			p, s := fixture(t, func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, body) })
			err := p.Login(context.Background(), func(DevicePrompt) error { t.Fatal("unsafe prompt"); return nil })
			if err == nil || s.token != "" {
				t.Fatal("accepted bad device response")
			}
			safe(t, err)
		})
	}
	for _, code := range []string{"access_denied", "expired_token", "other"} {
		t.Run(code, func(t *testing.T) {
			p, s := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/device" {
					io.WriteString(w, `{"device_code":"secret","user_code":"CODE","verification_uri":"https://github.com/login/device","expires_in":10,"interval":1}`)
				} else {
					fmt.Fprintf(w, `{"error":%q,"error_description":%q}`, code, canary)
				}
			})
			err := p.Login(context.Background(), func(DevicePrompt) error { return nil })
			if err == nil || s.token != "" {
				t.Fatal("accepted refusal")
			}
			safe(t, err)
		})
	}
}
func TestLoginGHExplicitBoundedAndPrivate(t *testing.T) {
	p, s := fixture(t, func(http.ResponseWriter, *http.Request) { t.Fatal("unexpected network") })
	calls := 0
	p.runGH = func(ctx context.Context) ([]byte, error) {
		calls++
		if d, ok := ctx.Deadline(); !ok || time.Until(d) > 5*time.Second {
			t.Error("missing process timeout")
		}
		return []byte(canary + "\n"), nil
	}
	if _, err := p.List(context.Background()); !errors.Is(err, ErrAuthRequired) || calls != 0 {
		t.Fatal("implicit gh reuse")
	}
	if err := p.LoginGH(context.Background()); err != nil || s.token != canary || calls != 1 {
		t.Fatalf("gh login: %v", err)
	}
	p.runGH = func(context.Context) ([]byte, error) { return []byte(canary), errors.New(canary) }
	err := p.LoginGH(context.Background())
	if err == nil {
		t.Fatal("accepted failed gh")
	}
	safe(t, err)
	p.runGH = func(context.Context) ([]byte, error) { return bytes.Repeat([]byte("x"), 16385), nil }
	if err := p.LoginGH(context.Background()); !errors.Is(err, ErrMalformed) {
		t.Fatalf("oversize gh: %v", err)
	}
}
func TestAuthFailureAndCancellationSafe(t *testing.T) {
	p, s := fixture(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	s.fail = errors.New(canary)
	_, err := p.List(context.Background())
	if !errors.Is(err, ErrKeychain) {
		t.Fatalf("store error: %v", err)
	}
	safe(t, err)
	s.fail = nil
	s.token = canary
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = p.List(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	safe(t, err)
	ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	_, err = p.List(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout: %v", err)
	}
	s.fail = errors.New(canary)
	if err := p.Logout(); !errors.Is(err, ErrKeychain) {
		t.Fatalf("logout: %v", err)
	}
	if p.bearer != "" {
		t.Fatal("logout kept memory after delete failure")
	}
}
