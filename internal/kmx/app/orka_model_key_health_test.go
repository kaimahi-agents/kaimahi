package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestOrkaModelKeyErrorVocabulary(t *testing.T) {
	for _, code := range []string{"credential_rejected", "provider_unreachable", "model_unavailable"} {
		err := modelFailure(code, errors.New("private-upstream-canary"))
		var safe *OrkaModelError
		if !errors.As(err, &safe) || safe.Code() != code {
			t.Fatalf("lost code %s: %v", code, err)
		}
		raw, _ := json.Marshal(NewMachineError(safe.Code(), "model"))
		if !strings.Contains(string(raw), `"code":"`+code+`"`) || strings.Contains(string(raw), "private") {
			t.Fatalf("unsafe projection: %s", raw)
		}
	}
}

func TestOrkaModelKeyReadinessFailure(t *testing.T) {
	for _, tc := range []struct {
		name, message, want string
		status              int
	}{
		{"rejected", `provider "openai" upstream probe returned 401: private-upstream-canary`, "credential_rejected", 503},
		{"forbidden", `provider "openai" upstream probe returned 403`, "credential_rejected", 503},
		{"escaped body", `provider "openai" upstream probe returned 401: ` + strings.Repeat("\x01", 4096), "credential_rejected", 503},
		{"network", `provider "openai" upstream probe failed: private-upstream-canary`, "provider_unreachable", 503},
		{"other provider", `provider "anthropic" upstream probe returned 401`, "dependency_unavailable", 503},
		{"spoofed body", `provider "openai" upstream probe returned 500: provider "openai" upstream probe returned 401`, "dependency_unavailable", 503},
		{"invalid status", `provider "openai" upstream probe returned 4010`, "dependency_unavailable", 503},
		{"pending", "provider model validation pending", "dependency_unavailable", 503},
		{"quota", `provider "openai" upstream probe returned 429`, "dependency_unavailable", 503},
		{"changed after failure", "", "dependency_unavailable", 200},
		{"oversized", strings.Repeat("x", 32<<10), "dependency_unavailable", 503},
		{"malformed", "", "dependency_unavailable", 503},
		{"redirect", "", "dependency_unavailable", 302},
		{"cancelled", "", "cancelled", 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/readyz" {
					t.Error("unexpected request", r.URL)
				}
				if tc.name == "cancelled" {
					cancel()
					return
				}
				if tc.name == "redirect" {
					w.Header().Set("Location", "/must-not-follow")
				}
				w.WriteHeader(tc.status)
				if tc.name == "malformed" {
					fmt.Fprint(w, "not-json")
					return
				}
				status := "not_ready"
				if tc.status == 200 {
					status = "ready"
				}
				json.NewEncoder(w).Encode(map[string]any{"status": status, "error": tc.message})
			}))
			defer server.Close()
			a, _, calls := modelLifecycleFixture(t)
			a.Run.Context = ctx
			u, _ := url.Parse(server.URL)
			bin := strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0]
			tool := filepath.Join(bin, "kubectl")
			original, err := os.ReadFile(tool)
			if err != nil {
				t.Fatal(err)
			}
			script := "#!/bin/sh\ncase \"$*\" in *port-forward*pod/copilot-vekil-pod*) printf 'Forwarding from 127.0.0.1:" + u.Port() + " -> 1337\\n'; exec /bin/sleep 60;; esac\n" + string(original)
			if err := os.WriteFile(tool, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			capture := a.captureCommand
			a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
				if strings.HasSuffix(strings.Join(cmd.Args, " "), "/readyz") {
					return errors.New("kubectl discards HTTP503 body")
				}
				return capture(ctx, cmd)
			}
			opt := modelConnectOptions()
			opt.Route = "key"
			opt.Vendor = "openai"
			result, err := a.ConnectOrkaModel(opt)
			var safe *OrkaModelError
			if !errors.As(err, &safe) || safe.Code() != tc.want || result.Connection == nil || result.Connection.State != "unknown" {
				t.Fatalf("got %+v %v, want %s", result, err, tc.want)
			}
			if strings.Contains(err.Error(), "private") {
				t.Fatal("leaked upstream reply")
			}
			for _, call := range *calls {
				if strings.Contains(call, "/v1/models") {
					t.Fatal("catalog after failed readiness")
				}
			}
		})
	}
}

func TestOrkaModelKeyCatalog(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{`{"data":[{"id":"claude-sonnet-5-5"}]}`, ""},
		{`{"data":[{"id":"other"}]}`, "model_unavailable"},
		{`{"data":[]}`, "model_unavailable"},
		{`{"data":[{"id":"claude-sonnet-5-5"},{"id":"claude-sonnet-5-5"}]}`, "model_unavailable"},
	} {
		a, _, _ := modelLifecycleFixture(t)
		capture := a.captureCommand
		seen := false
		a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
			if strings.Contains(strings.Join(cmd.Args, " "), "/v1/models") {
				if !strings.HasSuffix(strings.Join(cmd.Args, " "), "/v1/models?limit=1000") {
					t.Fatal("missing page size")
				}
				seen = true
				fmt.Fprint(cmd.Stdout, tc.body)
				return nil
			}
			return capture(ctx, cmd)
		}
		opt := modelConnectOptions()
		opt.Route = "key"
		opt.Vendor = "anthropic"
		opt.Model = "claude-sonnet-5-5"
		_, err := a.ConnectOrkaModel(opt)
		if !seen {
			t.Fatal("catalog not read")
		}
		if tc.want == "" {
			if err != nil {
				t.Fatal(err)
			}
		} else {
			var safe *OrkaModelError
			if !errors.As(err, &safe) || safe.Code() != tc.want {
				t.Fatalf("got %v, want %s", err, tc.want)
			}
		}
	}
}
