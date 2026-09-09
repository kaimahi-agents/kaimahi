// Package guard is scripts/kube-guard.sh in Go: the context-safety net in
// front of every mutating kmx command.
//
// The four rules below are the shell script's and both implementations
// have to agree on them. This one adds a rule the script does not have:
// every caller must record who chose the context, and a context nobody
// chose is refused rather than acted on.
//
//   - ALWAYS print where the action is about to land (context, API-server
//     host, namespaces) on stderr, so the answer is on screen even when
//     nothing is asked.
//   - A LOCAL kind cluster proceeds with a banner and no question. That
//     keeps the kind path's behaviour unchanged for existing users and for
//     CI. A kind-NAMED context the kubeconfig does not hold counts as local
//     too, because that is bring-up naming the cluster it is about to
//     create — but only for callers that create. A caller setting
//     MustBeKnown is told the kubeconfig does not describe it and has to
//     confirm, which is what stops a delete-by-container-name running under
//     a banner saying nothing is there.
//   - ANY other context requires explicit confirmation naming the context.
//   - FAIL CLOSED: no confirmation, no action. An unknown context, an
//     unreadable kubeconfig, or a non-interactive shell without
//     KAIMAHI_CONFIRM all refuse rather than guess.
//
// "Local kind" is deliberately TWO independent checks, because a context
// NAME is cosmetic — anyone can name an AKS context `kind-prod`. The
// substantive check is the API-server address: kind publishes its API
// server on loopback. Both must agree.
//
// The kubeconfig itself is read by shelling out to `kubectl config view
// -o json` rather than parsing files here. That is not laziness: KUBECONFIG
// can name several files, kubectl's merge rules are load-bearing, and a
// second implementation of them would be a second set of bugs deciding
// whose cluster gets written to. `config view` never contacts a cluster, so
// it stays cheap and offline.
package guard

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/cliui"
	"golang.org/x/term"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
)

// Kubeconfig is the sliver of `kubectl config view -o json` this package
// needs. Everything else in a kubeconfig — users, credentials, extensions —
// is deliberately not modelled: the guard decides on names and addresses,
// and reading no further means it can never print or log a credential.
type Kubeconfig struct {
	// CurrentContext is never a SOURCE: kmx acts only on the name it
	// resolved, and where the two differ the current one is never
	// substituted. It is read for two things — naming it in a refusal, so
	// an operator who set it knows kmx did not follow it, and
	// corroborating an invented default that happens to match it, which
	// turns a refusal into a proceed. kmx does not follow it on purpose: a
	// bare kubectl does, and `az aks get-credentials` rewrites it silently.
	CurrentContext string `json:"current-context"`
	Clusters       []struct {
		Name    string `json:"name"`
		Cluster struct {
			Server string `json:"server"`
		} `json:"cluster"`
	} `json:"clusters"`
	Contexts []struct {
		Name    string `json:"name"`
		Context struct {
			Cluster string `json:"cluster"`
		} `json:"context"`
	} `json:"contexts"`
}

// Posture is the guard's classification of a context.
type Posture struct {
	// Context is the context name that was classified.
	Context string
	// Host is the API server's hostname, empty when the context is not in
	// the kubeconfig at all.
	Host string
	// Label is the human-readable posture printed in the banner.
	Label string
	// Local reports whether this is a genuinely local kind cluster, which
	// is the only case that proceeds without confirmation.
	Local bool
}

// LoadKubeconfig returns the merged kubeconfig as kubectl sees it.
func LoadKubeconfig(kubectlBin string) (*Kubeconfig, error) {
	if kubectlBin == "" {
		kubectlBin = "kubectl"
	}
	out, err := exec.Command(kubectlBin, "config", "view", "-o", "json").Output()
	if err != nil {
		return nil, fmt.Errorf("cannot read the kubeconfig — refusing to act blind: %w", err)
	}
	return ParseKubeconfig(out)
}

// ParseKubeconfig decodes `kubectl config view -o json` output.
func ParseKubeconfig(raw []byte) (*Kubeconfig, error) {
	var cfg Kubeconfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("cannot read the kubeconfig — refusing to act blind: %w", err)
	}
	return &cfg, nil
}

// server resolves a context name to its cluster's API-server URL. An empty
// string means the context is absent from the kubeconfig.
func (c *Kubeconfig) server(context string) string {
	clusterName := ""
	found := false
	for _, ctx := range c.Contexts {
		if ctx.Name == context {
			clusterName, found = ctx.Context.Cluster, true
			break
		}
	}
	if !found {
		return ""
	}
	for _, cl := range c.Clusters {
		if cl.Name == clusterName {
			return cl.Cluster.Server
		}
	}
	return ""
}

func isLoopback(host string) bool {
	switch host {
	case "127.0.0.1", "localhost", "::1", "0.0.0.0":
		return true
	}
	return false
}

// Classify applies the two-check "local kind" rule.
//
// Order matters, exactly as in the shell script: an ABSENT context is not
// automatically unsafe — `kmx up` on an empty machine legitimately names a
// kind context that does not exist yet, and CI depends on that. Absent +
// kind-named is "about to be created"; absent + anything else is a typo,
// which is precisely what this guard exists to catch.
func Classify(cfg *Kubeconfig, context string) (Posture, error) {
	if context == "" {
		return Posture{}, fmt.Errorf("KUBE_CTX is empty — refusing to act on an unnamed cluster")
	}
	namedKind := strings.HasPrefix(context, "kind-")
	server := cfg.server(context)

	host := ""
	if server != "" {
		if u, err := url.Parse(strings.TrimSpace(server)); err == nil {
			host = u.Hostname()
		}
	}

	switch {
	case server == "" && namedKind:
		return Posture{Context: context, Label: "local kind (context not created yet)", Local: true}, nil
	case server == "":
		return Posture{}, fmt.Errorf("context %q is not in the kubeconfig.\n"+
			"  Nothing was applied. Check the name with: kubectl config get-contexts\n"+
			"  (Only a kind-* context may be named before it exists — that is\n"+
			"   'kmx up' creating it. Any other name here is a typo.)", context)
	case namedKind && isLoopback(host):
		return Posture{Context: context, Host: host, Label: "local kind", Local: true}, nil
	default:
		return Posture{Context: context, Host: host, Label: "REMOTE / non-kind", Local: false}, nil
	}
}

// Request is one call on the guard.
type Request struct {
	// Action is the sentence printed after "about to:".
	Action string
	// Context is the kube context the action would land on.
	Context string
	// Source says who chose Context — a flag, a variable, `kmx ctx`, or
	// nobody. It is printed on every run and it decides one thing: a
	// context nobody chose is refused rather than acted on.
	Source string
	// Namespaces is the banner's namespace list.
	Namespaces string
	// Confirm is KAIMAHI_CONFIRM's value.
	Confirm string
	// Command is named back to the operator in the "to proceed" hints, so
	// the hint is the command they actually typed.
	Command string
	// MustBeKnown says this action needs the kubeconfig to actually
	// describe the cluster, so the "about to be created" allowance below
	// does not apply to it.
	//
	// That allowance exists for bring-up: `kmx up` names a kind context
	// before there is one, and treating that as local is what makes
	// one-command bring-up work on an empty machine. It is wrong for
	// anything that DESTROYS, because `kind delete cluster` deletes by
	// container name and never reads the kubeconfig at all — so a stale
	// KUBECONFIG turns "this context does not exist yet" into a real
	// cluster being deleted under a banner saying nothing is there. A
	// caller setting this gets the truthful label and the confirmation
	// path instead of a free pass.
	MustBeKnown bool
}

// Check prints the banner and either returns nil (proceed) or an error
// (refuse). It never returns nil without having printed where the action
// lands.
//
// in is the stream a confirmation would be typed on; a nil or
// non-character-device stream means nobody is there to answer, and the
// guard fails closed rather than hanging or assuming.
func Check(cfg *Kubeconfig, req Request, out io.Writer, in *os.File) error {
	posture, err := Classify(cfg, req.Context)
	if err != nil {
		return fmt.Errorf("kube-guard: %w", err)
	}

	// An absent kind-named context is "about to be created" only for
	// callers that create. For the others the honest statement is that
	// nothing here describes the cluster the command names, so nothing
	// can vouch for it being local — and it goes down the same path any
	// other unvouched-for context does.
	if req.MustBeKnown && posture.Local && posture.Host == "" {
		posture.Local = false
		posture.Label = "kind-named, but this kubeconfig does not describe it"
	}

	namespaces := req.Namespaces
	if namespaces == "" {
		namespaces = "kagent, kaimahi, ollama"
	}
	hostShown := posture.Host
	if hostShown == "" {
		hostShown = "<none yet>"
	}
	// A caller that does not say who chose the context is a caller whose
	// banner cannot distinguish a target an operator typed from one kmx
	// invented — which is the whole distinction this guard now turns on. Fail
	// closed rather than print "unrecorded" and carry on.
	if strings.TrimSpace(req.Source) == "" {
		return fmt.Errorf("kube-guard: this command did not record who chose context %q — refusing.\n"+
			"  Nothing was applied. This is a bug in kmx, not something you can set.", req.Context)
	}
	source := req.Source
	ui := cliui.New(out)
	if ui.Rich() {
		fmt.Fprintln(out, ui.Callout(cliui.CalloutWarning, "Target confirmation", []cliui.Field{
			{Label: "about to", Value: req.Action}, {Label: "context", Value: posture.Context},
			{Label: "chosen by", Value: source}, {Label: "server", Value: hostShown},
			{Label: "namespace(s)", Value: namespaces}, {Label: "posture", Value: posture.Label},
		}))
	} else {
		fmt.Fprintf(out, "----------------------------------------------------------------\n"+
			"  about to: %s\n"+
			"  context:  %s\n"+
			"  chosen by: %s\n"+
			"  server:   %s\n"+
			"  namespace(s): %s\n"+
			"  posture:  %s\n"+
			"----------------------------------------------------------------\n",
			req.Action, posture.Context, source, hostShown, namespaces, posture.Label)
	}

	// Nobody chose this cluster. Two cases are not the failure, and the rule
	// has to let them through or it is friction rather than a safety net:
	//
	//   - A machine with no clusters at all. There is nothing to confuse the
	//     made-up kind name with and it is the only sensible target, which is
	//     what keeps one-command bring-up working on an empty machine.
	//   - The made-up name is EXACTLY the context the operator is pointed at.
	//     Nobody is being surprised: kmx would act on the same cluster their
	//     own kubectl would. This is not following current-context — kmx acts
	//     only on the name it resolved, and where that name and the current
	//     context DIFFER the current one is never substituted for it. The
	//     match is corroboration, not a source.
	//
	// What is left is the failure: an invented name sitting beside clusters it
	// is not, with nothing having chosen between them. The banner is the only
	// thing standing between an operator and the wrong cluster, and a banner
	// naming a cluster nobody picked is not a safety net.
	if req.Source == config.SourceDefault && len(cfg.Contexts) > 0 &&
		strings.TrimSpace(cfg.CurrentContext) != req.Context {
		return fmt.Errorf("kube-guard: nothing chose a cluster, so kmx will not act on one.\n"+
			"  It would have used %q, which is a name kmx made up, and your kubeconfig\n"+
			"  holds %d context(s) it could have meant instead.%s\n"+
			"  Nothing was applied. Choose one, and it is remembered:\n"+
			"    kmx ctx <name>            # kubectl config get-contexts lists them\n"+
			"    kmx --context <name> ...  # or just this once",
			req.Context, len(cfg.Contexts), currentContextNote(cfg.CurrentContext))
	}

	if posture.Local {
		return nil
	}

	confirm := req.Context
	if confirm == "" || strings.IndexFunc(confirm, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_@%+=:,./-", r))
	}) >= 0 {
		confirm = "'" + strings.ReplaceAll(confirm, "'", "'\"'\"'") + "'"
	}
	proceed := fmt.Sprintf("  to proceed:  KAIMAHI_CONFIRM=%s %s", confirm, req.Command)

	// Remote: explicit confirmation naming the context, or nothing happens.
	if req.Confirm != "" {
		if req.Confirm == req.Context {
			fmt.Fprintln(out, "kube-guard: confirmed via KAIMAHI_CONFIRM.")
			return nil
		}
		return fmt.Errorf("kube-guard: KAIMAHI_CONFIRM does not name this context — refusing.\n%s", proceed)
	}

	// No pre-confirmation. Prompt only if a human is actually there to
	// answer; a script or CI job reaching here must fail rather than hang.
	if !isTerminal(in) {
		return fmt.Errorf("kube-guard: %s, and there is no TTY to ask.\n%s",
			unvouched(req, posture), proceed)
	}

	fmt.Fprint(out, ui.Warning("Type the context name to continue (anything else aborts): "))
	answer := readLine(in)
	if answer != req.Context {
		return fmt.Errorf("kube-guard: not confirmed — nothing was applied")
	}
	fmt.Fprintln(out, "kube-guard: confirmed.")
	return nil
}

// unvouched says why a context did not proceed on its own, in the words
// that fit the case. "Not a local kind cluster" is true of a production
// context and misleading about a cluster whose only problem is that this
// kubeconfig has never heard of it — and the second is the case where an
// operator most needs to be told what kmx actually knows.
func unvouched(req Request, posture Posture) string {
	if req.MustBeKnown && posture.Host == "" {
		return fmt.Sprintf("nothing in this kubeconfig describes %q, so kmx cannot tell whether it is\n"+
			"  the local cluster you mean or another one with the same name", req.Context)
	}
	return fmt.Sprintf("%q is not a local kind cluster", req.Context)
}

// currentContextNote names the operator's current context without following
// it. Saying nothing here would leave the most likely intended answer off a
// screen that exists to name the target.
func currentContextNote(current string) string {
	if strings.TrimSpace(current) == "" {
		return "\n  Your kubeconfig has no current context set either."
	}
	return fmt.Sprintf("\n  Your current context is %q; kmx does not follow it, because a tool that\n"+
		"  rewrites it (`az aks get-credentials` does) would silently re-aim kmx.", current)
}

func isTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

// readLine reads one line without pulling in bufio's buffering, which would
// swallow bytes a subsequently-spawned command might want. Confirmations are
// short; a byte at a time is free here.
func readLine(f *os.File) string {
	var b []byte
	buf := make([]byte, 1)
	for {
		n, err := f.Read(buf)
		if n > 0 {
			if buf[0] == '\n' {
				break
			}
			b = append(b, buf[0])
		}
		if err != nil {
			break
		}
		if len(b) > 4096 {
			break
		}
	}
	return strings.TrimRight(string(b), "\r")
}
