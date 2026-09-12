package app

import (
	"bytes"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/admin"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/seamcert"
)

// planeCertificate mints, renews and publishes the certificate the plane's
// model seam serves with.
//
// The shape, and why it is three Secrets rather than one:
//
//   - The AUTHORITY (certificate and private key) is created once and kept.
//     Keeping it is what makes renewal a re-sign under an unchanged
//     authority: the CA agents are told to trust never has to change, so
//     there is no window where one side has rolled over and the other has
//     not. It is mounted into no pod, including the proxy's.
//   - The SERVING material (certificate, key, and the authority's
//     certificate) is what the proxy mounts. It can sign a handshake and
//     cannot issue anything.
//   - The AUTHORITY'S CERTIFICATE ALONE is copied into the agent namespace,
//     where a ModelConfig's and a RemoteMCPServer's `spec.tls` name it. It
//     says who to trust and confers nothing, so it is the one piece that
//     leaves the plane's namespace.
//
// singleStep says whether this ran as `kmx plane --step certificate`. A full
// `kmx plane` ends in a rollout that picks up a new certificate anyway;
// asked for on its own, this has to do that restart itself, or it would
// report a renewal that the running plane is not serving.
func (a *App) planeCertificate(singleStep bool) error {
	authority, authorityIsNew, err := a.ensureAuthority()
	if err != nil {
		return err
	}
	authorityCert, err := seamcert.ParseCertificate(authority.CertPEM)
	if err != nil {
		return err
	}

	serving, err := a.servingCertificate()
	if err != nil {
		return err
	}
	decision := seamcert.Decide(serving, authorityCert, a.timeNow())
	if !decision.Sign {
		a.notef("Seam certificate: %s", decision.Reason)
	} else {
		a.notef("Signing a seam certificate: %s", decision.Reason)
		material, err := authority.Sign(seamcert.SeamNames(), a.timeNow())
		if err != nil {
			return err
		}
		body := secretManifest(config.PlaneSeamTLSSecret, admin.Namespace, map[string]string{
			"tls.crt": string(material.CertPEM),
			"tls.key": string(material.KeyPEM),
			"ca.crt":  string(authority.CertPEM),
		}, nil)
		if err := a.applySecretIn(admin.Namespace, body, config.PlaneSeamTLSSecret); err != nil {
			return err
		}
	}

	// Asked BEFORE the publish below overwrites the answer. Whether agents
	// were trusting an OLDER authority is a fact about the AGENT namespace,
	// not about whether this namespace had a serving certificate. Gating on
	// the latter missed the case that matters most: the plane's namespace
	// deleted and recreated while governed agents stayed where they were,
	// which is exactly when every agent's trust anchor has just been
	// swapped underneath them.
	trustChanged := authorityIsNew && a.agentsTrustedAnotherAuthority(authority.CertPEM)

	// The authority's certificate goes to the agent namespace on EVERY run,
	// not only when something was signed. It is how a cluster repairs
	// itself after the Secret was deleted, and it is what makes a
	// re-minted authority reach the agents that have to trust it.
	if err := a.publishAuthority(config_kagentNamespace, authority.CertPEM); err != nil {
		return err
	}
	if trustChanged {
		a.notef("NOTE: the authority was re-minted, so every agent's trust changed with it.\n" +
			"  kagent rolls an agent when the CA Secret it names changes, so this propagates on its own —\n" +
			"  but calls made between the plane restarting and that rollout finishing fail closed.")
	}
	if singleStep && decision.Sign {
		return a.restartForCertificate()
	}
	return nil
}

// ensureAuthority reads the authority, or mints one if there is none.
//
// Create, not apply, on the write: a second authority generated under a live
// plane would leave every agent trusting a CA that no longer signs anything,
// and the losing side of a race must fail rather than overwrite.
func (a *App) ensureAuthority() (seamcert.Authority, bool, error) {
	data, err := a.secretData(admin.Namespace, config.PlaneAuthoritySecret)
	switch {
	case err == nil:
		authority, err := seamcert.LoadAuthority(data["ca.crt"], data["ca.key"])
		if err != nil {
			return seamcert.Authority{}, false, fmt.Errorf(
				"Secret %s/%s does not hold a usable certificate authority: %w\n"+
					"  Deleting it makes the next `kmx plane` mint a new one — every agent's\n"+
					"  trust then changes with it, which kagent rolls them for.",
				admin.Namespace, config.PlaneAuthoritySecret, err)
		}
		return authority, false, nil
	case !isNotFound(err):
		// Not collapsed with NotFound: an unreachable API server or an RBAC
		// denial answered as "absent" would mint a SECOND authority under a
		// plane whose agents trust the first.
		return seamcert.Authority{}, false, fmt.Errorf(
			"cannot tell whether the seam authority exists (refusing to mint a second one): %w", err)
	}

	authority, err := seamcert.MintAuthority(a.timeNow())
	if err != nil {
		return seamcert.Authority{}, false, err
	}
	body := secretManifest(config.PlaneAuthoritySecret, admin.Namespace, map[string]string{
		"ca.crt": string(authority.CertPEM),
		"ca.key": string(authority.KeyPEM),
	}, nil)
	quiet := *a.Run
	quiet.Echo = false
	fmt.Fprintf(a.Err, "kubectl --context %s -n %s create -f - # (seam certificate authority)\n",
		a.Cfg.KubeContext, admin.Namespace)
	if err := quiet.RunStdin(body, "kubectl", a.kubectl("-n", admin.Namespace, "create", "-f", "-")...); err != nil {
		return seamcert.Authority{}, false, err
	}
	a.notef("Seam certificate authority minted; its private key stays in Secret %s/%s and is mounted nowhere.",
		admin.Namespace, config.PlaneAuthoritySecret)
	return authority, true, nil
}

// servingCertificate returns what the plane is serving with now, or nil if
// there is nothing to keep.
//
// An unparseable certificate is nil rather than an error: whatever is in that
// Secret, it is not something to keep serving, and the answer is to sign
// another rather than to refuse to deploy.
func (a *App) servingCertificate() (*x509.Certificate, error) {
	data, err := a.secretData(admin.Namespace, config.PlaneSeamTLSSecret)
	switch {
	case isNotFound(err):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("cannot read the seam certificate: %w", err)
	}
	cert, err := seamcert.ParseCertificate(data["tls.crt"])
	if err != nil {
		return nil, nil
	}
	return cert, nil
}

// publishAuthority copies the authority's certificate — and only its
// certificate — into a namespace that has to verify a seam: the agent
// namespace, where `spec.tls.caCertSecretRef` names it, and the namespace a
// runtime holding a credential runs in, which on a cluster with no kagent
// is the only one there is.
//
// Tolerant of a missing namespace, and deliberately so: `kmx plane` can be
// run against a cluster where kagent is not installed yet, and refusing the
// whole deploy over the absence of the thing that will be told to trust it
// would be the wrong end of the problem. `kmx govern` publishes it again
// before it needs it.
func (a *App) publishAuthority(namespace string, certPEM []byte) error {
	// The namespace is checked FIRST rather than inferred from a failed
	// apply. `kubectl apply -f -` is run through a pipe, so its error
	// reaches Go as a bare exit status with the message on the child's
	// stderr — isNotFound has nothing to read, every failure looks alike,
	// and the tolerated case is indistinguishable from a real one. Reaching
	// for the message here made `kmx plane` fail outright on a cluster where
	// kagent is not installed, which is the exact case this is meant to
	// allow.
	if _, err := a.kubectlCapture("get", "namespace", namespace, "-o", "name"); err != nil {
		if isNotFound(err) {
			a.notef("NOTE: namespace %s does not exist yet, so nothing was told what to trust.\n"+
				"  `kmx govern` publishes Secret %s there before it points an agent at the plane.",
				namespace, config.PlaneCASecret)
			return nil
		}
		return fmt.Errorf("cannot tell whether namespace %s exists (refusing to guess): %w",
			namespace, err)
	}
	body := secretManifest(config.PlaneCASecret, namespace,
		map[string]string{config.PlaneCAKey: string(certPEM)}, nil)
	return a.applySecretIn(namespace, body, config.PlaneCASecret)
}

// publishPlaneAuthority republishes the authority's certificate into the
// agent namespace from what the plane is currently serving with.
//
// Called by every command that points something at a seam, not only by
// `kmx plane`. kagent refuses a ModelConfig or a RemoteMCPServer whose named
// CA Secret is absent or lacks the named key — it reports Accepted=false
// rather than switching — so an agent governed against a cluster where this
// Secret went missing would fail in a way that reads as a broken seam.
//
// Refuses rather than warning when the plane has no certificate: pointing an
// agent at a TLS seam it cannot verify is not a partial success.
func (a *App) publishPlaneAuthority(namespace string) error {
	data, err := a.secretData(admin.Namespace, config.PlaneSeamTLSSecret)
	if err != nil {
		if isNotFound(err) {
			return fmt.Errorf(
				"the plane has no seam certificate (Secret %s/%s is absent), so nothing can be told what to trust.\n"+
					"  Run `kmx plane` first — it mints the certificate and publishes the authority.",
				admin.Namespace, config.PlaneSeamTLSSecret)
		}
		return fmt.Errorf("cannot read the plane's seam certificate: %w", err)
	}
	ca := data[config.PlaneCAKey]
	if len(ca) == 0 {
		return fmt.Errorf(
			"Secret %s/%s carries no %s, so nothing can be told what to trust.\n"+
				"  Run `kmx plane` — it mints the certificate and publishes the authority.",
			admin.Namespace, config.PlaneSeamTLSSecret, config.PlaneCAKey)
	}
	return a.publishAuthority(namespace, ca)
}

// agentsTrustedAnotherAuthority reports whether the agent namespace already
// held a DIFFERENT authority than the one just minted.
//
// Read before it is overwritten, and answered conservatively: a namespace
// that cannot be read, or has no Secret yet, is not evidence of a swap, and
// warning about one that did not happen would train the reader to skip the
// line that matters.
func (a *App) agentsTrustedAnotherAuthority(certPEM []byte) bool {
	data, err := a.secretData(config_kagentNamespace, config.PlaneCASecret)
	if err != nil {
		return false
	}
	existing, ok := data[config.PlaneCAKey]
	return ok && len(existing) > 0 && !bytes.Equal(existing, certPEM)
}

// restartForCertificate rolls the proxy so it picks up a certificate this
// command just replaced. The mounted Secret updates in place, but the
// process reads it once at startup — without this, `kmx plane --step
// certificate` would report a renewal the plane is not serving.
func (a *App) restartForCertificate() error {
	if _, err := a.kubectlCapture("-n", admin.Namespace, "get", "deploy", planeWorkload, "-o", "name"); err != nil {
		if isNotFound(err) {
			a.notef("The plane is not deployed yet, so nothing was restarted; `kmx plane` will serve the new certificate.")
			return nil
		}
		return fmt.Errorf("cannot tell whether the plane is deployed: %w", err)
	}
	if err := a.kubectlRun("-n", admin.Namespace, "rollout", "restart", "deploy/"+planeWorkload); err != nil {
		return err
	}
	return a.kubectlRun("-n", admin.Namespace, "rollout", "status", "deploy/"+planeWorkload, "--timeout=300s")
}

// secretData reads one Secret's decoded values.
func (a *App) secretData(namespace, name string) (map[string][]byte, error) {
	raw, err := a.kubectlCapture("-n", namespace, "get", "secret", name, "-o", "json")
	if err != nil {
		return nil, err
	}
	var secret struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &secret); err != nil {
		return nil, fmt.Errorf("reading Secret %s/%s: %w", namespace, name, err)
	}
	out := make(map[string][]byte, len(secret.Data))
	for k, v := range secret.Data {
		decoded, err := base64.StdEncoding.DecodeString(v)
		if err != nil {
			return nil, fmt.Errorf("Secret %s/%s key %q is not base64: %w", namespace, name, k, err)
		}
		out[k] = decoded
	}
	return out, nil
}

func (a *App) applySecretIn(namespace string, body []byte, name string) error {
	fmt.Fprintf(a.Err, "kubectl --context %s -n %s apply -f - # (Secret %s)\n", a.Cfg.KubeContext, namespace, name)
	quiet := *a.Run
	quiet.Echo = false
	return quiet.RunStdin(body, "kubectl", a.kubectl("-n", namespace, "apply", "-f", "-")...)
}

// SeamCertificate is what `kmx status` reports about the seam certificate,
// and it is deliberately three states rather than two. "There is no
// certificate" and "the certificate could not be read" are different
// situations, and reporting the second as the first would send an operator
// to `kmx plane` when the problem is that nobody can read the Secret.
type SeamCertificate struct {
	// Line names the certificate and when it expires, or says why not.
	Line string `json:"line"`
	// State is valid, expiring, expired, none or unknown.
	State string `json:"state"`
}

// seamCertificate reads the certificate the plane is serving with, for
// `kmx status`. Never an error: status diagnoses a cluster and must not fail
// because the thing it is diagnosing is absent.
func (a *App) seamCertificate() SeamCertificate {
	data, err := a.secretData(admin.Namespace, config.PlaneSeamTLSSecret)
	switch {
	case isNotFound(err):
		return SeamCertificate{State: stateNone, Line: "none — the plane has not been deployed"}
	case err != nil:
		return SeamCertificate{State: stateUnknown, Line: "unknown — " + firstLine(err.Error())}
	}
	cert, parseErr := seamcert.ParseCertificate(data["tls.crt"])
	if parseErr != nil {
		return SeamCertificate{State: stateUnknown, Line: fmt.Sprintf(
			"unknown — Secret %s/%s holds no readable certificate (%s)",
			admin.Namespace, config.PlaneSeamTLSSecret, firstLine(parseErr.Error()))}
	}
	report := seamcert.Expiry(cert, a.timeNow())
	return SeamCertificate{State: report.State.String(), Line: report.Line()}
}

// certificateFailure recognises a refusal that is about TRUST rather than
// about reachability.
//
// It exists because of what the other side does not say. kagent's agent
// runtime surfaces a failed handshake as a generic connection error — the
// certificate-naming diagnostic in its own source is never called on that
// path — so an operator meeting a wrong or expired certificate is told only
// that something could not connect. kmx cannot fix that message, but it can
// decline to repeat it.
func certificateFailure(message string) bool {
	lower := strings.ToLower(message)
	for _, marker := range []string{
		"certificate", "x509", "tls", "ssl", "unknown authority", "handshake",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
