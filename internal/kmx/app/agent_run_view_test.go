package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	runorka "github.com/kaimahi-agents/kaimahi/internal/kmx/runview/orka"
)

func TestOrkaRunHTTPReadDistinguishesDenialAndMalformedContentFromDisconnect(t *testing.T) {
	for _, tc := range []struct {
		status            int
		contentType, body string
		wantErr           bool
	}{
		{http.StatusForbidden, "text/plain", "denied", false},
		{http.StatusNotFound, "text/plain", "missing", false},
		{http.StatusOK, "application/json", "not-json", true},
	} {
		for _, resource := range []string{"events", "trace"} {
			t.Run(fmt.Sprint(tc.status)+"/"+resource, func(t *testing.T) {
				a, opt, _, _, _ := orkaCreateFixture(t, "")
				orkaResultServer(t, &opt, func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", tc.contentType)
					w.WriteHeader(tc.status)
					fmt.Fprint(w, tc.body)
				})
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				session, err := a.openOrkaResultSession(ctx, opt)
				if err != nil {
					t.Fatal(err)
				}
				defer session.close()
				source := orkaRunSource{session: session}
				var status int
				if resource == "events" {
					_, status, err = source.Events(ctx, opt.Namespace, "task", 0, 100)
				} else {
					status, err = source.Trace(ctx, opt.Namespace, "task")
				}
				if status != tc.status || (err != nil) != tc.wantErr || (tc.wantErr && !errors.Is(err, runorka.ErrInvalidResponse)) {
					t.Fatalf("%s status=%d err=%v", resource, status, err)
				}
			})
		}
	}
}

func TestOrkaRunHelpersReadCurrentPolicyNotSpecText(t *testing.T) {
	raw := []byte(`{"spec":{"coordination":{"allowedAgents":[{"name":"receiving"},{"name":"purchasing"}]},"systemPrompt":"PRIVATE PROMPT"}}`)
	names, err := orkaRunHelpers(raw)
	if err != nil || len(names) != 2 || names[0] != "receiving" || names[1] != "purchasing" {
		t.Fatalf("helpers=%v err=%v", names, err)
	}
}
