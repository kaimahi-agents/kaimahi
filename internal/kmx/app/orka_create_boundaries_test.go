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

func TestOrkaServiceNameValidationDoesNotReserveAgentNames(t *testing.T) {
	for _, name := range []string{"hello-world", "hello-tools", "orka-api", "a", strings.Repeat("a", 63)} {
		opt := CreateOptions{OrkaAPIService: name}
		if err := validateOrkaResultOptions(&opt); err != nil {
			t.Errorf("valid Service name %q refused: %v", name, err)
		}
	}
	for _, name := range []string{"Upper", "a.b", "a_b", "-a", "a-", "a b", "a/b", "1service", strings.Repeat("a", 64)} {
		opt := CreateOptions{OrkaAPIService: name}
		if err := validateOrkaResultOptions(&opt); err == nil {
			t.Errorf("invalid Service name %q accepted", name)
		}
	}
}

func TestOrkaTerminatingResourcesBlockLaterSteps(t *testing.T) {
	for _, tc := range []struct {
		kind   string
		writes int
	}{{"Provider", 1}, {"Agent", 2}, {"Task", 3}} {
		t.Run(tc.kind, func(t *testing.T) {
			a, opt, out, _, dir := orkaCreateFixture(t, "terminating-"+strings.ToLower(tc.kind))
			var requests atomic.Int32
			orkaResultServer(t, &opt, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if requests.Add(1) == 1 {
					w.WriteHeader(http.StatusNotFound)
					fmt.Fprint(w, `{"error":{"code":404,"message":"task not found"}}`)
					return
				}
				fmt.Fprint(w, `{"result":"answer from terminating Task"}`)
			})
			err := a.CreateAgent(opt)
			if err == nil || !strings.Contains(err.Error(), tc.kind+"/") || !strings.Contains(err.Error(), "terminating") {
				t.Errorf("terminating %s not refused: %v", tc.kind, err)
			}
			writes := 0
			for _, call := range orkaCalls(t, dir) {
				if call.Document != nil && !slices.Contains(call.Args, "--dry-run=server") {
					writes++
				}
			}
			if writes != tc.writes || requests.Load() != 1 || out.Len() != 0 {
				t.Fatalf("terminating %s unlocked later steps: writes=%d requests=%d stdout=%q", tc.kind, writes, requests.Load(), out.String())
			}
			assertOrkaForwardExited(t, dir)
		})
	}
}

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

func TestOrkaOfflineCollisionAdviceNeverBulkApplies(t *testing.T) {
	for _, target := range []string{"file", "device"} {
		t.Run(target, func(t *testing.T) {
			a, opt, out, diagnostics, dir := orkaCreateFixture(t, "")
			opt.NoApply = true
			if target == "device" {
				opt.Out = os.DevNull
			} else if err := os.WriteFile(opt.Out, []byte("user owned\n"), 0600); err != nil {
				t.Fatal(err)
			}
			err := a.CreateAgent(opt)
			if err == nil {
				t.Fatal("existing artifact accepted")
			}
			message := err.Error() + diagnostics.String()
			if strings.Contains(message, "kubectl") || strings.Contains(message, "Apply it as it stands") {
				t.Fatalf("unsafe or unpinned collision advice: %s", message)
			}
			for _, want := range []string{"already exists", "--out", "Secret skeleton", "Provider", "Agent", "Task"} {
				if !strings.Contains(message, want) {
					t.Errorf("collision advice missing %q: %s", want, message)
				}
			}
			if target == "file" {
				body, err := os.ReadFile(opt.Out)
				if err != nil || string(body) != "user owned\n" {
					t.Fatalf("existing file changed: %q, %v", body, err)
				}
			}
			if out.Len() != 0 || len(orkaCalls(t, dir)) != 0 {
				t.Fatal("offline collision emitted manifest or invoked kubectl")
			}
		})
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
	// Include the real executable-boundary preflight: race-instrumented
	// helper startup alone can exceed a second before the Ready poll begins.
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	err = a.createOrkaOnline(ctx, opt, bundle)
	// The deadline can interrupt either the poll delay or its kubectl read.
	// Assert the reached boundary and timeout, not one path's diagnostic text.
	if err == nil || ctx.Err() != context.DeadlineExceeded {
		t.Fatalf("stale readiness did not stop at the deadline: %v", err)
	}
	writes, reads := 0, 0
	for _, call := range orkaCalls(t, dir) {
		if call.Document != nil && !slices.Contains(call.Args, "--dry-run=server") {
			writes++
			if call.Document["kind"] != "Provider" {
				t.Fatal("wrote beyond unready Provider")
			}
		}
		if slices.Contains(call.Args, "get") && slices.Contains(call.Args, "providers.core.orka.ai") && slices.Contains(call.Args, "json") {
			reads++
		}
	}
	if writes != 1 || reads == 0 {
		t.Fatalf("never exercised Provider wait: writes=%d reads=%d", writes, reads)
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
			orkaResultServer(t, &opt, func(http.ResponseWriter, *http.Request) {
				t.Error("opening a session sent HTTP before its explicit access probe")
			})
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

func TestOrkaResultLossStopsDependencyWait(t *testing.T) {
	for _, loss := range []string{"forward", "connection"} {
		t.Run(loss, func(t *testing.T) {
			a, opt, out, _, dir := orkaCreateFixture(t, "stale-ready")
			server := orkaResultServer(t, &opt, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusNotFound)
				fmt.Fprint(w, `{"error":{"code":404,"message":"task not found"}}`)
			})
			if err := validateOrkaResultOptions(&opt); err != nil {
				t.Fatal(err)
			}
			bundle, err := createOrkaBundle(opt)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			done := make(chan error, 1)
			go func() { done <- a.createOrkaOnline(ctx, opt, bundle) }()
			finished := false
			defer func() {
				cancel()
				if !finished {
					<-done
				}
			}()
			// Provider creation proves the access probe succeeded. Its stale
			// Ready condition leaves the real pipeline waiting at this boundary.
			for {
				if _, err := os.Stat(filepath.Join(dir, "sample-providers.core.orka.ai.json")); err == nil {
					break
				}
				select {
				case err := <-done:
					finished = true
					t.Fatalf("never reached Provider wait: %v", err)
				case <-ctx.Done():
					t.Fatal("never reached Provider wait")
				case <-time.After(10 * time.Millisecond):
				}
			}
			if loss == "forward" {
				body, err := os.ReadFile(filepath.Join(dir, "forward-pid"))
				if err != nil {
					t.Fatal(err)
				}
				pid, err := strconv.Atoi(string(body))
				if err != nil {
					t.Fatal(err)
				}
				if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
					t.Fatal(err)
				}
			} else {
				server.CloseClientConnections()
			}
			select {
			case err := <-done:
				finished = true
				if err == nil {
					t.Fatal("result-session loss allowed creation to succeed")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("result-session loss did not cancel the dependency wait")
			}
			if out.Len() != 0 {
				t.Fatal("result-session loss printed an answer")
			}
			for _, call := range orkaCalls(t, dir) {
				if call.Document != nil && !slices.Contains(call.Args, "--dry-run=server") && call.Document["kind"] != "Provider" {
					t.Fatal("later resource created after result-session loss")
				}
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
			// The budget includes schema/preflight subprocesses, not only the
			// blocked wait. Keep room for instrumented helper startup under -race.
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
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
	for _, scenario := range []string{"auth-lost", "replace-after-read", "changed-spec-after-read", "changed-prompt-after-read", "disappear-after-read", "terminating-after-read", "echo-token"} {
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
				case "replace-after-read", "changed-spec-after-read", "changed-prompt-after-read", "disappear-after-read", "terminating-after-read":
					name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/tasks/"), "/result")
					path := filepath.Join(dir, name+"-tasks.core.orka.ai.json")
					body, err := os.ReadFile(path)
					if err != nil {
						t.Error(err)
					}
					if scenario == "replace-after-read" {
						body = bytes.ReplaceAll(body, []byte("task-uid"), []byte("other-uid"))
					}
					if scenario == "changed-spec-after-read" || scenario == "changed-prompt-after-read" {
						body = bytes.ReplaceAll(body, []byte(`"generation":1`), []byte(`"generation":2`))
					}
					if scenario == "changed-prompt-after-read" {
						body = bytes.ReplaceAll(body, []byte(`"prompt":"Say hello"`), []byte(`"prompt":"Run a different request"`))
					}
					if scenario == "terminating-after-read" {
						body = bytes.ReplaceAll(body, []byte(`"generation":1`), []byte(`"generation":1,"deletionTimestamp":"2026-01-01T00:00:00Z","finalizers":["orka.ai/cleanup"]`))
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
