package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

func TestDiscoveryTargetToolTransport(t *testing.T) {
	for _, tc := range []struct{ text, want string }{{"error: You must be logged in to the server (Unauthorized)", "authentication_required"}, {"Error from server (Unauthorized): private", "read_unknown"}, {"Error from server (Forbidden): private", "read_denied"}, {"Unable to connect to the server: private", "read_unknown"}, {"Error from server (Forbidden): private\nError from server (NotFound): private", "read_unknown"}, {strings.Repeat("x", 4097), "read_unknown"}} {
		a, _, _ := toolFixture(t)
		a.captureCommand = func(context.Context, *exec.Cmd) error { return nil }
		a.captureCommand = func(_ context.Context, c *exec.Cmd) error {
			fmt.Fprint(c.Stderr, tc.text)
			return errors.New("private")
		}
		_, reason := a.toolRead(t.Context(), "/api/v1/namespaces/orka-system")
		if reason != tc.want {
			t.Fatalf("got %s want %s", reason, tc.want)
		}
	}
}
func TestDiscoveryTargetSessionBudget(t *testing.T) {
	a := captureTestApp()
	s := &discoveryReadSession{context: "chosen"}
	calls := 0
	a.captureCommand = func(_ context.Context, c *exec.Cmd) error {
		calls++
		fmt.Fprint(c.Stdout, "abc")
		return errors.New("failed")
	}
	_, err := s.capture(t.Context(), a, "get", "--raw", "/api/v1/namespaces/x")
	if err == nil || s.bytes != 3 {
		t.Fatal(s.bytes, err)
	}
	s.bytes = 8 << 20
	_, err = s.capture(t.Context(), a, "get", "--raw", "/api/v1/namespaces/x")
	if err == nil || calls != 1 {
		t.Fatal(calls, err)
	}
	s = &discoveryReadSession{context: "chosen", pages: 32}
	_, err = s.capture(t.Context(), a, "get", "--raw", "/apis/core.orka.ai/v1alpha1/namespaces/x/providers?limit=64")
	if err == nil || calls != 1 {
		t.Fatal(calls, err)
	}
}
func TestOrkaToolsD95AndCanonicalKeys(t *testing.T) {
	a, p, _ := toolFixture(t)
	path := "/apis/core.orka.ai/v1alpha1/namespaces/orka-system/tools?limit=64"
	raw, _ := json.Marshal(p[path])
	p[path] = func(c *exec.Cmd) error { fmt.Fprint(c.Stdout, string(raw[:len(raw)-1])+`,"Items":[]}`); return nil }
	got, err := a.ReadOrkaTools(t.Context(), OrkaToolOptions{Namespace: "orka-system"})
	if err != nil || len(got.Tools) != 8 {
		t.Fatal(got, err)
	}
}
func TestOrkaToolsRuntimePagination(t *testing.T) {
	a, p, _ := toolFixture(t)
	path := "/apis/apps/v1/namespaces/orka-system/deployments?limit=64"
	last := p[path].(map[string]any)
	last["metadata"] = map[string]any{"resourceVersion": "1"}
	p[path+"&continue=next"] = last
	p[path] = map[string]any{"apiVersion": "apps/v1", "kind": "DeploymentList", "metadata": map[string]any{"resourceVersion": "1", "continue": "next"}, "items": []any{}}
	got, err := a.ReadOrkaTools(t.Context(), OrkaToolOptions{Namespace: "orka-system"})
	if err != nil || !got.Complete || len(got.Tools) != 8 {
		t.Fatal(got, err)
	}
}
func TestOrkaToolsStableListIdentity(t *testing.T) {
	for _, mode := range []string{"version", "uid"} {
		t.Run(mode, func(t *testing.T) {
			a, p, _ := toolFixture(t)
			path := "/apis/core.orka.ai/v1alpha1/namespaces/orka-system/tools?limit=64"
			first := toolPage(toolObject("one", "GET"))
			second := toolPage(toolObject("two", "GET"))
			first["metadata"] = map[string]any{"continue": "next", "resourceVersion": "1"}
			second["metadata"] = map[string]any{"resourceVersion": "2"}
			if mode == "uid" {
				second["metadata"] = map[string]any{"resourceVersion": "1"}
				second["items"].([]map[string]any)[0]["metadata"].(map[string]any)["uid"] = "uid-one"
			}
			p[path], p[path+"&continue=next"] = first, second
			got, e := a.ReadOrkaTools(t.Context(), OrkaToolOptions{Namespace: "orka-system"})
			if e == nil || got.Complete {
				t.Fatal(mode, got)
			}
		})
	}
}

func TestOrkaToolsStabilityReason(t *testing.T) {
	for _, mode := range []string{"replacement", "denied", "controller"} {
		t.Run(mode, func(t *testing.T) {
			a, p, _ := toolFixture(t)
			path := "/api/v1/namespaces/orka-system"
			original := p[path]
			n := 0
			if mode == "controller" {
				path = "/apis/apps/v1/namespaces/orka-system/deployments?limit=64"
				original = p[path]
			}
			p[path] = func(c *exec.Cmd) error {
				n++
				if n > 1 {
					if mode == "denied" {
						fmt.Fprint(c.Stderr, "Error from server (Forbidden): private")
						return errors.New("fail")
					}
					b, _ := json.Marshal(original)
					old, new := "namespace-uid", "replaced"
					if mode == "controller" {
						old, new = "controller-uid", "new-controller"
					}
					fmt.Fprint(c.Stdout, strings.ReplaceAll(string(b), old, new))
					return nil
				}
				return json.NewEncoder(c.Stdout).Encode(original)
			}
			got, err := a.ReadOrkaTools(t.Context(), OrkaToolOptions{Namespace: "orka-system"})
			if err == nil || got.Complete {
				t.Fatal(got, err)
			}
			want := "stale_input"
			if mode == "denied" {
				want = "read_denied"
			}
			for _, r := range got.Tools {
				if r.Reason != want {
					t.Fatalf("%s: %s", r.Name, r.Reason)
				}
			}
		})
	}
}
