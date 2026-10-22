package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess/copilot"
)

type cliCopilot struct {
	login, gh, logout, list int
	err                     error
}

func (f *cliCopilot) Login(_ context.Context, show func(copilot.DevicePrompt) error) error {
	f.login++
	if f.err != nil {
		return f.err
	}
	return show(copilot.DevicePrompt{VerificationURI: "https://github.com/login/device", UserCode: "ABCD-EFGH"})
}
func (f *cliCopilot) LoginGH(context.Context) error { f.gh++; return f.err }
func (f *cliCopilot) Logout() error                 { f.logout++; return f.err }
func (f *cliCopilot) List(context.Context) ([]modelaccess.Route, error) {
	f.list++
	return []modelaccess.Route{{Provider: "copilot", Model: "gpt-test", Endpoint: "https://api.githubcopilot.com", Auth: "copilot"}}, f.err
}
func copilotDeps(t *testing.T, f *cliCopilot, out, diag *bytes.Buffer) dependencies {
	t.Helper()
	deps := productionDependencies()
	deps.stdout = out
	deps.stderr = diag
	deps.loadConfig = nil
	deps.newApp = nil
	deps.discoverModels = func(context.Context) []modelaccess.Report { t.Fatal("Copilot probed local discovery"); return nil }
	deps.newCopilot = func() copilotAccess { return f }
	return deps
}
func TestCopilotLoginAndLogoutAreExplicitConfigFree(t *testing.T) {
	for _, tc := range []struct {
		args              []string
		login, gh, logout int
		prompt            bool
	}{
		{[]string{"model", "login"}, 1, 0, 0, true}, {[]string{"model", "login", "--gh"}, 0, 1, 0, false}, {[]string{"model", "logout"}, 0, 0, 1, false},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			var out, diag bytes.Buffer
			f := &cliCopilot{}
			if err := execute(tc.args, copilotDeps(t, f, &out, &diag)); err != nil {
				t.Fatal(err)
			}
			if f.login != tc.login || f.gh != tc.gh || f.logout != tc.logout || f.list != 0 || out.Len() != 0 {
				t.Fatalf("calls=%+v stdout=%s", f, out.String())
			}
			if strings.Contains(diag.String(), "ABCD-EFGH") != tc.prompt || strings.Contains(diag.String(), "https://github.com/login/device") != tc.prompt {
				t.Fatalf("prompt: %s", diag.String())
			}
		})
	}
}
func TestCopilotLoginExplainsRuntimeCredentialsWithoutKeychain(t *testing.T) {
	t.Setenv("COPILOT_GITHUB_TOKEN", "login-private-test-value")
	for _, args := range [][]string{{"model", "login"}, {"model", "login", "--gh"}} {
		var out, diag bytes.Buffer
		f := &cliCopilot{err: copilot.ErrKeychain}
		if err := execute(args, copilotDeps(t, f, &out, &diag)); err != nil {
			t.Fatalf("keychain guidance failed: %v", err)
		}
		for _, want := range []string{"gh auth token", "COPILOT_GITHUB_TOKEN", "runtime", "not saved"} {
			if !strings.Contains(diag.String(), want) {
				t.Fatalf("missing %q in guidance: %s", want, &diag)
			}
		}
		if out.Len() != 0 || strings.Contains(diag.String(), "login-private-test-value") || strings.Contains(diag.String(), "credential saved") || strings.Contains(diag.String(), "ABCD-EFGH") {
			t.Fatal("guidance claimed a saved credential or displayed device sign-in")
		}
	}
	var out, diag bytes.Buffer
	if err := execute([]string{"model", "login"}, copilotDeps(t, &cliCopilot{err: copilot.ErrPolicy}, &out, &diag)); !errors.Is(err, copilot.ErrPolicy) {
		t.Fatalf("policy refusal hidden by guidance: %v", err)
	}
}

func TestCopilotListFormatsNeverLogin(t *testing.T) {
	for _, format := range []string{"table", "json"} {
		t.Run(format, func(t *testing.T) {
			var out, diag bytes.Buffer
			f := &cliCopilot{}
			if err := execute([]string{"model", "list", "--provider", "copilot", "-o", format}, copilotDeps(t, f, &out, &diag)); err != nil {
				t.Fatal(err)
			}
			if f.list != 1 || f.login+f.gh+f.logout != 0 || diag.Len() != 0 {
				t.Fatalf("calls=%+v stderr=%s", f, diag.String())
			}
			if format == "json" {
				var routes []modelaccess.Route
				if err := json.Unmarshal(out.Bytes(), &routes); err != nil || len(routes) != 1 || routes[0].Auth != "copilot" || routes[0].Model != "gpt-test" {
					t.Fatalf("routes=%v err=%v", routes, err)
				}
			} else if !strings.Contains(out.String(), "gpt-test") {
				t.Fatal(out.String())
			}
		})
	}
	for _, failure := range []error{copilot.ErrAuthRequired, copilot.ErrAuthRevoked, copilot.ErrSubscription, copilot.ErrPolicy, copilot.ErrQuota, copilot.ErrKeychain} {
		var out, diag bytes.Buffer
		f := &cliCopilot{err: failure}
		err := execute([]string{"model", "list", "--provider", "copilot", "-o", "json"}, copilotDeps(t, f, &out, &diag))
		if !errors.Is(err, failure) || out.Len() != 0 || f.login+f.gh != 0 {
			t.Fatalf("err=%v stdout=%s", err, out.String())
		}
	}
}
func TestCopilotHelpCompletionAndInvalidArgumentsAreInert(t *testing.T) {
	for _, tc := range []struct {
		args []string
		fail bool
	}{
		{[]string{"model", "login", "--help"}, false}, {[]string{"model", "logout", "--help"}, false}, {[]string{"model", "list", "--help"}, false}, {[]string{"__complete", "model", "login", ""}, false}, {[]string{"__complete", "model", "list", "--provider", ""}, false},
		{[]string{"model", "login", "extra"}, true}, {[]string{"model", "logout", "--gh"}, true}, {[]string{"model", "logout", "extra"}, true}, {[]string{"model", "list", "--provider", "unknown"}, true}, {[]string{"model", "list", "--provider", "copilot", "-o", "bad"}, true},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			var out, diag bytes.Buffer
			deps := copilotDeps(t, &cliCopilot{}, &out, &diag)
			deps.newCopilot = func() copilotAccess { t.Fatal("operational factory invoked"); return nil }
			err := execute(tc.args, deps)
			if (err != nil) != tc.fail {
				t.Fatalf("err=%v", err)
			}
		})
	}
}
