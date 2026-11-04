package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

func discoveryPage(names ...string) string {
	items := []any{}
	for _, n := range names {
		items = append(items, map[string]any{"metadata": map[string]any{"name": n, "uid": "uid-" + n}})
	}
	b, _ := json.Marshal(map[string]any{"apiVersion": "v1", "kind": "NamespaceList", "metadata": map[string]any{"resourceVersion": "1"}, "items": items})
	return string(b)
}
func TestDiscoveryContextsProjectedConfigViewOnly(t *testing.T) {
	a := captureTestApp()
	calls := 0
	a.captureCommand = func(_ context.Context, c *exec.Cmd) error {
		calls++
		if !reflect.DeepEqual(c.Args[1:], []string{"--request-timeout=10s", "config", "view", "-o", "go-template=" + discoveryContextTemplate}) {
			t.Fatal(c.Args)
		}
		fmt.Fprint(c.Stdout, "\"arn:aws:eks:cluster/dev\"\n\"z\"\n\"arn:aws:eks:cluster/dev\"\n\"日本語\"\n")
		return nil
	}
	got, err := a.Discover(t.Context(), DiscoveryOptions{})
	if err != nil || !got.Complete || got.Scope != "contexts" || len(got.Rows) != 3 || calls != 1 {
		t.Fatalf("%+v %v calls%d", got, err, calls)
	}
	if got.Rows[0].Context != "arn:aws:eks:cluster/dev" || got.Rows[0].Current == nil || !*got.Rows[0].Current || got.Rows[1].Current == nil || *got.Rows[1].Current {
		t.Fatal(got.Rows)
	}
	a.captureCommand = func(_ context.Context, c *exec.Cmd) error { fmt.Fprint(c.Stdout, "\"\"\n"); return nil }
	got, err = a.Discover(t.Context(), DiscoveryOptions{})
	if err != nil || !got.Complete || got.Rows == nil || len(got.Rows) != 0 || got.Checks == nil {
		t.Fatal(got, err)
	}
}
func TestDiscoveryNamespacesPinsContextAndPreservesPartialRows(t *testing.T) {
	a := captureTestApp()
	calls := 0
	a.captureCommand = func(_ context.Context, c *exec.Cmd) error {
		calls++
		if c.Args[1] != "--context" || c.Args[2] != "chosen" || c.Args[4] != "get" || c.Args[5] != "--raw" {
			t.Fatal(c.Args)
		}
		u, err := url.Parse(c.Args[6])
		if err != nil || u.Path != "/api/v1/namespaces" || u.Query().Get("limit") != "128" {
			t.Fatal(c.Args)
		}
		if calls == 1 {
			page := strings.Replace(discoveryPage("dev"), `"resourceVersion":"1"`, `"resourceVersion":"1","continue":"a&b"`, 1)
			fmt.Fprint(c.Stdout, page)
			return nil
		}
		if u.Query().Get("continue") != "a&b" {
			t.Fatal(u)
		}
		fmt.Fprint(c.Stderr, "Error from server (Forbidden): sensitive")
		return errors.New("hidden cause")
	}
	got, err := a.Discover(t.Context(), DiscoveryOptions{Context: "chosen"})
	if err == nil || got.Complete || len(got.Rows) != 1 || got.Rows[0].Context != "chosen" || got.Rows[0].UID != "uid-dev" || got.Checks[0].Reason != "read_denied" || calls != 2 {
		t.Fatal(got, err, calls)
	}
}
func TestDiscoveryFailedListNeverProvesMissing(t *testing.T) {
	a := captureTestApp()
	a.captureCommand = func(_ context.Context, c *exec.Cmd) error {
		fmt.Fprint(c.Stderr, "Error from server (NotFound): namespaces not found")
		return errors.New("exit")
	}
	got, err := a.Discover(t.Context(), DiscoveryOptions{Context: "chosen"})
	if err == nil || got.Complete || got.Checks[0].State != "unknown" || got.Checks[0].Reason != "read_unknown" || DiscoveryError(err).Error() == "" {
		t.Fatal(got, err)
	}
}
func TestDiscoveryPaginationAndOutputBounds(t *testing.T) {
	for _, kind := range []string{"repeat", "version", "rows", "output", "malformed", "pages", "aggregate", "duplicate"} {
		t.Run(kind, func(t *testing.T) {
			a := captureTestApp()
			calls := 0
			a.captureCommand = func(_ context.Context, c *exec.Cmd) error {
				calls++
				page := discoveryPage("dev")
				switch kind {
				case "repeat":
					page = strings.Replace(discoveryPage(fmt.Sprintf("ns-%d", calls)), `"resourceVersion":"1"`, `"resourceVersion":"1","continue":"same"`, 1)
				case "version":
					page = strings.Replace(page, `"resourceVersion":"1"`, `"resourceVersion":"`+fmt.Sprint(calls)+`","continue":"`+fmt.Sprint(calls)+`"`, 1)
				case "rows":
					names := []string{}
					for i := 0; i < 129; i++ {
						names = append(names, fmt.Sprintf("ns-%03d", i))
					}
					page = discoveryPage(names...)
				case "output":
					names := []string{}
					for i := 0; i < 100; i++ {
						names = append(names, fmt.Sprintf("ns-%03d", i))
					}
					page = discoveryPage(names...)
				case "pages", "aggregate":
					page = strings.Replace(discoveryPage(fmt.Sprintf("ns-%d", calls)), `"resourceVersion":"1"`, `"resourceVersion":"1","continue":"`+fmt.Sprint(calls)+`"`, 1)
					if kind == "aggregate" {
						page = `{"padding":"` + strings.Repeat("x", 3<<20) + `",` + page[1:]
					}
				case "duplicate":
					page = discoveryPage("dev", "dev")
				case "malformed":
					page = `{"apiVersion":"v1","kind":"NamespaceList","items":null}`
				}
				fmt.Fprint(c.Stdout, page)
				return nil
			}
			selected := "chosen"
			if kind == "output" {
				selected = strings.Repeat("x", 1024)
			}
			got, err := a.Discover(t.Context(), DiscoveryOptions{Context: selected})
			if err == nil || got.Complete || calls > 32 {
				t.Fatal(got, err, calls)
			}
			b, _ := json.Marshal(got)
			if len(b) > 60<<10 || len(got.Rows) > 128 {
				t.Fatal(len(b), len(got.Rows))
			}
		})
	}
}
func TestDiscoveryUnsafeNamesAndDocuments(t *testing.T) {
	a := captureTestApp()
	a.captureCommand = func(_ context.Context, c *exec.Cmd) error {
		fmt.Fprint(c.Stdout, "\"\"\n\"safe\"\n\"bad\\x01name\"\nmalformed\n")
		return nil
	}
	got, err := a.Discover(t.Context(), DiscoveryOptions{})
	if err == nil || got.Complete || len(got.Rows) != 1 || got.Rows[0].Context != "safe" {
		t.Fatal(got, err)
	}
	b, _ := json.Marshal(got)
	if strings.Contains(string(b), "bad") || strings.Contains(string(b), "malformed\\n") {
		t.Fatal(string(b))
	}
}
func TestDiscoveryTargetScopeUnavailable(t *testing.T) {
	a := captureTestApp()
	a.captureCommand = func(context.Context, *exec.Cmd) error { t.Fatal("unsupported dispatched"); return nil }
	_, err := a.Discover(t.Context(), DiscoveryOptions{Namespace: "dev"})
	if err == nil || DiscoveryError(err).Error() != "The requested operation is unsupported." {
		t.Fatal(err)
	}
}
func TestDiscoveryNamespaceSecretShapeIsSuppressed(t *testing.T) {
	name := strings.Repeat("a1b2", 8)
	if statusSafeText(name) {
		t.Fatal("fixture must be secret-shaped")
	}
	a := captureTestApp()
	a.captureCommand = func(_ context.Context, c *exec.Cmd) error { fmt.Fprint(c.Stdout, discoveryPage(name)); return nil }
	got, err := a.Discover(t.Context(), DiscoveryOptions{Context: "chosen"})
	if err == nil || got.Complete || len(got.Rows) != 0 {
		t.Fatal("unsafe namespace retained")
	}
}

func TestDiscoveryNamespaceTerminationIsNotReadiness(t *testing.T) {
	a := captureTestApp()
	a.captureCommand = func(_ context.Context, c *exec.Cmd) error {
		fmt.Fprint(c.Stdout, strings.Replace(discoveryPage("dev"), `"uid":"uid-dev"`, `"uid":"uid-dev","deletionTimestamp":"2026-01-01T00:00:00Z"`, 1))
		return nil
	}
	got, err := a.Discover(t.Context(), DiscoveryOptions{Context: "chosen"})
	if err != nil || !got.Complete || len(got.Rows) != 1 {
		t.Fatal(got, err)
	}
}
func TestDiscoveryUnterminatedContextProjection(t *testing.T) {
	a := captureTestApp()
	a.captureCommand = func(_ context.Context, c *exec.Cmd) error { fmt.Fprint(c.Stdout, `""`); return nil }
	got, err := a.Discover(t.Context(), DiscoveryOptions{})
	if err == nil || got.Complete {
		t.Fatal(got, err)
	}
}
func TestDiscoveryContextReadFailureReason(t *testing.T) {
	a := captureTestApp()
	a.captureCommand = func(_ context.Context, c *exec.Cmd) error {
		fmt.Fprint(c.Stderr, "error: You must be logged in to the server (Unauthorized)")
		return errors.New("hidden")
	}
	got, err := a.Discover(t.Context(), DiscoveryOptions{})
	if err == nil || got.Checks[0].Reason != "unauthorized" {
		t.Fatal(got, err)
	}
}

func TestDiscoveryNoImplicitTarget(t *testing.T) {
	a := captureTestApp()
	a.captureCommand = func(context.Context, *exec.Cmd) error { t.Fatal("invalid dispatched"); return nil }
	_, err := a.Discover(t.Context(), DiscoveryOptions{Context: "bad\nname"})
	if err == nil || DiscoveryError(err).Error() != "The command arguments are invalid." {
		t.Fatal(err)
	}
}
