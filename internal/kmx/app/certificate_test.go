package app

import (
	"bytes"
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

// A model client surfaces a failed handshake as a generic connection error.
// kmx cannot fix that message, but it must recognise it, so a seam rejected
// over trust is not reported as a seam that is merely down.
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

// A missing namespace is tolerated, so the note it prints instead is the only
// instruction an operator gets — and it has to name a command that exists and
// publishes into THAT namespace. There is exactly one: nothing left in kmx
// knows which namespaces want the authority, so the command that points a
// workload at the seam is the command that has to be re-run.
func TestMissingNamespaceNamesTheCommandThatPublishesIntoIt(t *testing.T) {
	for _, tc := range []struct {
		namespace string
		want      string
	}{
		{"payments", "`kmx migrate <deployment> --namespace payments --model <provider>/<model>`"},
		{"kagent", "`kmx migrate <deployment> --namespace kagent --model <provider>/<model>`"},
	} {
		t.Run(tc.namespace, func(t *testing.T) {
			a := appWithKubectl(t, `case "$*" in
*"get namespace "*) printf 'Error from server (NotFound): namespaces "%s" not found\n' "${*##* }" >&2; exit 1 ;;
*) exit 0 ;;
esac`)
			if err := a.publishAuthority(tc.namespace, []byte("-----BEGIN CERTIFICATE-----\n")); err != nil {
				t.Fatalf("an absent namespace was not tolerated: %v", err)
			}
			note := a.Err.(*bytes.Buffer).String()
			if !strings.Contains(note, tc.want) {
				t.Errorf("the note does not name the publisher %s:\n%s", tc.want, note)
			}
			if strings.Contains(note, "kmx govern") {
				t.Errorf("the note still names the retired governance command:\n%s", note)
			}
			if !strings.Contains(note, config.PlaneCASecret) {
				t.Errorf("the note does not say what was not published:\n%s", note)
			}
		})
	}
}
