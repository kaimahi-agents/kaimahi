package copilot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
)

func TestOrdinaryListRetainsItsExpiryPolicy(t *testing.T) {
	now := time.Now()
	var elapsed atomic.Int64
	p, s := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("unexpected refresh %s", r.URL.Path)
		}
		elapsed.Store(int64(2 * time.Second))
		io.WriteString(w, catalog)
	})
	s.token = canary
	p.now = func() time.Time { return now.Add(time.Duration(elapsed.Load())) }
	p.source, p.bearer, p.legacy, p.expires = canary, previewBearer, true, now.Add(31*time.Second)
	// A catalog completing inside the ordinary 30-second refresh margin has
	// always remained usable. Only preview setup requires a full-run lifetime.
	if routes, err := p.List(t.Context()); err != nil || !slices.Contains(routes, selected()) {
		t.Fatalf("ordinary listing policy changed: %v / %v", routes, err)
	}
}

func TestPreviewCapabilityFormattingDoesNotExposeCredentialFields(t *testing.T) {
	p, s := fixture(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, catalog) })
	s.token = canary
	a := previewAccess(t, p)
	requirePrivate(t, fmt.Sprintf("%v %+v %#v", a, a, a))
}

func TestPreviewDistinctRoutesShareSnapshotAndStopState(t *testing.T) {
	catalogs, chats := 0, 0
	both := strings.Replace(catalog, `{"data":[`, `{"data":[{"id":"judge-test","capabilities":{"type":"chat"},"policy":{"state":"enabled"},"supported_endpoints":["/chat/completions"]},`, 1)
	p, s := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			catalogs++
			io.WriteString(w, both)
		case "/chat/completions":
			chats++
			if chats == 2 {
				w.WriteHeader(429)
				return
			}
			io.WriteString(w, `{"choices":[]}`)
		}
	})
	s.token = canary
	judge := selected()
	judge.Model = "judge-test"
	a := previewAccess(t, p, selected(), judge)
	if catalogs != 1 || s.reads != 1 {
		t.Fatalf("setup not atomic catalogs=%d reads=%d", catalogs, s.reads)
	}
	agentRT, err := a.Client(selected())
	if err != nil {
		t.Fatal(err)
	}
	judgeRT, err := a.Client(judge)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := agentRT.RoundTrip(chatRequest(t, previewBody))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	_, err = judgeRT.RoundTrip(chatRequest(t, strings.ReplaceAll(previewBody, "gpt-test", "judge-test")))
	if !errors.Is(err, ErrQuota) {
		t.Fatalf("judge refusal=%v", err)
	}
	before := catalogs
	_, err = agentRT.RoundTrip(chatRequest(t, previewBody))
	if !errors.Is(err, ErrQuota) || chats != 2 || catalogs != before || s.reads != 1 {
		t.Fatal("judge refusal did not stop retained agent transport")
	}
}

func TestPreviewSetupCatalogRefusalKeepsExchangedBearerChecker(t *testing.T) {
	exchanges := 0
	p, s := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/exchange" {
			exchanges++
			fmt.Fprintf(w, `{"token":%q,"expires_at":%d}`, previewBearer, time.Now().Add(10*time.Minute).Unix())
			return
		}
		if r.Header.Get("Authorization") == "Bearer "+canary {
			w.WriteHeader(401)
		} else {
			w.WriteHeader(403)
		}
	})
	s.token = canary
	a, err := p.PreviewClient(t.Context(), []modelaccess.Route{selected()})
	if !errors.Is(err, ErrPolicy) || a == nil || exchanges != 1 {
		t.Fatalf("setup=%v checker=%t exchanges=%d", err, a != nil, exchanges)
	}
	if !errors.Is(a.CheckContent(canary), ErrCredentialContent) || !errors.Is(a.CheckContent(previewBearer), ErrCredentialContent) {
		t.Fatal("setup refusal lost credentials")
	}
}

func TestPreviewCancellationDuringInferenceStopsLaterSends(t *testing.T) {
	started := make(chan struct{})
	var chats atomic.Int32
	p, s := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			io.WriteString(w, catalog)
			return
		}
		io.Copy(io.Discard, r.Body)
		chats.Add(1)
		close(started)
		<-r.Context().Done()
	})
	s.token = canary
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	a, err := p.PreviewClient(ctx, []modelaccess.Route{selected()})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	rt, err := a.Client(selected())
	if err != nil {
		t.Fatal(err)
	}
	req := chatRequest(t, previewBody)
	go func() { _, err := rt.RoundTrip(req); done <- err }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("inference did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel=%v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("inference ignored invocation cancellation")
	}
	if _, err := previewSend(t, a, previewBody); !errors.Is(err, context.Canceled) || chats.Load() != 1 {
		t.Fatal("cancelled invocation sent again")
	}
}

func TestPreviewExpiryRecheckedAfterCatalogBeforeInference(t *testing.T) {
	var seconds atomic.Int64
	var catalogs, chats atomic.Int32
	base := time.Now()
	p, s := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			if catalogs.Add(1) == 2 {
				seconds.Store(6 * 60)
			}
			io.WriteString(w, catalog)
			return
		}
		chats.Add(1)
		io.WriteString(w, `{"choices":[]}`)
	})
	s.token = canary
	p.now = func() time.Time { return base.Add(time.Duration(seconds.Load()) * time.Second) }
	a := previewAccess(t, p)
	if _, err := previewSend(t, a, previewBody); !errors.Is(err, ErrAuthRevoked) || chats.Load() != 0 {
		t.Fatal("expired snapshot sent after catalog")
	}
}

func TestPreviewCheckContentShortPlainCredentials(t *testing.T) {
	p, s := fixture(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, catalog) })
	s.token = "Q"
	a := previewAccess(t, p)
	if a.CheckContent(`literal \\u0051`) != nil {
		t.Fatal("plain string was incorrectly JSON-decoded")
	}
	if !errors.Is(a.CheckContent("Q"), ErrCredentialContent) {
		t.Fatal("one-character credential escaped checking")
	}
}
