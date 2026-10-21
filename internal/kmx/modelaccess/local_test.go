package modelaccess

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestLocalDiscoveryStates(t *testing.T) {
	for _, provider := range []string{"ollama", "foundry-local"} {
		for _, tc := range []struct {
			name, body string
			status     int
			want       State
		}{
			{"empty", `[]`, 200, Empty}, {"installed", `["qwen:1"]`, 200, Installed},
			{"malformed", `private-canary`, 200, Failure}, {"http", `private-canary`, 503, Failure},
		} {
			t.Run(provider+"/"+tc.name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method != "GET" || r.Header.Get("Authorization") != "" {
						t.Errorf("unexpected request %s", r.Method)
					}
					expected := "/openai/models"
					body := tc.body
					if provider == "ollama" {
						expected = "/api/tags"
						if tc.name == "empty" {
							body = `{"models":[]}`
						} else if tc.name == "installed" {
							body = `{"models":[{"name":"qwen:1","size":42}]}`
						}
					}
					if r.URL.Path != expected {
						t.Errorf("path=%s", r.URL.Path)
					}
					w.WriteHeader(tc.status)
					fmt.Fprint(w, body)
				}))
				defer server.Close()
				d := LocalDiscovery{OllamaEndpoint: server.URL, LookPath: func(string) (string, error) { return "foundry", nil }, Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
					if name != "foundry" || strings.Join(args, " ") != "server status" {
						t.Errorf("not read-only status: %s %v", name, args)
					}
					return []byte("Server endpoint: " + server.URL), nil
				}}
				var got Report
				if provider == "ollama" {
					got = d.Ollama(context.Background())
				} else {
					got = d.Foundry(context.Background())
				}
				if got.State != tc.want {
					t.Fatalf("got=%+v", got)
				}
				data, _ := json.Marshal(got)
				if strings.Contains(string(data), "private-canary") {
					t.Fatal("leaked upstream output")
				}
				if tc.want == Installed {
					if len(got.Models) != 1 || got.Models[0].Route.Model != "qwen:1" || got.Models[0].Route.Endpoint != server.URL+"/v1" {
						t.Fatalf("models=%+v", got.Models)
					}
				}
			})
		}
	}
}
func TestMissingAndStoppedRuntimeAreDifferent(t *testing.T) {
	missing := func(string) (string, error) { return "", exec.ErrNotFound }
	d := LocalDiscovery{LookPath: missing, Client: &http.Client{Transport: refusedTransport{syscall.ECONNREFUSED}}}
	if got := d.Foundry(context.Background()); got.State != Absent {
		t.Fatal(got)
	}
	if got := d.Ollama(context.Background()); got.State != Absent {
		t.Fatal(got)
	}
	d.LookPath = func(string) (string, error) { return "foundry", nil }
	d.Run = func(context.Context, string, ...string) ([]byte, error) {
		return []byte("private-canary"), errors.New("private-canary")
	}
	if got := d.Foundry(context.Background()); got.State != Failure || strings.Contains(got.Reason, "private-canary") {
		t.Fatal(got)
	}
	if got := d.Ollama(context.Background()); got.State != Failure {
		t.Fatal(got)
	}
}

type refusedTransport struct{ errno syscall.Errno }

func (r refusedTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, &net.OpError{Op: "dial", Net: "tcp", Err: r.errno}
}

func TestWinsockRefusalIsAbsentWithoutBinary(t *testing.T) {
	d := LocalDiscovery{LookPath: func(string) (string, error) { return "", exec.ErrNotFound }, Client: &http.Client{Transport: refusedTransport{syscall.Errno(10061)}}}
	if got := d.Ollama(context.Background()); got.State != Absent {
		t.Fatalf("Winsock refusal: %+v", got)
	}
}

func TestFoundryStatusFromCLIReport(t *testing.T) {
	// Output shape captured in microsoft/Foundry-Local issue #915 (CLI 0.10.2).
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `["qwen:1"]`) }))
	defer server.Close()
	for _, suffix := range []string{"", "/openai/status"} {
		d := LocalDiscovery{LookPath: func(string) (string, error) { return "foundry", nil }, Run: func(context.Context, string, ...string) ([]byte, error) {
			return []byte("State    Ready\nPID      14744\nStarted  2026-07-26 12:32:01Z\nUptime   5m 42s\nWeb URLs " + server.URL + suffix + "\nDocs: https://learn.microsoft.com/en-us/azure/foundry-local/"), nil
		}}
		if got := d.Foundry(context.Background()); got.State != Installed {
			t.Fatal(got)
		}
	}
}

func TestDiscoveryBoundsAndNoRedirect(t *testing.T) {
	calls := 0
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer server.Close()
	d := LocalDiscovery{OllamaEndpoint: server.URL}
	if got := d.Ollama(context.Background()); got.State != Failure || calls != 0 {
		t.Fatalf("report=%+v calls=%d", got, calls)
	}
	server.Close()
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, strings.Repeat("x", (1<<20)+1)) }))
	defer server.Close()
	d.OllamaEndpoint = server.URL
	if got := d.Ollama(context.Background()); got.State != Failure {
		t.Fatal(got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if got := d.Ollama(ctx); got.State != Failure || time.Since(start) > time.Second {
		t.Fatal(got)
	}
}
func TestFoundryRejectsUnsafeOrAmbiguousStatus(t *testing.T) {
	for _, status := range []string{"no endpoint", "http://example.com:1234", "http://user:private-canary@127.0.0.1:1234", "http://127.0.0.1:1234?token=private-canary", "http://127.0.0.1:1234 http://127.0.0.1:4321"} {
		d := LocalDiscovery{LookPath: func(string) (string, error) { return "foundry", nil }, Run: func(context.Context, string, ...string) ([]byte, error) { return []byte(status), nil }}
		got := d.Foundry(context.Background())
		if got.State != Failure || strings.Contains(got.Reason, "private-canary") {
			t.Fatal(got)
		}
	}
}
func TestInventoryValidation(t *testing.T) {
	for _, body := range []string{`{}`, `{"models":null}`, `{"models":[]} {}`, `{"models":[{"name":"bad\nname"}]}`, `{"models":[{"name":"x","size":-1}]}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
		got := (LocalDiscovery{OllamaEndpoint: server.URL}).Ollama(context.Background())
		server.Close()
		if got.State != Failure {
			t.Fatalf("body %s: %+v", body, got)
		}
	}
}
func TestExplicitSelectionDoesNotFallback(t *testing.T) {
	routes := []Route{{Provider: "ollama", Model: "exact:1", Endpoint: "http://127.0.0.1:11434/v1", Auth: "none"}}
	got, err := Select(routes, "ollama", "exact:1")
	if err != nil || got != routes[0] {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err = Select(routes, "ollama", "missing"); err == nil {
		t.Fatal("selected a different model")
	}
	for _, endpoint := range []string{"http://x:private-canary@localhost/v1", "https://host/v1?token=private-canary", "https://host/v1#private-canary"} {
		r := routes[0]
		r.Endpoint = endpoint
		if err := r.Validate(); err == nil || strings.Contains(err.Error(), "private-canary") {
			t.Fatalf("unsafe error %v", err)
		}
	}
	if err := (Route{Provider: "fake", Model: "fake", Auth: "none"}).Validate(); err != nil {
		t.Fatal(err)
	}
}
func TestStatusSubprocessLimits(t *testing.T) {
	t.Setenv("KMX_MODEL_STATUS_HELPER", "1")
	d := LocalDiscovery{}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := d.run(ctx, os.Args[0], "-test.run=TestStatusHelper", "--", "hang"); err == nil {
		t.Fatal("unbounded status process")
	}
	if _, err := d.run(context.Background(), os.Args[0], "-test.run=TestStatusHelper", "--", "large"); err == nil {
		t.Fatal("unbounded status output")
	}
}

func TestStatusHelper(t *testing.T) {
	if os.Getenv("KMX_MODEL_STATUS_HELPER") != "1" {
		return
	}
	switch os.Args[len(os.Args)-1] {
	case "hang":
		time.Sleep(10 * time.Second)
	case "large":
		fmt.Print(strings.Repeat("x", probeLimit+1))
	}
	os.Exit(0)
}

func TestInstallGuidancePlatforms(t *testing.T) {
	for _, goos := range []string{"windows", "darwin", "linux"} {
		for _, provider := range []string{"foundry-local", "ollama"} {
			g := InstallGuidance(provider, goos, "amd64")
			if !strings.HasPrefix(g.URL, "https://") || g.RuntimeSize == "" || g.ModelSize == "" || g.Requirements == "" {
				t.Fatal(g)
			}
			if g.Recommended != (provider == "foundry-local" && goos == "windows") {
				t.Fatal(g)
			}
		}
	}
}
