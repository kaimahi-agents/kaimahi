package copilot

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
)

func TestRuntimeGHPrecedesKeychain(t *testing.T) {
	for _, state := range []string{"present", "missing", "unavailable"} {
		t.Run(state, func(t *testing.T) {
			p, store := fixture(t, func(w http.ResponseWriter, req *http.Request) {
				if req.Header.Get("Authorization") != "Bearer "+canary {
					t.Error("gh did not supply the credential")
				}
				io.WriteString(w, catalog)
			})
			if state == "present" {
				store.token = "old-account"
			}
			if state == "unavailable" {
				store.fail = ErrKeychain
			}
			p.runStoredGH = func(context.Context) ([]byte, error) { return []byte(canary), nil }
			if _, err := p.List(t.Context()); err != nil {
				t.Fatal(err)
			}
			if store.reads != 0 || store.writes != 0 {
				t.Fatalf("gh depended on the keychain: reads=%d writes=%d", store.reads, store.writes)
			}
		})
	}
}

func TestRuntimeGHUnavailableAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		name             string
		ghError          error
		token            string
		storeError, want error
	}{
		{"no-gh-use-keychain", ErrUnavailable, canary, nil, nil},
		{"inner-gh-timeout-use-keychain", context.DeadlineExceeded, canary, nil, nil},
		{"both-unavailable", ErrUnavailable, "", ErrKeychain, ErrAuthRequired},
		{"malformed-gh-no-alternate", ErrMalformed, canary, nil, ErrMalformed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, store := fixture(t, func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, catalog) })
			store.token, store.fail = tc.token, tc.storeError
			p.runStoredGH = func(context.Context) ([]byte, error) { return nil, tc.ghError }
			_, err := p.List(t.Context())
			if !errors.Is(err, tc.want) {
				t.Fatalf("runtime auth: got %v want %v", err, tc.want)
			}
			if store.writes != 0 {
				t.Fatal("runtime credential persisted")
			}
		})
	}
	p, store := fixture(t, func(http.ResponseWriter, *http.Request) { t.Error("cancelled auth reached network") })
	store.token = canary
	p.runStoredGH = func(ctx context.Context) ([]byte, error) { <-ctx.Done(); return nil, ctx.Err() }
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := p.List(ctx)
	if !errors.Is(err, context.Canceled) || store.reads != 0 {
		t.Fatalf("outer cancellation lost: %v", err)
	}
}
