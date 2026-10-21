package copilot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/zalando/go-keyring"
)

const clientID = "Iv1.b507a08c87ecfe98"
const responseLimit = 1 << 20
const credentialLimit = 16 << 10

// Store owns only kmx's reusable GitHub credential. Missing credentials return
// ErrAuthRequired. Implementations must use protected storage, not plaintext files.
type Store interface {
	Get() (string, error)
	Set(string) error
	Delete() error
}

// Provider serializes auth changes and catalog resolution. Exchange bearers are
// memory-only; existing transports resolve through this owner even after logout.
type Provider struct {
	mu                                    sync.Mutex
	store                                 Store
	http                                  *http.Client
	api, deviceURL, tokenURL, exchangeURL string
	source, bearer                        string
	expires                               time.Time
	legacy                                bool
	now                                   func() time.Time
	wait                                  func(context.Context, time.Duration) error
	runGH                                 func(context.Context) ([]byte, error)
}

// New does not open a keychain, start a subprocess, or make a network request.
// A nil store selects the native OS keychain, opened lazily on explicit use.
func New(store Store) *Provider {
	if store == nil {
		store = &keychainStore{get: keyring.Get, set: keyring.Set, delete: keyring.Delete}
	}
	return &Provider{store: store, http: &http.Client{Timeout: 30 * time.Second}, api: "https://api.githubcopilot.com", deviceURL: "https://github.com/login/device/code", tokenURL: "https://github.com/login/oauth/access_token", exchangeURL: "https://api.github.com/copilot_internal/v2/token", now: time.Now, wait: wait, runGH: githubToken}
}

// DevicePrompt contains only values safe to display, never the device secret.
type DevicePrompt struct {
	VerificationURI string
	UserCode        string
}

var userCode = regexp.MustCompile(`^[A-Z0-9-]{4,32}$`)

// Login starts GitHub's device flow. show should display the URI and code on
// stderr. It never opens a browser or falls back to another credential source.
func (p *Provider) Login(ctx context.Context, show func(DevicePrompt) error) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	var d struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		VerificationURI string `json:"verification_uri"`
		ExpiresIn       int    `json:"expires_in"`
		Interval        int    `json:"interval"`
	}
	if err := p.form(ctx, p.deviceURL, url.Values{"client_id": {clientID}, "scope": {"read:user"}}, &d); err != nil {
		return err
	}
	if !validCredential(d.DeviceCode) || !userCode.MatchString(d.UserCode) || d.VerificationURI != "https://github.com/login/device" || d.ExpiresIn < 1 || d.ExpiresIn > 900 || d.Interval < 1 || d.Interval > 60 || show == nil {
		return ErrMalformed
	}
	if err := show(DevicePrompt{d.VerificationURI, d.UserCode}); err != nil {
		return safeError(err)
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(d.ExpiresIn)*time.Second)
	defer cancel()
	interval := time.Duration(d.Interval) * time.Second
	for {
		if err := p.wait(ctx, interval); err != nil {
			return safeError(err)
		}
		var a struct {
			AccessToken string `json:"access_token"`
			TokenType   string `json:"token_type"`
			Error       string `json:"error"`
		}
		err := p.form(ctx, p.tokenURL, url.Values{"client_id": {clientID}, "device_code": {d.DeviceCode}, "grant_type": {"urn:ietf:params:oauth:grant-type:device_code"}}, &a)
		if err != nil {
			return err
		}
		switch a.Error {
		case "authorization_pending":
			continue
		case "slow_down":
			interval += 5 * time.Second
			continue
		case "access_denied":
			return ErrPolicy
		case "expired_token":
			return ErrAuthRevoked
		case "":
		default:
			return ErrAuthRevoked
		}
		if !validCredential(a.AccessToken) || !strings.EqualFold(a.TokenType, "bearer") {
			return ErrMalformed
		}
		return p.save(a.AccessToken)
	}
}

// LoginGH is the only path that invokes gh. Its stdout stays private and only
// the reusable credential is copied into kmx's own keychain item.
func (p *Provider) LoginGH(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	b, err := p.runGH(ctx)
	if errors.Is(err, ErrMalformed) {
		return ErrMalformed
	}
	if err != nil {
		return safeError(err)
	}
	if len(b) > credentialLimit {
		return ErrMalformed
	}
	token := strings.TrimSpace(string(b))
	if !validCredential(token) {
		return ErrMalformed
	}
	return p.save(token)
}
func (p *Provider) save(token string) error {
	if err := p.store.Set(token); err != nil {
		return ErrKeychain
	}
	p.clear()
	return nil
}
func (p *Provider) clear() { p.source = ""; p.bearer = ""; p.expires = time.Time{}; p.legacy = false }

// Logout deletes only the kmx-owned item; it never revokes gh or editor sign-in.
// Memory is cleared even when deletion fails; the failure must not be ignored.
func (p *Provider) Logout() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.clear()
	if err := p.store.Delete(); err != nil && !errors.Is(err, ErrAuthRequired) {
		return ErrKeychain
	}
	return nil
}
func validCredential(s string) bool {
	if len(s) == 0 || len(s) > credentialLimit {
		return false
	}
	for _, c := range []byte(s) {
		if c < 33 || c > 126 {
			return false
		}
	}
	return true
}
func wait(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
func (p *Provider) form(ctx context.Context, endpoint string, values url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return ErrUnavailable
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := p.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := readBounded(resp.Body, responseLimit)
	if err != nil {
		return err
	}
	if resp.StatusCode != 200 {
		return classify(resp.StatusCode, b)
	}
	if json.Unmarshal(b, out) != nil {
		return ErrMalformed
	}
	return nil
}
func (p *Provider) do(req *http.Request) (*http.Response, error) {
	c := *p.http
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if c.Timeout == 0 {
		c.Timeout = 30 * time.Second
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, safeError(err)
	}
	return resp, nil
}
func readBounded(r io.Reader, limit int) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, int64(limit)+1))
	if err != nil {
		return nil, safeError(err)
	}
	if len(b) > limit {
		return nil, ErrMalformed
	}
	return b, nil
}

// Do not embed bytes.Buffer: its promoted ReadFrom would bypass Write's cap.
type limitedOutput struct{ buffer bytes.Buffer }

func (b *limitedOutput) Len() int      { return b.buffer.Len() }
func (b *limitedOutput) Bytes() []byte { return b.buffer.Bytes() }
func (b *limitedOutput) Write(v []byte) (int, error) {
	if b.Len()+len(v) > credentialLimit {
		return 0, ErrMalformed
	}
	return b.buffer.Write(v)
}
func githubToken(ctx context.Context) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", "auth", "token", "--hostname", "github.com")
	cmd.WaitDelay = 100 * time.Millisecond
	var out limitedOutput
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(err, ErrMalformed) {
			return nil, ErrMalformed
		}
		return nil, ErrUnavailable
	}
	return out.Bytes(), nil
}
