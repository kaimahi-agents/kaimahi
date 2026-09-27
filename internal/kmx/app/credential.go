package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/admin"
)

// errCredentialNotBound marks the case where the plane already holds a
// credential of this name and the Secret that should carry its token is
// absent. The token is shown once and cannot be recovered, so the
// situation is unrecoverable either way — but WHY it happened decides
// what to do about it, and only the caller knows whether the name could
// belong to something else.
var errCredentialNotBound = errors.New("credential exists and its Secret is not bound")

// CredentialOptions names the destination of an issued credential: which
// Secret carries the one-time token, in which namespace, for how long, and
// which command an operator would re-run if a refusal below names one.
//
// It is shared: `kmx credential issue`, `kmx migrate` and the transitional
// lift governance phase all mint credentials through the same pre-POST
// binding checks and the same 409 reconciliation, because the property being
// protected — a token that exists exactly once — is the same in all three.
type CredentialOptions struct {
	// Secret is the agent-side Secret the issued token is stored in.
	Secret string
	// SecretNamespace is where that Secret lives. It is never defaulted for
	// the caller: a credential silently issued into somebody else's
	// namespace is not a convenience.
	SecretNamespace string
	// TTLSeconds, when set, is the issued credential's lifetime. Nil takes
	// the plane's default; there is no way to ask for "never", because a
	// credential with no expiry is the closed legacy class.
	TTLSeconds *int64
	// Command names the command an operator would re-run with a different
	// --secret, so a refusal names what they actually typed.
	Command string
}

// issueCredential mints the credential and stores its token as the
// agent-side Secret, reconciling the already-issued case without exposing the
// one-time token.
//
// The token is shown EXACTLY ONCE, at issue time, and cannot be recovered.
// That is what makes both the check before the POST and the 409 branch below
// more than politeness.
func (a *App) issueCredential(client *admin.Client, credential string, opt CredentialOptions) error {
	// Whose token is in that Secret? Asked BEFORE issuing, because the
	// answer can forbid the whole operation: issuing `demo` while the
	// Secret holds hello-world's token would otherwise mint demo's
	// credential, overwrite the Secret, and destroy the only copy of
	// hello-world's token — leaving a live credential nothing can use. The
	// 409 branch refuses exactly this once the credential already exists;
	// the first issue of a SECOND name has to refuse it too, and refusing
	// before the POST also avoids leaving an orphan credential row behind.
	bound, secretExists, err := a.secretBinding(opt)
	if err != nil {
		return err
	}
	if secretExists && bound == "" {
		return fmt.Errorf("Secret %s/%s already exists without a kaimahi.dev/credential binding; refusing to overwrite it",
			opt.SecretNamespace, opt.Secret)
	}
	if bound != "" && bound != credential {
		return a.wrongCredentialError(bound, credential, opt)
	}

	issue := map[string]any{"name": credential}
	if opt.TTLSeconds != nil {
		issue["ttl_seconds"] = *opt.TTLSeconds
	}
	status, body, err := client.Do(http.MethodPost, "/admin/credentials", issue)
	if err != nil {
		return err
	}

	if status == http.StatusConflict {
		return a.reconcileExistingCredential(credential, opt)
	}
	if status != http.StatusCreated {
		// A broken or incompatible plane could include a bearer in an error
		// response. Never copy credential endpoint bodies into operator output.
		return fmt.Errorf("issuing credential %q failed (HTTP %d)", credential, status)
	}

	token, err := admin.TokenFrom(body)
	if err != nil {
		return err
	}
	// Straight from the reply into the manifest into kubectl's stdin. The
	// token is in this process's memory and in the cluster, and nowhere
	// else: not argv, not the environment, not a file, not a log.
	annotations := map[string]string{
		"kaimahi.dev/credential":       credential,
		"app.kubernetes.io/managed-by": "kmx",
	}
	manifest := secretManifest(opt.Secret, opt.SecretNamespace,
		map[string]string{"api-key": token}, annotations)
	quiet := *a.Run
	quiet.Echo = false
	// `apply`, always. Every caller is non-interactive, and a `create` that
	// loses to a Secret written since secretBinding read it would fail with
	// the one-time token already minted and nowhere to put it.
	fmt.Fprintf(a.Err, "kubectl --context %s -n %s apply -f - # (Secret %s, from the pipe)\n",
		a.Cfg.KubeContext, opt.SecretNamespace, opt.Secret)
	if err := quiet.RunStdin(manifest, "kubectl",
		a.kubectl("-n", opt.SecretNamespace, "apply", "-f", "-")...); err != nil {
		return err
	}
	a.notef("Governed credential %q issued; Secret %s/%s created.", credential, opt.SecretNamespace, opt.Secret)
	a.notef("The plane stores only its hash — the real upstream keys stay with the proxy.")
	if expires := admin.ExpiresFrom(body); expires != "" {
		// Said at issue time, not only when it bites: an operator who
		// never learns the deadline discovers it as an outage.
		a.notef("It expires %s — `kmx credentials` shows every deadline, `kmx credential renew %s` extends this one.",
			expires, credential)
	}
	return nil
}

// secretBinding returns whether the agent-side Secret exists and, if so, the
// credential its token is bound to.
//
// Only a genuine NotFound is "no Secret". Any other read failure aborts: an
// unreadable Secret answered as absent is how the overwrite this check
// exists to prevent would happen anyway.
func (a *App) secretBinding(opt CredentialOptions) (string, bool, error) {
	raw, err := a.kubectlCapture("-n", opt.SecretNamespace, "get", "secret", opt.Secret, "-o", "json")
	if err != nil {
		if isNotFound(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("cannot read Secret %s to tell whose token it holds (refusing to overwrite it blind): %w",
			opt.Secret, err)
	}
	var secret struct {
		Metadata struct {
			Annotations map[string]string `json:"annotations"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal([]byte(raw), &secret); err != nil {
		return "", false, fmt.Errorf("cannot decode Secret %s to tell whose token it holds (refusing to overwrite it blind)", opt.Secret)
	}
	return strings.TrimSpace(secret.Metadata.Annotations["kaimahi.dev/credential"]), true, nil
}

// command is the invocation the refusals point back at.
func command(opt CredentialOptions, credential string) string {
	if opt.Command != "" {
		return opt.Command
	}
	return "kmx credential issue " + shellArg(credential)
}

func (a *App) wrongCredentialError(bound, credential string, opt CredentialOptions) error {
	return fmt.Errorf("Secret %s holds the token for credential %q, not %q — refusing.\n"+
		"  That token is the only copy; overwriting it would leave %q live in the plane and unusable.\n"+
		"  A different --secret also requires matching model references; %s cannot rewire them for you.\n"+
		"  Keep the existing credential, or issue this one into a Secret of its own.",
		opt.Secret, bound, credential, bound, command(opt, credential))
}

// reconcileExistingCredential decides what an HTTP 409 means, given what the
// agent-side Secret is bound to.
func (a *App) reconcileExistingCredential(credential string, opt CredentialOptions) error {
	bound, _, err := a.secretBinding(opt)
	if err != nil {
		return err
	}
	switch bound {
	case credential:
		a.notef("Credential %q already issued and %s is bound to it; keeping both.", credential, opt.Secret)
		return nil
	case "":
		// Wrapped so a caller that knows a SECOND reason this can happen
		// can say so. The recovery below is right when the Secret was
		// lost; it is dangerous when the name simply belongs to somebody
		// else's workload, because the row it deletes is theirs.
		return fmt.Errorf("%w: credential %q exists in the plane but Secret %s is missing (or unlabeled).\n"+
			"  The token is shown exactly once at issue time and cannot be recovered;\n"+
			"  delete the row and re-run:\n"+
			"    kubectl --context %s -n %s exec deploy/kaimahi-postgres -- \\\n"+
			"      psql -U kaimahi -c \"DELETE FROM credential WHERE name='%s'\"",
			errCredentialNotBound, credential, opt.Secret, a.Cfg.KubeContext, admin.Namespace, credential)
	default:
		return a.wrongCredentialError(bound, credential, opt)
	}
}

// validCredentialName checks names before they are interpolated into JSON and
// query strings. The plane validates again.
func validCredentialName(name string) error {
	if name == "" {
		return fmt.Errorf("a credential name is required")
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return fmt.Errorf("invalid credential name %q (want [a-z0-9-]+)", name)
		}
	}
	return nil
}
