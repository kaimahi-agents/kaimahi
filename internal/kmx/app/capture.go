package app

import (
	"encoding/base64"
	"fmt"
	"os"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/admin"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/seam"
	"golang.org/x/term"
)

// Capturing an upstream's credential: the one path on which kmx accepts
// credential material, and the fence around it.
//
// Everything else in kmx refuses a credential — the agent wizard screens its
// input against credential shapes and the blueprint parser refuses
// credential-shaped keys before a document is decoded — and this changes none
// of that. It exists because the alternative was worse: installing kmx,
// standing up a cluster, deploying the plane and governing an agent all work
// with no checkout, and then handing the thing a token required cloning a
// repository to run a make target. The clone-free journey ended one step
// before it was useful.
//
// The fence, and none of it is negotiable:
//
//   - TERMINAL ONLY. The value is read from a TTY and from nowhere else.
//     There is no flag, no environment variable, no file, and a pipe or a
//     redirect is refused rather than read. A token that can arrive from a
//     pipe can arrive from a shell history or a CI log.
//   - NO ECHO. The terminal's echo is off while it is typed and the value is
//     never printed back.
//   - THE VALUE TRAVELS TO TWO PLACES AND NO OTHERS: the upstream, in an
//     Authorization header, because a capture that stored an unchecked value
//     would be worse than the make target it replaced; and the Secret. It is
//     not logged, not written to a temporary file, not printed back, and not
//     passed as an argument to kubectl — `--from-literal` would put it in the
//     process table, so the Secret is rendered in memory and piped to
//     `kubectl apply -f -`, and the buffers holding it are zeroed as soon as
//     the write returns.
//
// The order of the steps is part of the fence too. The terminal check comes
// first, so a piped invocation refuses before touching a cluster. The context
// guard comes next, so the operator sees WHICH cluster is about to hold their
// credential before they type it. Only then is anything read.

// CaptureOptions is one credential capture.
type CaptureOptions struct {
	// Seam names the upstream (and so the Secret the gateway reads).
	Seam string
	// Subject is the one thing that upstream is scoped to: a repository, an
	// organization.
	Subject string
	// Replace allows overwriting a credential that is already stored. It is
	// off by default because overwriting silently is how someone loses a
	// working credential.
	Replace bool
}

// CaptureCredential reads an upstream credential from the terminal, proves
// what can be proven about it, and stores it in plane custody.
func (a *App) CaptureCredential(opt CaptureOptions) error {
	s, err := seam.Lookup(opt.Seam)
	if err != nil {
		return err
	}
	if err := s.CheckSubject(opt.Subject); err != nil {
		return err
	}
	if err := a.requireTerminalForCredential(); err != nil {
		return err
	}
	args := []string{"credential", "capture", s.Name, opt.Subject}
	if opt.Replace {
		args = append(args, "--replace")
	}
	command := a.operationCommand(args...)
	if err := a.Guard(fmt.Sprintf("store the %s credential as Secret %s/%s", s.Name, admin.Namespace, s.Secret),
		command); err != nil {
		return err
	}
	if err := a.refuseStoredCredential(s, opt); err != nil {
		return err
	}

	fmt.Fprintf(a.Err, "\n%s\n\n  %s\n\n", s.Prompt, s.Guidance)
	token, err := a.readCredential()
	if err != nil {
		return err
	}
	defer zeroBytes(token)
	if len(token) == 0 {
		return fmt.Errorf("nothing was typed — nothing was stored")
	}

	a.notef("Checking it with %s before storing anything...", s.Name)
	report, err := s.Validate(a.seamEnvironment(), opt.Subject, token)
	if err != nil {
		return fmt.Errorf("REFUSING: %w", err)
	}
	if err := a.storeCredential(s, token); err != nil {
		return err
	}
	// The write is done, so the plaintext has no further use. Everything
	// below is reporting and cluster work.
	zeroBytes(token)

	a.reportCapture(s, report)
	return a.enableHostedUpstream(s)
}

// requireTerminalForCredential is the fence's first rule, and the reason it
// is first: a refusal here has read nothing and reached no cluster.
//
// Both streams are checked, the same way the agent wizard checks them: the
// prompt is written to stderr and the value is read from stdin, so a
// redirected either way is not the interactive session this path requires.
func (a *App) requireTerminalForCredential() error {
	if a.readSecret != nil {
		return nil
	}
	errFile, visible := a.Err.(*os.File)
	if a.Stdin == nil || !visible || !term.IsTerminal(int(a.Stdin.Fd())) || !term.IsTerminal(int(errFile.Fd())) {
		return fmt.Errorf("kmx reads a credential from a terminal, and this is not one.\n" +
			"  Nothing was read and nothing was stored. There is deliberately no flag,\n" +
			"  environment variable or file that will take the value instead: a token that\n" +
			"  can arrive through a pipe can arrive from a shell history or a CI log.\n" +
			"  Run this from a terminal and paste the credential at the prompt.")
	}
	return nil
}

// readCredential reads one line with the terminal's echo off.
//
// term.ReadPassword turns echo off for the duration and restores the terminal
// afterwards, including when the read fails. The trailing newline the
// operator typed is not echoed either, so one is printed here — otherwise the
// next line of output lands on the prompt.
func (a *App) readCredential() ([]byte, error) {
	if a.readSecret != nil {
		return a.readSecret()
	}
	value, err := term.ReadPassword(int(a.Stdin.Fd()))
	fmt.Fprintln(a.Err)
	if err != nil {
		return nil, fmt.Errorf("could not read the credential: %w", err)
	}
	return trimSpaceBytes(value), nil
}

// refuseStoredCredential decides what happens when there is already a
// credential for this seam.
//
// It refuses. Overwriting silently is how somebody replaces a working
// credential with a broken one and finds out during the thing they were about
// to do; the destructive direction is spelled out instead. It is not a wall —
// --replace is one word — and rotation is a real and frequent need, most of
// all for the Azure DevOps token, which lives about an hour.
func (a *App) refuseStoredCredential(s *seam.Seam, opt CaptureOptions) error {
	created, err := a.kubectlCapture("-n", admin.Namespace, "get", "secret", s.Secret,
		"-o", "jsonpath={.metadata.creationTimestamp}")
	switch {
	case err == nil && !opt.Replace:
		return fmt.Errorf("Secret %s/%s already holds a %s credential%s.\n"+
			"  Nothing was read and nothing was stored. Replacing it is the destructive\n"+
			"  direction, so it is said out loud:\n"+
			"    kmx credential capture %s %s --replace",
			admin.Namespace, s.Secret, s.Name, storedSince(created), s.Name, opt.Subject)
	case err == nil:
		a.notef("Secret %s/%s already exists%s and --replace was given: it will be overwritten.",
			admin.Namespace, s.Secret, storedSince(created))
	case !isNotFound(err):
		// Deliberately not collapsed with NotFound: an unreachable API
		// server, an expired kubeconfig credential or an RBAC denial read as
		// "there is nothing there" would overwrite a working credential while
		// reporting a first capture.
		return fmt.Errorf("cannot tell whether Secret %s/%s already exists, so nothing was read: %w",
			admin.Namespace, s.Secret, err)
	}
	return nil
}

func storedSince(timestamp string) string {
	timestamp = strings.TrimSpace(timestamp)
	if timestamp == "" {
		return ""
	}
	return " (stored " + timestamp + ")"
}

// storeCredential renders the Secret in memory and pipes it to kubectl.
//
// Every other way of doing this leaks. `--from-literal` puts the value in the
// process table, where any user on the machine can read it for as long as the
// command runs. `--from-file` needs a path, which means the plaintext lands
// on a disk — which is why the shell scripts this replaces had to write a
// 0600 file first. Rendering the document here and handing it to kubectl on
// stdin means the value exists in this process's memory and in the API
// server's response to it, and nowhere in between.
func (a *App) storeCredential(s *seam.Seam, token []byte) error {
	if err := a.apply("plane/namespace.yaml"); err != nil {
		return err
	}
	return a.storeCredentialValue(s.Secret, admin.Namespace, s.Key, token)
}

func (a *App) storeCredentialValue(name, namespace, key string, token []byte) error {
	body := credentialSecretManifest(name, namespace, key, token)
	defer zeroBytes(body)
	// Echo off for this one command. Every other kmx command prints itself so
	// an operator can copy it off the screen; this one's stdin is a
	// credential, and a copyable line would invite reconstructing it.
	quiet := *a.Run
	quiet.Echo = false
	fmt.Fprintf(a.Err, "kubectl --context %s -n %s apply -f - # (Secret %s, key %s, credential on stdin)\n",
		a.Cfg.KubeContext, namespace, name, key)
	if err := quiet.RunStdin(body, "kubectl", a.kubectl("-n", namespace, "apply", "-f", "-")...); err != nil {
		return err
	}
	a.notef("Secret %s/%s stored.", namespace, name)
	return nil
}

// credentialSecretManifest renders an Opaque Secret carrying one value.
//
// It is the byte-oriented sibling of secretManifest: the value never becomes
// a Go string, because a string cannot be overwritten and would sit in this
// process's heap until the garbage collector happened to reuse the page. The
// caller zeroes the returned buffer once kubectl has taken it.
func credentialSecretManifest(name, namespace, key string, value []byte) []byte {
	encoded := make([]byte, base64.StdEncoding.EncodedLen(len(value)))
	base64.StdEncoding.Encode(encoded, value)
	defer zeroBytes(encoded)

	var body []byte
	body = append(body, "apiVersion: v1\nkind: Secret\nmetadata:\n"...)
	body = append(body, "  name: "+name+"\n  namespace: "+namespace+"\ntype: Opaque\ndata:\n  "+key+": "...)
	body = append(body, encoded...)
	body = append(body, '\n')
	return body
}

// reportCapture says what was proven, what was not, and when the credential
// dies. The unproven half is not a footnote: a silence there would read as a
// guarantee this path cannot make.
func (a *App) reportCapture(s *seam.Seam, report seam.Report) {
	fmt.Fprintln(a.Err)
	for _, line := range report.Proven {
		a.notef("PROVEN: %s", line)
	}
	if report.Deadline != "" {
		a.notef("DEADLINE: %s", report.Deadline)
	}
	for _, line := range report.Unproven {
		a.notef("NOT PROVEN: %s", line)
	}
	a.notef("\nThe gateway injects this credential on calls to the %s upstream from plane\n"+
		"custody; the agent never holds it.", s.Name)
}

// enableHostedUpstream opens the gateway's way out to the internet and rolls
// the proxy, which is the rest of what capturing a hosted credential means.
//
// The allowance: the plane's own boundary lets the proxy reach nothing
// outside the cluster, so a stored credential for a hosted upstream is a
// credential that cannot be used. `make github-revoke` and
// `make release-revoke` close it again, with the credential.
//
// The roll: the Secret is an OPTIONAL mount, so a proxy that started without
// it does not see the file until the kubelet projects it on its own sync
// period. Rolling means both replicas start with the credential present
// rather than discovering it minutes later.
func (a *App) enableHostedUpstream(s *seam.Seam) error {
	if !s.HostedEgress {
		return nil
	}
	if err := a.apply("egress-hosted.yaml"); err != nil {
		return err
	}
	_, err := a.kubectlCapture("-n", admin.Namespace, "get", "deploy/kaimahi-proxy", "-o", "name")
	if err != nil {
		if !strings.Contains(err.Error(), "Error from server (NotFound):") {
			return fmt.Errorf("credential is stored, but cannot tell whether the proxy is deployed; restart was not performed: %w", err)
		}
		a.notef("The plane is not deployed here yet, so there is nothing to restart.\n" +
			"The credential is stored and `kmx plane` will start with it:\n" +
			"  kmx plane")
		return nil
	}
	if err := a.kubectlRun("-n", admin.Namespace, "rollout", "restart", "deploy/kaimahi-proxy"); err != nil {
		return err
	}
	return a.kubectlRun("-n", admin.Namespace, "rollout", "status", "deploy/kaimahi-proxy", "--timeout=300s")
}

// seamEnvironment is where the validators reach. Tests point it at a local
// server: this project's CI holds no credential and never will, so every
// proof about a refusal has to be reproducible without one.
func (a *App) seamEnvironment() seam.Env {
	if a.seamEnv != nil {
		return *a.seamEnv
	}
	return seam.DefaultEnv()
}

// zeroBytes overwrites a buffer that held credential material. It is not a
// guarantee — Go's runtime may have copied a slice while growing it — but it
// removes the copy this code is responsible for, promptly, rather than
// leaving it for the garbage collector.
func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// trimSpaceBytes trims surrounding whitespace in place: a pasted credential
// commonly arrives with a stray space or a carriage return, and neither is
// part of the value.
func trimSpaceBytes(b []byte) []byte {
	start, end := 0, len(b)
	for start < end && isSpaceByte(b[start]) {
		start++
	}
	for end > start && isSpaceByte(b[end-1]) {
		end--
	}
	return b[start:end]
}

func isSpaceByte(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == '\v' || c == '\f'
}
