package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func toolFixture(t *testing.T) (*App, map[string]any, *int) {
	t.Helper()
	a, _, f := nativeFixture(t)
	deployments := f.objects["deploy/-o"]
	deployments["apiVersion"], deployments["kind"] = "apps/v1", "DeploymentList"
	delete(deployments["items"].([]any)[0].(map[string]any), "kind") // raw API lists omit item GVK
	for _, name := range []string{"kube-system", "orka-system"} {
		f.objects["namespace/"+name]["apiVersion"] = "v1"
	}
	pages := map[string]any{
		"/api/v1/namespaces/kube-system":                                    f.objects["namespace/kube-system"],
		"/api/v1/namespaces/orka-system":                                    f.objects["namespace/orka-system"],
		"/apis/apps/v1/namespaces/orka-system/deployments?limit=64":         deployments,
		"/apis/core.orka.ai/v1alpha1":                                       map[string]any{"kind": "APIResourceList", "apiVersion": "v1", "groupVersion": "core.orka.ai/v1alpha1", "resources": []any{map[string]any{"name": "tools", "kind": "Tool", "namespaced": true, "verbs": []string{"get", "list"}}}},
		"/apis/core.orka.ai/v1alpha1/namespaces/orka-system/tools?limit=64": toolPage(toolObject("inventory", "POST")),
	}
	calls := 0
	a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
		calls++
		if !slices.Equal(cmd.Args[1:3], []string{"--context", "kind-test"}) || !slices.Contains(cmd.Args, "--request-timeout=10s") {
			t.Fatalf("unbounded/unpinned: %v", cmd.Args)
		}
		i := slices.Index(cmd.Args, "get")
		if i < 0 || len(cmd.Args) != i+3 || cmd.Args[i+1] != "--raw" {
			t.Fatalf("non-GET capture: %v", cmd.Args)
		}
		if !slices.Contains(cmd.Env, "KUBERC=off") || !slices.Contains(cmd.Env, "KUBECTL_KUBERC=false") {
			t.Fatal("preferences enabled")
		}
		value, ok := pages[cmd.Args[i+2]]
		if !ok {
			fmt.Fprint(cmd.Stderr, `Error from server (NotFound): tools.core.orka.ai "absent" not found`)
			return fmt.Errorf("exit1")
		}
		if fn, ok := value.(func(*exec.Cmd) error); ok {
			return fn(cmd)
		}
		return json.NewEncoder(cmd.Stdout).Encode(value)
	}
	return a, pages, &calls
}
func toolObject(name, method string) map[string]any {
	http := map[string]any{"url": "https://private-content-canary.invalid", "headers": map[string]any{"X-Private": "private-content-canary"}}
	if method != "" {
		http["method"] = method
	}
	return map[string]any{"metadata": map[string]any{"name": name, "namespace": "orka-system", "uid": "uid-" + name, "resourceVersion": "1", "generation": 1}, "spec": map[string]any{"http": http}, "status": map[string]any{"available": true, "error": "private-content-canary", "endpoint": "private-content-canary"}}
}
func toolPage(items ...map[string]any) map[string]any {
	if items == nil {
		items = []map[string]any{}
	}
	return map[string]any{"apiVersion": "core.orka.ai/v1alpha1", "kind": "ToolList", "metadata": map[string]any{}, "items": items}
}

func TestOrkaToolsInventory(t *testing.T) {
	a, _, calls := toolFixture(t)
	t.Setenv("KUBERC", "inherited-private")
	a.Run.Env = append(a.Run.Env, "KUBERC=caller-private")
	before := append([]string(nil), a.Run.Env...)
	got, err := a.ReadOrkaTools(t.Context(), OrkaToolOptions{Namespace: "orka-system"})
	if err != nil || !got.Complete || len(got.Tools) != 8 {
		t.Fatalf("inventory=%+v err=%v", got, err)
	}
	want := []string{"web_search", "code_exec", "file_read", "web_fetch", "file_write", "request_approval", "reply_in_conversation"}
	for i, name := range want {
		if got.Tools[i].Name != name || got.Tools[i].Source != "pinned-catalog" || got.Tools[i].Availability != "available" {
			t.Fatal(got.Tools)
		}
	}
	if got.RuntimeVersion != OrkaVersion || got.CatalogCommit != "a533c3491d350263c0fa39bb7887dabd0d055c4e" || got.Target.ClusterUID != "cluster-uid" {
		t.Fatal(got)
	}
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), "private-content-canary") || !reflect.DeepEqual(before, a.Run.Env) || *calls < 6 {
		t.Fatalf("unsafe or shared mutation: %s", raw)
	}
}
func TestOrkaToolsMethodsAndBackends(t *testing.T) {
	for _, method := range []string{"", "GET", "POST", "PUT", "PATCH", "DELETE", "BAD", "MCP", "false", "absent"} {
		t.Run(method, func(t *testing.T) {
			a, p, _ := toolFixture(t)
			obj := toolObject("inventory", method)
			if method == "MCP" {
				obj["spec"].(map[string]any)["mcp"] = map[string]any{}
			}
			if method == "false" || method == "absent" {
				obj["spec"] = map[string]any{"http": map[string]any{"url": "https://example.invalid"}}
				obj["status"] = map[string]any{}
				if method == "false" {
					obj["status"].(map[string]any)["available"] = false
				}
			}
			p["/apis/core.orka.ai/v1alpha1/namespaces/orka-system/tools?limit=64"] = toolPage(obj)
			got, err := a.ReadOrkaTools(t.Context(), OrkaToolOptions{Namespace: "orka-system"})
			if err != nil {
				t.Fatal(err)
			}
			state := "available"
			if method == "MCP" || method == "BAD" {
				state = "unsupported"
			}
			if method == "false" || method == "absent" {
				state = "unknown"
			}
			if got.Tools[7].Availability != state {
				t.Fatal(got.Tools[7])
			}
		})
	}
}
func TestOrkaToolsRequirements(t *testing.T) {
	a, p, _ := toolFixture(t)
	req := []OrkaToolRequirement{{Kind: "http", Name: "absent", Enabled: true}, {Kind: "builtin", Name: "web_fetch", Enabled: true}, {Kind: "http", Name: "disabled", Enabled: false}}
	got, err := a.ReadOrkaTools(t.Context(), OrkaToolOptions{Namespace: "orka-system", Requirements: req})
	if err != nil || !got.Complete {
		t.Fatalf("%+v %v", got, err)
	}
	if len(got.Tools) != 3 || got.Tools[0].Availability != "missing" || got.Tools[1].Name != "web_fetch" || got.Tools[2].Availability != "disabled" {
		t.Fatal(got.Tools)
	}
	// A forbidden exact read is not absence, and arbitrary echoed 'not found' is not a reason.
	p["/apis/core.orka.ai/v1alpha1/namespaces/orka-system/tools/absent"] = func(cmd *exec.Cmd) error {
		fmt.Fprint(cmd.Stderr, "private not found Forbidden")
		return fmt.Errorf("fail")
	}
	got, err = a.ReadOrkaTools(t.Context(), OrkaToolOptions{Namespace: "orka-system", Requirements: req})
	if err == nil || got.Tools[0].Availability != "unknown" {
		t.Fatalf("%+v %v", got, err)
	}
}
func TestOrkaToolsInvalidBeforeRead(t *testing.T) {
	for _, opt := range []OrkaToolOptions{{Namespace: "../bad"}, {Namespace: "orka-system", Requirements: []OrkaToolRequirement{{Kind: "http", Name: "bad?path", Enabled: true}}}, {Namespace: "orka-system", Requirements: []OrkaToolRequirement{{Kind: "builtin", Name: "bad/name", Enabled: true}}}} {
		a, _, calls := toolFixture(t)
		_, err := a.ReadOrkaTools(t.Context(), opt)
		if err == nil || *calls != 0 {
			t.Fatalf("invalid read %v %d", err, *calls)
		}
	}
}
func TestOrkaToolsIncomplete(t *testing.T) {
	for _, mode := range []string{"runtime", "watch", "namespace", "api", "denied", "repeat", "duplicate", "overflow", "stderr", "malformed", "cancel", "changed"} {
		t.Run(mode, func(t *testing.T) {
			a, p, _ := toolFixture(t)
			list := "/apis/core.orka.ai/v1alpha1/namespaces/orka-system/tools?limit=64"
			switch mode {
			case "runtime", "watch":
				d := p["/apis/apps/v1/namespaces/orka-system/deployments?limit=64"].(map[string]any)["items"].([]any)[0].(map[string]any)
				c := d["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)
				if mode == "runtime" {
					c["image"] = "ghcr.io/orka-agents/orka:other"
				} else {
					args := c["args"].([]any)
					for i, v := range args {
						if v == "--watch-namespace=orka-system" {
							args[i] = "--watch-namespace=other"
						}
					}
				}
			case "namespace":
				delete(p, "/api/v1/namespaces/orka-system")
			case "api":
				delete(p, "/apis/core.orka.ai/v1alpha1")
			case "denied":
				p["/api/v1/namespaces/kube-system"] = func(cmd *exec.Cmd) error {
					fmt.Fprint(cmd.Stderr, "Error from server (Forbidden): private-content-canary")
					return fmt.Errorf("fail")
				}
			case "repeat":
				page := toolPage()
				page["metadata"].(map[string]any)["continue"] = "same"
				p[list] = page
				p[list+"&continue=same"] = page
			case "duplicate":
				p[list] = toolPage(toolObject("same", "GET"), toolObject("same", "GET"))
			case "overflow":
				p[list] = func(cmd *exec.Cmd) error {
					_, err := fmt.Fprint(cmd.Stdout, strings.Repeat("x", (4<<20)+1))
					return err
				}
			case "stderr":
				p[list] = func(cmd *exec.Cmd) error {
					fmt.Fprint(cmd.Stderr, strings.Repeat("x", 5000))
					return fmt.Errorf("fail")
				}
			case "malformed":
				p[list] = func(cmd *exec.Cmd) error { fmt.Fprint(cmd.Stdout, `{"kind":"ToolList","kind":"ToolList"}`); return nil }
			case "changed":
				n := 0
				p["/api/v1/namespaces/orka-system"] = func(cmd *exec.Cmd) error {
					n++
					uid := "namespace-uid"
					if n > 1 {
						uid = "changed"
					}
					fmt.Fprintf(cmd.Stdout, `{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"orka-system","uid":%q}}`, uid)
					return nil
				}
			}
			ctx := t.Context()
			if mode == "cancel" {
				c, cancel := context.WithCancel(ctx)
				cancel()
				ctx = c
			}
			got, err := a.ReadOrkaTools(ctx, OrkaToolOptions{Namespace: "orka-system"})
			if err == nil || got.Complete {
				t.Fatalf("accepted %s %+v %v", mode, got, err)
			}
			for _, row := range got.Tools {
				if row.Availability == "available" && (mode == "runtime" || mode == "watch" || mode == "changed") {
					t.Fatal("unqualified availability", row)
				}
			}
			raw, _ := json.Marshal(got)
			if strings.Contains(string(raw), "private-content-canary") {
				t.Fatal(string(raw))
			}
		})
	}
}
