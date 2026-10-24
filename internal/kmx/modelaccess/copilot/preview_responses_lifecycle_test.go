package copilot

import (
	"context"
	"errors"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestPreviewResponsesLifecycle(t *testing.T) {
	for _, phase := range []string{"cancel", "logout", "expiry", "expiry-after-catalog", "auth", "network", "rotation"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			now := time.Now()
			var elapsed atomic.Int64
			var catalogs, attempts atomic.Int32
			var p *Provider
			var store *memoryStore
			p, store = fixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer "+canary {
					t.Error("snapshot credential changed")
				}
				if r.URL.Path == "/models" {
					if catalogs.Add(1) > 1 && phase == "expiry-after-catalog" {
						elapsed.Store(int64(6 * time.Minute))
					}
					io.WriteString(w, previewProtocolCatalog("/responses"))
					return
				}
				if r.URL.Path != "/responses" {
					t.Error("protocol changed")
				}
				switch phase {
				case "cancel":
					cancel()
				case "logout":
					if err := p.Logout(); err != nil {
						t.Error(err)
					}
				case "expiry":
					elapsed.Store(int64(6 * time.Minute))
				case "auth":
					w.WriteHeader(401)
					io.WriteString(w, `{"error":{"message":"`+canary+`"}}`)
					return
				case "rotation":
					store.token = "rotated-unused-token"
				}
				io.WriteString(w, previewResponsesText("hello"))
			})
			store.token = canary
			p.now = func() time.Time { return now.Add(time.Duration(elapsed.Load())) }
			upstream := p.http.Transport
			p.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == "/responses" {
					attempts.Add(1)
					if phase == "network" {
						return nil, errors.New(canary)
					}
				}
				return upstream.RoundTrip(r)
			})
			a, err := p.PreviewClient(ctx, []modelaccess.Route{selected()})
			if err != nil {
				t.Fatal(err)
			}
			rt, err := a.Client(selected())
			if err != nil {
				t.Fatal(err)
			}
			resp, err := rt.RoundTrip(chatRequest(t, previewBody))
			if phase == "rotation" {
				if err != nil {
					t.Fatal(err)
				}
				resp.Body.Close()
				resp, err = rt.RoundTrip(chatRequest(t, previewBody))
				if err != nil {
					t.Fatal(err)
				}
				resp.Body.Close()
				if attempts.Load() != 2 || store.reads != 1 {
					t.Fatal("rotation re-resolved credentials")
				}
				return
			}
			want := ErrAuthRevoked
			if phase == "cancel" {
				want = context.Canceled
			}
			if phase == "network" {
				want = ErrUnavailable
			}
			if resp != nil || !errors.Is(err, want) {
				t.Fatalf("lifecycle refusal: %v", err)
			}
			requirePrivate(t, err)
			before := catalogs.Load()
			_, later := rt.RoundTrip(chatRequest(t, previewBody))
			wantAttempts := int32(1)
			if phase == "expiry-after-catalog" {
				wantAttempts = 0
			}
			if !errors.Is(later, want) || catalogs.Load() != before || attempts.Load() != wantAttempts || store.reads != 1 {
				t.Fatalf("lifecycle resumed: %v", later)
			}
		})
	}
}
