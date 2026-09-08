package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

// appWithKubectl puts a kubectl on PATH that does whatever the script says,
// so the reading side of these commands can be driven without a cluster.
func appWithKubectl(t *testing.T, script string) *App {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake kubectl is a shell script")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	r := run.Default()
	r.Stdout, r.Stderr, r.Echo = out, errOut, false
	return &App{
		Cfg: &config.Config{KubeContext: "kind-kaimahi-p1", ContextSource: config.SourceKubeCtx},
		Run: r, Out: out, Err: errOut,
	}
}

// Two packages name the CA Secret: config, which the plane-side commands read,
// and scaffold, which writes it into every manifest that points at a seam.
// scaffold is a leaf package and does not import config, so the constants are
// duplicated — and a duplicate that drifts here would produce manifests naming
// a Secret nothing creates. kagent refuses those with Accepted=false, which
// reads as a broken seam rather than as a typo.
func TestTheCASecretIsNamedTheSameWhereverItIsNamed(t *testing.T) {
	if config.PlaneCASecret != scaffold.PlaneCASecret {
		t.Errorf("config names the CA Secret %q and scaffold names it %q",
			config.PlaneCASecret, scaffold.PlaneCASecret)
	}
	if config.PlaneCAKey != scaffold.PlaneCAKey {
		t.Errorf("config names the CA key %q and scaffold names it %q",
			config.PlaneCAKey, scaffold.PlaneCAKey)
	}
}

// The ModelConfig `kmx agent chat --interactive` applies is GENERATED, so
// scripts/check-seam-tls.py cannot see it — that checker reads the tree. This
// is the rule held over it instead.
//
// It is the manifest most able to drift: it was a second copy of the seam URL
// spelled out in Go while the committed presets moved to TLS, and nothing
// would have said so. kagent admits an https baseUrl with no authority, and a
// tls block beside an http one, and refuses neither.
func TestTheInteractiveGovernedModelConfigIsHttpsAndNamesTheAuthority(t *testing.T) {
	body, err := interactiveModelManifest("kmx-governed-ollama-demo", "kmx-token-demo", "qwen2.5:3b", true, "demo")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Spec struct {
			OpenAI map[string]string `json:"openAI"`
			TLS    map[string]string `json:"tls"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(doc.Spec.OpenAI["baseUrl"], "https://") {
		t.Errorf("the interactive governed seam is not https: %q", doc.Spec.OpenAI["baseUrl"])
	}
	if doc.Spec.TLS["caCertSecretRef"] != scaffold.PlaneCASecret {
		t.Errorf("caCertSecretRef = %q, want %q", doc.Spec.TLS["caCertSecretRef"], scaffold.PlaneCASecret)
	}
	if doc.Spec.TLS["caCertSecretKey"] != scaffold.PlaneCAKey {
		t.Errorf("caCertSecretKey = %q, want %q", doc.Spec.TLS["caCertSecretKey"], scaffold.PlaneCAKey)
	}
	if _, ok := doc.Spec.TLS["disableVerify"]; ok {
		t.Error("the interactive governed seam carries disableVerify")
	}

	// An UNGOVERNED preset points at the provider, so it must not name the
	// plane's authority — a Secret it has no use for, mounted into its pod
	// for nothing, and absent on a cluster with no plane.
	body, err = interactiveModelManifest("kmx-ollama-demo", "", "qwen2.5:3b", false, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), scaffold.PlaneCASecret) {
		t.Errorf("an ungoverned preset names the plane's authority: %s", body)
	}
}

// `kmx plane --step certificate` is the command every message about a
// certificate tells an operator to run, so it has to be a step that exists.
func TestTheCertificateStepIsAddressableOnItsOwn(t *testing.T) {
	found := false
	for _, s := range PlaneSteps {
		if s == "certificate" {
			found = true
		}
	}
	if !found {
		t.Fatalf("`kmx plane --step certificate` is named in error messages but is not a step: %v", PlaneSteps)
	}
	// It has to run BEFORE deploy: the proxy mounts the certificate and
	// refuses to start without it, so a deploy that preceded the mint would
	// crash-loop until the next run.
	certificate, deploy := -1, -1
	for i, s := range PlaneSteps {
		switch s {
		case "certificate":
			certificate = i
		case "deploy":
			deploy = i
		}
	}
	if certificate > deploy {
		t.Errorf("the certificate is minted after the deploy that mounts it: %v", PlaneSteps)
	}
}

// The three states are three different situations and three different things
// to do about them. Reporting "could not read the Secret" as "there is no
// certificate" would send an operator to `kmx plane` when the problem is that
// nobody can read it — the same false-zero rule the governance counts follow.
func TestStatusSeparatesAnAbsentCertificateFromAnUnreadableOne(t *testing.T) {
	cases := []struct {
		name       string
		script     string
		wantState  string
		wantInLine string
	}{
		{
			name:       "absent",
			script:     "printf 'Error from server (NotFound): secrets \"x\" not found\\n' >&2; exit 1",
			wantState:  stateNone,
			wantInLine: "not been deployed",
		},
		{
			name:       "unreadable",
			script:     "printf 'error: You must be logged in to the server (Unauthorized)\\n' >&2; exit 1",
			wantState:  stateUnknown,
			wantInLine: "Unauthorized",
		},
		{
			name:       "present but not a certificate",
			script:     `printf '{"data":{"tls.crt":"bm90LWEtY2VydA=="}}'`,
			wantState:  stateUnknown,
			wantInLine: "no readable certificate",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			app := appWithKubectl(t, c.script)
			got := app.seamCertificate()
			if got.State != c.wantState {
				t.Errorf("state = %q, want %q (line %q)", got.State, c.wantState, got.Line)
			}
			if !strings.Contains(got.Line, c.wantInLine) {
				t.Errorf("line %q does not carry %q", got.Line, c.wantInLine)
			}
		})
	}
}

// A live certificate reports its issuer, its subject and its expiry, because
// the operator reading this line is usually reading it after something would
// not connect — and kagent's own message for that says only that a connection
// failed.
func TestStatusNamesTheCertificateItFound(t *testing.T) {
	app := appWithKubectl(t, `printf '%s' "$KMX_TEST_SEAM_TLS"`)
	t.Setenv("KMX_TEST_SEAM_TLS", seamTLSSecret(t))
	got := app.seamCertificate()
	if got.State != "valid" {
		t.Fatalf("state = %q, line %q", got.State, got.Line)
	}
	for _, want := range []string{"kaimahi-plane-seams", "kaimahi-plane-ca", "expires in"} {
		if !strings.Contains(got.Line, want) {
			t.Errorf("%q missing from %q", want, got.Line)
		}
	}
}

// kagent's agent runtime surfaces a failed handshake as a generic connection
// error — the certificate-naming diagnostic in its own source is never called
// on that path. kmx cannot fix that message, but it must recognise it, so a
// seam rejected over trust is not reported as a seam that is merely down.
func TestACertificateFailureIsRecognisedInASeamVerdict(t *testing.T) {
	for _, message := range []string{
		`Get "https://kaimahi-mcp-gateway.kaimahi:8081/upstream/x/mcp": tls: failed to verify certificate: x509: certificate signed by unknown authority`,
		`x509: certificate has expired or is not yet valid`,
		`certificate is valid for kaimahi-proxy.kaimahi, not kaimahi-proxy.other`,
		`remote error: tls: bad certificate`,
		`SSL: CERTIFICATE_VERIFY_FAILED`,
	} {
		if !certificateFailure(message) {
			t.Errorf("a trust failure was not recognised: %q", message)
		}
	}
	for _, message := range []string{
		"connection refused",
		"context deadline exceeded",
		"401 Unauthorized: credential not found",
		"i/o timeout",
	} {
		if certificateFailure(message) {
			t.Errorf("an ordinary failure was reported as a certificate problem: %q", message)
		}
	}
}
