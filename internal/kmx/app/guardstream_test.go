package app

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/guard"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

// The guard's whole value is that it names where it is about to act BEFORE
// acting. That value survives only if the naming cannot be routed away with
// the command's own output: a careful reader can survive a banner they did
// not read, and nobody survives a banner that went somewhere they were not
// looking.
//
// So the banner goes to stderr and the guard writes nothing to stdout — the
// stream a pipe or a redirect usually takes.
func TestTheGuardNamesTheClusterOnStderrAndNeverOnStdout(t *testing.T) {
	kubeconfig, err := guard.ParseKubeconfig([]byte(`{
	  "clusters": [{"name":"c","cluster":{"server":"https://127.0.0.1:6443"}}],
	  "contexts": [{"name":"kind-real","context":{"cluster":"c"}}],
	  "current-context": "kind-real"
	}`))
	if err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	a := &App{
		Cfg: &config.Config{KubeContext: "kind-real", ContextSource: config.SourceKubeCtx},
		Run: &run.Runner{}, Out: &out, Err: &errOut, Stdin: os.Stdin,
	}
	if err := guard.Check(kubeconfig, guard.Request{
		Action:  "install kagent",
		Context: a.Cfg.KubeContext,
		Source:  a.Cfg.ContextSource,
		Command: "kmx up",
	}, a.Err, nil); err != nil {
		t.Fatalf("a local kind cluster should proceed: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("the guard wrote to stdout, where a pipe can swallow it:\n%s", out.String())
	}
	for _, want := range []string{"about to:", "context:  kind-real", "chosen by: " + config.SourceKubeCtx} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("stderr does not carry %q:\n%s", want, errOut.String())
		}
	}
}

// Every call site must say who chose the context. A guard request that does
// not is refused rather than quietly labelled "unrecorded": the banner's job
// is to distinguish a cluster an operator named from one kmx invented, and a
// missing source erases exactly that distinction.
func TestAGuardRequestThatDoesNotSayWhoChoseIsRefused(t *testing.T) {
	kubeconfig, err := guard.ParseKubeconfig([]byte(`{
	  "clusters": [{"name":"c","cluster":{"server":"https://127.0.0.1:6443"}}],
	  "contexts": [{"name":"kind-real","context":{"cluster":"c"}}],
	  "current-context": "kind-real"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	var errOut bytes.Buffer
	err = guard.Check(kubeconfig, guard.Request{
		Action: "install kagent", Context: "kind-real", Command: "kmx up",
	}, &errOut, nil)
	if err == nil || !strings.Contains(err.Error(), "did not record who chose") {
		t.Fatalf("a sourceless request was allowed through: %v", err)
	}
}
