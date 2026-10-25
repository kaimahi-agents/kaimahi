package mcp

import (
	"context"
	"testing"
)

func TestHostAvailabilityRequiresExactMissingAccounting(t *testing.T) {
	for _, tc := range []struct {
		name    string
		defs    bool
		missing any
		want    bool
	}{
		{"partial", true, []string{"absent"}, true}, {"all missing", false, []string{"lookup", "absent"}, true},
		{"omitted", true, nil, false}, {"incomplete", true, []string{}, false}, {"overlap", true, []string{"lookup", "absent"}, false},
		{"duplicate", false, []string{"lookup", "lookup"}, false}, {"unexpected", true, []string{"other"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newBridgePeer(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan bridgeResolved, 1)
			go func() {
				h, err := p.bridge.ResolveAvailableTools(ctx, []string{"lookup", "absent"})
				done <- bridgeResolved{h, err}
			}()
			job := p.next()
			if job["allowMissing"] != true {
				t.Fatal("missing availability request")
			}
			reply := map[string]any{"missing": tc.missing, "definitions": []any{}}
			if tc.defs {
				reply["definitions"] = bridgeDefinitions()
			}
			if p.result(job, reply) != 200 {
				t.Fatal("response refused")
			}
			got := awaitResolved(t, done)
			if (got.err == nil) != tc.want {
				t.Fatalf("host=%v err=%v", got.host, got.err)
			}
			if tc.want && tc.defs && (got.host == nil || !got.host.Matches([]string{"lookup"})) {
				t.Fatal("partial host lost")
			}
			if tc.want && !tc.defs && got.host != nil {
				t.Fatal("all missing constructed host")
			}
		})
	}
}
