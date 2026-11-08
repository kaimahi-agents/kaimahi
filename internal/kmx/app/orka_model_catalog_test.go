package app

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

func TestOrkaModelLargeCatalog(t *testing.T) {
	// Upstream metadata can exceed the private checkpoint's 64 KiB limit.
	metadata := strings.Repeat("x", 70<<10)
	for _, tc := range []struct {
		name, body, want string
	}{
		{"advertised", `{"data":[{"id":"gpt-4.1","metadata":"` + metadata + `"}]}`, ""},
		{"missing", `{"data":[{"id":"other","metadata":"` + metadata + `"}]}`, "selected model is not advertised"},
		{"repeated", `{"data":[{"id":"gpt-4.1","metadata":"` + metadata + `"},{"id":"gpt-4.1"}]}`, "selected model is not advertised"},
		{"invalid identity", `{"data":[{"id":"gpt-4.1","metadata":"` + metadata + `"},{"id":"bad id"}]}`, "model catalog unavailable"},
		{"duplicate key", `{"data":[{"id":"gpt-4.1","metadata":"` + metadata + `","metadata":"other"}]}`, "model catalog unavailable"},
		{"trailing data", `{"data":[{"id":"gpt-4.1","metadata":"` + metadata + `"}]} {}`, "model catalog unavailable"},
		{"over response bound", `{"data":[{"id":"gpt-4.1","metadata":"` + strings.Repeat("x", 1<<20) + `"}]}`, "model catalog unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _, _ := modelGatewayFixture(t)
			capture := a.captureCommand
			a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
				if strings.HasSuffix(strings.Join(cmd.Args, " "), "/v1/models") {
					_, err := fmt.Fprint(cmd.Stdout, tc.body)
					return err
				}
				return capture(ctx, cmd)
			}
			err := a.modelHealth(context.Background(), OrkaModelConnection{Provider: "copilot", Model: "gpt-4.1"})
			if tc.want == "" {
				if err != nil {
					t.Fatalf("large advertised catalog refused: %v", err)
				}
			} else if err == nil || err.Error() != tc.want {
				t.Fatalf("got %v, want %s", err, tc.want)
			}
		})
	}
}
