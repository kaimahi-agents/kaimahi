package app

import (
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
// two data seams serve with.
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
		if err := a.applySecret(body, config.PlaneSeamTLSSecret); err != nil {
			return err
		}
	}

	// The authority's certificate goes to the agent namespace on EVERY run,
	// not only when something was signed. It is how a cluster repairs
	// itself after the Secret was deleted, and it is what makes a
	// re-minted authority reach the agents that have to trust it.
	if err := a.publishAuthority(authority.CertPEM); err != nil {
		return err
	}
	if authorityIsNew && serving != nil {
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
// certificate — into the agent namespace, where `spec.tls.caCertSecretRef`
// names it.
//
// Tolerant of a missing namespace, and deliberately so: `kmx plane` can be
// run against a cluster where kagent is not installed yet, and refusing the
// whole deploy over the absence of the thing that will be told to trust it
// would be the wrong end of the problem. `kmx govern` publishes it again
// before it needs it.
func (a *App) publishAuthority(certPEM []byte) error {
	body := secretManifest(config.PlaneCASecret, config_kagentNamespace,
		map[string]string{config.PlaneCAKey: string(certPEM)}, nil)
	if err := a.applySecretIn(config_kagentNamespace, body, config.PlaneCASecret); err != nil {
		if isNotFound(err) {
			a.notef("NOTE: namespace %s does not exist yet, so nothing was told what to trust.\n"+
				"  `kmx govern` publishes Secret %s there before it points an agent at the plane.",
				config_kagentNamespace, config.PlaneCASecret)
			return nil
		}
		return err
	}
	return nil
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
func (a *App) publishPlaneAuthority() error {
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
	return a.publishAuthority(ca)
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

func (a *App) applySecret(body []byte, name string) error {
	return a.applySecretIn(admin.Namespace, body, name)
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

// certificateFailure recognises kagent reporting that it could not verify the
// plane's certificate, and answers with the certificate rather than with the
// connection.
//
// This exists because of what the other side does NOT say. kagent's agent
// runtime surfaces a failed handshake as a generic connection error — the
// helpful, certificate-naming diagnostic in its own source is never called on
// that path — so an operator meeting a wrong or expired certificate is told
// only that something could not connect. That is the failure this lane was
// warned about by name. kmx cannot fix the agent's message, but it can refuse
// to repeat it: whenever a seam verdict carries one of these, the certificate
// is named alongside.
// certificateNote turns a refusal that is about trust into one that names the
// certificate, and adds nothing to a refusal that is about anything else.
//
// It reads the certificate rather than describing the problem in the
// abstract, because the two questions an operator has at this moment are "is
// this certificate the one my agents were told to trust" and "has it
// expired", and both are answered by the line below.
func (a *App) certificateNote(message string) string {
	if !certificateFailure(message) {
		return ""
	}
	return fmt.Sprintf("\n  This is a TRUST failure, not an unreachable seam. The plane is serving:\n"+
		"    %s\n"+
		"  Agents verify it against Secret %s/%s. `kmx plane --step certificate` re-signs it\n"+
		"  and republishes the authority.",
		a.seamCertificate().Line, config_kagentNamespace, config.PlaneCASecret)
}

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
