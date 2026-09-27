package app

import (
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/admin"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/seamcert"
)

// planeWorkload is the Deployment k8s/plane/proxy.yaml creates. Restarting
// it is how a freshly signed certificate reaches the process serving it: the
// mounted Secret updates in place, but the proxy reads it once at startup.
const planeWorkload = "kaimahi-proxy"

// planeCertificate mints, renews and publishes the certificate the plane's
// model seam serves with.
//
// The shape, and why it is three Secrets rather than one:
//
//   - The AUTHORITY (certificate and private key) is created once and kept.
//     Keeping it is what makes renewal a re-sign under an unchanged
//     authority: the CA workloads are told to trust never has to change, so
//     there is no window where one side has rolled over and the other has
//     not. It is mounted into no pod, including the proxy's.
//   - The SERVING material (certificate, key, and the authority's
//     certificate) is what the proxy mounts. It can sign a handshake and
//     cannot issue anything.
//   - The AUTHORITY'S CERTIFICATE ALONE is copied into a workload's
//     namespace, where its model client verifies the seam against it. It
//     says who to trust and confers nothing, so it is the one piece that
//     leaves the plane's namespace.
//
// It publishes NOWHERE on its own. It used to copy the authority into the
// retired runtime's namespace on every run, for agents this repository
// installed there; that runtime is gone, and no owner-managed workload lives at a namespace
// kmx could guess. Publication is now the job of the command that points a
// workload at the seam and is therefore told which namespace that is —
// `kmx migrate`, through publishPlaneAuthority.
//
// singleStep says whether this ran as `kmx plane --step certificate`. A full
// `kmx plane` ends in a rollout that picks up a new certificate anyway;
// asked for on its own, this has to do that restart itself, or it would
// report a renewal that the running plane is not serving.
func (a *App) planeCertificate(singleStep bool) error {
	authority, err := a.ensureAuthority()
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

	if singleStep && decision.Sign {
		return a.restartForCertificate()
	}
	return nil
}

// ensureAuthority reads the authority, or mints one if there is none.
//
// Create, not apply, on the write: a second authority generated under a live
// plane would leave every workload verifying against a CA that no longer signs
// anything, and the losing side of a race must fail rather than overwrite.
func (a *App) ensureAuthority() (seamcert.Authority, error) {
	data, err := a.secretData(admin.Namespace, config.PlaneAuthoritySecret)
	switch {
	case err == nil:
		authority, err := seamcert.LoadAuthority(data["ca.crt"], data["ca.key"])
		if err != nil {
			return seamcert.Authority{}, fmt.Errorf(
				"Secret %s/%s does not hold a usable certificate authority: %w\n"+
					"  Deleting it makes the next `kmx plane` mint a new one — every workload's\n"+
					"  trust then changes with it, and each has to be re-pointed at the seam.",
				admin.Namespace, config.PlaneAuthoritySecret, err)
		}
		return authority, nil
	case !isNotFound(err):
		// Not collapsed with NotFound: an unreachable API server or an RBAC
		// denial answered as "absent" would mint a SECOND authority under a
		// plane whose workloads verify against the first.
		return seamcert.Authority{}, fmt.Errorf(
			"cannot tell whether the seam authority exists (refusing to mint a second one): %w", err)
	}

	authority, err := seamcert.MintAuthority(a.timeNow())
	if err != nil {
		return seamcert.Authority{}, err
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
		return seamcert.Authority{}, err
	}
	a.notef("Seam certificate authority minted; its private key stays in Secret %s/%s and is mounted nowhere.",
		admin.Namespace, config.PlaneAuthoritySecret)
	return authority, nil
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
// certificate — into the namespace a workload holding a credential runs in,
// which is the only namespace that has to verify the seam.
//
// Tolerant of a missing namespace, and deliberately so: the operator may be
// naming a namespace they have not created yet, and refusing over the
// absence of the thing that will be told to trust the plane would be the
// wrong end of the problem. The note names the command that publishes it
// once the namespace exists.
func (a *App) publishAuthority(namespace string, certPEM []byte) error {
	// The namespace is checked FIRST rather than inferred from a failed
	// apply. `kubectl apply -f -` is run through a pipe, so its error
	// reaches Go as a bare exit status with the message on the child's
	// stderr — isNotFound has nothing to read, every failure looks alike,
	// and the tolerated case is indistinguishable from a real one.
	if _, err := a.kubectlCapture("get", "namespace", namespace, "-o", "name"); err != nil {
		if isNotFound(err) {
			a.notef("NOTE: namespace %s does not exist yet, so nothing was told what to trust.\n"+
				"  Nothing publishes Secret %s into a namespace that does not exist. Create it, then\n"+
				"  re-run %s.",
				namespace, config.PlaneCASecret, authorityPublisher(namespace))
			return nil
		}
		return fmt.Errorf("cannot tell whether namespace %s exists (refusing to guess): %w",
			namespace, err)
	}
	body := secretManifest(config.PlaneCASecret, namespace,
		map[string]string{config.PlaneCAKey: string(certPEM)}, nil)
	return a.applySecretIn(namespace, body, config.PlaneCASecret)
}

// authorityPublisher names the command that publishes the plane's authority
// into namespace.
//
// There is one, and that is the whole shape of it now: no command
// republishes into a namespace on its own, because nothing left in kmx knows
// which namespaces want the authority. A namespace is reached only by the
// command that points a workload there at the seam, so that is what has to
// be re-run.
func authorityPublisher(namespace string) string {
	return "`kmx migrate <deployment> --namespace " + namespace + " --model <provider>/<model>`"
}

// publishPlaneAuthority republishes the authority's certificate into a
// workload's namespace from what the plane is currently serving with.
//
// Called by every command that points something at a seam. A model client
// that cannot verify the seam fails at request time, in a way that reads as
// a broken plane rather than as a missing trust anchor.
//
// Refuses rather than warning when the plane has no certificate: pointing a
// workload at a TLS seam it cannot verify is not a partial success.
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
