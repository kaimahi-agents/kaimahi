package app

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"text/template"
	"time"
)

func TestOrkaOfflineRequiredInputsAndRateDriftNeverEmit(t *testing.T) {
	for _, change := range []struct {
		name  string
		apply func(*CreateOptions)
	}{
		{"namespace", func(o *CreateOptions) { o.Namespace = "" }},
		{"provider", func(o *CreateOptions) { o.ProviderType = "" }},
		{"model", func(o *CreateOptions) { o.Model = "" }},
		{"secret", func(o *CreateOptions) { o.Secret = "" }},
		{"legacy-tools", func(o *CreateOptions) { o.Tools = "server:tool" }},
		{"empty-tools", func(o *CreateOptions) { o.Tools = "tool," }},
		{"agent-requests", func(o *CreateOptions) { o.SchemaTarget = "main"; o.AgentRequestsPerMinute = "1" }},
		{"agent-tokens", func(o *CreateOptions) { o.SchemaTarget = "main"; o.AgentTokensPerMinute = "1" }},
		{"provider-requests", func(o *CreateOptions) { o.SchemaTarget = "main"; o.ProviderRequestsPerMinute = "1" }},
		{"provider-tokens", func(o *CreateOptions) { o.SchemaTarget = "main"; o.ProviderTokensPerMinute = "1" }},
	} {
		t.Run(change.name, func(t *testing.T) {
			t.Setenv("PATH", t.TempDir())
			var out, diagnostics bytes.Buffer
			a := &App{Out: &out, Err: &diagnostics}
			opt := CreateOptions{Name: "sample", Namespace: "orka-system", ProviderType: "openai", Model: "local", Secret: "model-key", Out: filepath.Join(t.TempDir(), "artifact.yaml"), NoApply: true}
			change.apply(&opt)
			if err := a.CreateAgent(opt); err == nil {
				t.Fatal("accepted invalid bundle")
			}
			if out.Len() != 0 {
				t.Fatal("invalid input emitted bytes")
			}
			if _, err := os.Stat(opt.Out); !os.IsNotExist(err) {
				t.Fatal("invalid input created artifact")
			}
		})
	}
}

func TestOrkaNewFlagErrorsNeverEchoCredentials(t *testing.T) {
	secret := "sk-" + "proj-" + strings.Repeat("B", 32)
	for _, field := range []string{"account", "service", "port", "agent-requests", "agent-tokens", "provider-requests", "provider-tokens"} {
		t.Run(field, func(t *testing.T) {
			opt := CreateOptions{Name: "sample", Namespace: "orka-system", ProviderType: "openai", Model: "local", Secret: "model-key", Out: "-"}
			switch field {
			case "account":
				opt.ResultServiceAccount = secret
			case "service":
				opt.OrkaAPIService = secret
			case "port":
				opt.ResultPort = secret
			case "agent-requests":
				opt.AgentRequestsPerMinute = secret
			case "agent-tokens":
				opt.AgentTokensPerMinute = secret
			case "provider-requests":
				opt.ProviderRequestsPerMinute = secret
			case "provider-tokens":
				opt.ProviderTokensPerMinute = secret
			}
			a := &App{}
			err := a.CreateAgent(opt)
			if err == nil || strings.Contains(err.Error(), secret) {
				t.Fatalf("credential not safely refused for %s", field)
			}
		})
	}
}

func TestOrkaOfflineNeverCallsKubectlEvenWithTask(t *testing.T) {
	for _, stdout := range []bool{false, true} {
		a, opt, _, _, dir := orkaCreateFixture(t, "")
		opt.NoApply = true
		opt.Task = "Say hello"
		if stdout {
			opt.Out = "-"
		}
		if err := a.CreateAgent(opt); err != nil {
			t.Fatal(err)
		}
		if len(orkaCalls(t, dir)) != 0 {
			t.Fatal("offline called kubectl")
		}
	}
}

func TestOrkaOverwriteRefusalDoesNotMutate(t *testing.T) {
	a, opt, _, _, dir := orkaCreateFixture(t, "")
	if err := os.WriteFile(opt.Out, []byte("user owned"), 0600); err != nil {
		t.Fatal(err)
	}
	err := a.CreateAgent(opt)
	if err == nil {
		t.Fatal("overwrote artifact")
	}
	body, _ := os.ReadFile(opt.Out)
	if string(body) != "user owned" {
		t.Fatal("artifact changed")
	}
	for _, call := range orkaCalls(t, dir) {
		if call.Document != nil && !slices.Contains(call.Args, "--dry-run=server") {
			t.Fatal("created resource despite file collision")
		}
	}
}

func TestOrkaStaleReadinessStopsAtDeadline(t *testing.T) {
	a, opt, _, _, dir := orkaCreateFixture(t, "stale-ready")
	if err := validateOrkaResultOptions(&opt); err != nil {
		t.Fatal(err)
	}
	bundle, err := createOrkaBundle(opt)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	err = a.createOrkaOnline(ctx, opt, bundle)
	if err == nil || !strings.Contains(err.Error(), "Ready") {
		t.Fatalf("stale readiness admitted: %v", err)
	}
	writes := 0
	for _, call := range orkaCalls(t, dir) {
		if call.Document != nil && !slices.Contains(call.Args, "--dry-run=server") {
			writes++
			if call.Document["kind"] != "Provider" {
				t.Fatal("wrote beyond unready Provider")
			}
		}
	}
	if writes != 1 {
		t.Fatal("never exercised Provider wait")
	}
}

func assertOrkaForwardExited(t *testing.T, dir string) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(dir, "forward-pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(body))
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pid, 0); err != syscall.ESRCH {
		t.Fatalf("forward process %d still exists: %v", pid, err)
	}
}

func TestOrkaForwardClosedOnCancellationAndBindTimeout(t *testing.T) {
	for _, scenario := range []string{"", "forward-hang"} {
		t.Run(scenario, func(t *testing.T) {
			a, opt, _, _, dir := orkaCreateFixture(t, scenario)
			opt.ResultServiceAccount = "reader"
			opt.OrkaAPIService = "orka-api"
			opt.ResultPort = "19180"
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			started := time.Now()
			session, err := a.openOrkaResultSession(ctx, opt)
			if scenario == "" {
				if err != nil {
					t.Fatal(err)
				}
				cancel()
				session.close()
				if session.token != "" {
					t.Fatal("closed session retained token")
				}
			} else if err == nil {
				session.close()
				t.Fatal("unproven bind admitted")
			}
			if time.Since(started) > 2*time.Second {
				t.Fatal("cancelled forward waited for full bind timeout")
			}
			assertOrkaForwardExited(t, dir)
		})
	}
}

func TestOrkaKubectlCaptureCancellationBoundsHungProcess(t *testing.T) {
	a, _, _, _, _ := orkaCreateFixture(t, "hang-get")
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := a.orkaCapture(ctx, nil, "get", "crd", "agents.core.orka.ai", "-o", "json"); err == nil {
		t.Fatal("hung process succeeded")
	}
	if time.Since(started) > time.Second {
		t.Fatal("capture ignored cancellation")
	}
}

func TestOrkaKubectlCaptureRejectsOversizedStdout(t *testing.T) {
	a, _, _, _, _ := orkaCreateFixture(t, "oversized-get")
	if raw, err := a.orkaCapture(t.Context(), nil, "get", "crd", "agents.core.orka.ai", "-o", "json"); err == nil {
		t.Fatalf("accepted %d bytes from subprocess", len(raw))
	}
}

func TestOrkaSecretTemplateOnlyOutputsPresence(t *testing.T) {
	a, opt, _, _, dir := orkaCreateFixture(t, "")
	opt.DryRun = true
	if err := a.CreateAgent(opt); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, call := range orkaCalls(t, dir) {
		if !slices.Contains(call.Args, "secret") {
			continue
		}
		index := slices.Index(call.Args, "-o")
		if index < 0 || !strings.HasPrefix(call.Args[index+1], "go-template=") {
			t.Fatal("secret fetched as data instead of marker")
		}
		parsed, err := template.New("presence").Parse(strings.TrimPrefix(call.Args[index+1], "go-template="))
		if err != nil {
			t.Fatal(err)
		}
		for _, data := range []map[string]string{{"api-key": "private-model-value", "other": "also-private"}, {"api-key": ""}} {
			var out bytes.Buffer
			if err := parsed.Execute(&out, map[string]any{"data": data}); err != nil {
				t.Fatal(err)
			}
			if out.String() != "secret\npresent" {
				t.Fatalf("template emitted value or lost key presence: %q", out.String())
			}
		}
		found = true
	}
	if !found {
		t.Fatal("Secret prerequisite was not checked")
	}
}

func TestOrkaUnreadyAgentAndUnavailableResultBlockLaterSteps(t *testing.T) {
	for _, scenario := range []string{"stale-agent", "unavailable-task"} {
		t.Run(scenario, func(t *testing.T) {
			a, opt, _, _, dir := orkaCreateFixture(t, scenario)
			var requests atomic.Int32
			orkaResultServer(t, &opt, func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(404)
				fmt.Fprint(w, `{"error":{"code":404,"message":"task not found"}}`)
			})
			if err := validateOrkaResultOptions(&opt); err != nil {
				t.Fatal(err)
			}
			bundle, err := createOrkaBundle(opt)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			if err := a.createOrkaOnline(ctx, opt, bundle); err == nil {
				t.Fatal("unready dependency or unavailable result accepted")
			}
			writes := 0
			for _, call := range orkaCalls(t, dir) {
				if call.Document != nil && !slices.Contains(call.Args, "--dry-run=server") {
					writes++
				}
			}
			want := 2
			if scenario == "unavailable-task" {
				want = 3
			}
			if writes != want || requests.Load() != 1 {
				t.Fatalf("writes=%d requests=%d; wanted %d writes and probe only", writes, requests.Load(), want)
			}
			assertOrkaForwardExited(t, dir)
		})
	}
}

func TestOrkaResultAuthLossAndReplacementFailWithoutPrinting(t *testing.T) {
	for _, scenario := range []string{"auth-lost", "replace-after-read", "changed-spec-after-read", "disappear-after-read", "echo-token"} {
		t.Run(scenario, func(t *testing.T) {
			a, opt, out, diagnostics, dir := orkaCreateFixture(t, "")
			reads := 0
			orkaResultServer(t, &opt, func(w http.ResponseWriter, r *http.Request) {
				reads++
				w.Header().Set("Content-Type", "application/json")
				if reads == 1 {
					w.WriteHeader(404)
					fmt.Fprint(w, `{"error":{"code":404,"message":"task not found"}}`)
					return
				}
				switch scenario {
				case "auth-lost":
					w.WriteHeader(403)
					fmt.Fprintf(w, `{"error":{"code":403,"message":%q}}`, orkaTestToken())
				case "replace-after-read", "changed-spec-after-read", "disappear-after-read":
					name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/tasks/"), "/result")
					path := filepath.Join(dir, name+"-tasks.core.orka.ai.json")
					body, err := os.ReadFile(path)
					if err != nil {
						t.Error(err)
					}
					if scenario == "replace-after-read" {
						body = bytes.ReplaceAll(body, []byte("task-uid"), []byte("other-uid"))
					}
					if scenario == "changed-spec-after-read" {
						body = bytes.ReplaceAll(body, []byte(`"generation":1`), []byte(`"generation":2`))
					}
					if scenario == "disappear-after-read" {
						err = os.Remove(path)
					} else {
						err = os.WriteFile(path, body, 0600)
					}
					if err != nil {
						t.Error(err)
					}
					fmt.Fprint(w, `{"result":"stale answer"}`)
				case "echo-token":
					fmt.Fprintf(w, `{"result":%q}`, orkaTestToken())
				}
			})
			err := a.CreateAgent(opt)
			if err == nil {
				t.Fatal("unsafe result admitted")
			}
			if out.Len() != 0 || strings.Contains(diagnostics.String()+fmt.Sprint(err), orkaTestToken()) {
				t.Fatal("unsafe answer or token printed")
			}
			assertOrkaForwardExited(t, dir)
		})
	}
}
