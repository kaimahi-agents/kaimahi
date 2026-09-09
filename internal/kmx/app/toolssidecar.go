package app

// `kmx tools sidecar` — a credential for a client that cannot set a header.
//
// The tool seam authenticates with a token in `Authorization`, which assumes
// the client can be told to send one. An MCP client whose only configuration
// is a URL cannot, and that alone is enough to stop an integration that needs
// nothing else: it is not about enforcement, only about addressing.
//
// The answer is an in-pod reverse proxy on loopback — the client posts to
// 127.0.0.1, the shim adds the header from the Secret the plane wrote and
// forwards to the gateway. kmx owns the mechanical half, the same division
// `kmx tools add` drew: the gateway URL, the Secret names, the authority to
// verify the seam against and the SSE settings are all values that fail
// silently or confusingly when they are wrong, and none of them is a policy
// choice. The operator owns the one thing that is theirs — whether a
// container is added to their workload.

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

// SidecarOptions is `kmx tools sidecar`'s surface. As everywhere in kmx,
// there is no flag here that can carry a credential: the generator emits
// Secret REFERENCES.
type SidecarOptions struct {
	// Upstream is the name in the gateway's table — what `kmx tools add`
	// onboarded the server under.
	Upstream string
	// Deployment is the workload running the MCP client.
	Deployment string
	// Namespace is where that workload runs. Defaults to the namespace
	// the tool credentials go to.
	Namespace string
	// Secret holds the kmh_ token. A NAME, never a value.
	Secret  string
	Out     string
	NoApply bool
}

// ToolsSidecar scaffolds the credential shim and applies the half that is
// kmx's to apply.
func (a *App) ToolsSidecar(opt SidecarOptions) error {
	if opt.Namespace == "" {
		opt.Namespace = config.DefaultNamespace
	}
	if opt.Secret == "" {
		opt.Secret = "kaimahi-" + opt.Upstream + "-token"
	}
	spec := scaffold.SidecarSpec{
		Upstream:   opt.Upstream,
		Namespace:  opt.Namespace,
		Secret:     opt.Secret,
		Deployment: opt.Deployment,
	}
	configMap, patch, err := scaffold.GenerateSidecar(spec)
	if err != nil {
		return err
	}
	if opt.Out == "-" {
		_, err := fmt.Fprint(a.Out, configMap, "\n", patch)
		return err
	}

	path := opt.Out
	if path == "" {
		path = filepath.Join("upstreams", opt.Upstream+"-auth-sidecar.yaml")
	}
	// The patch goes beside the ConfigMap rather than inside it. They are
	// applied by different people to different things, and a patch that
	// looked like an object would invite `kubectl apply -f`, which would
	// replace the Deployment's whole pod spec with the shim's two entries.
	patchPath := strings.TrimSuffix(path, ".yaml") + ".patch.yaml"
	if err := scaffold.WriteNew(path, configMap); err != nil {
		return err
	}
	if err := scaffold.WriteNew(patchPath, patch); err != nil {
		return err
	}
	a.notef("Wrote %s — the shim's nginx config, which kmx applies.", path)
	a.notef("Wrote %s — the container and volumes to merge into YOUR", patchPath)
	a.notef("Deployment, which kmx does not apply.")

	if !opt.NoApply {
		if err := a.Guard(fmt.Sprintf("write the credential shim's config into namespace %q", opt.Namespace),
			"kmx tools sidecar "+opt.Upstream); err != nil {
			return err
		}
		if err := a.requireNamespace(opt.Namespace, "--namespace"); err != nil {
			return err
		}
		if err := a.kubectlRun("apply", "-f", path); err != nil {
			return err
		}
		// The shim verifies the seam, so it needs the authority where it
		// runs. Public material: it says who to trust and confers nothing.
		if err := a.publishPlaneAuthority(opt.Namespace); err != nil {
			return err
		}
	} else {
		a.notef("Not applied (--no-apply). Review it, then:")
		a.notef("  kubectl --context %s apply -f %s", a.Cfg.KubeContext, path)
	}

	a.notef("")
	a.notef("The credential the shim presents is in Secret %s/%s (key api-key).",
		opt.Namespace, opt.Secret)
	a.notef("If it is not there yet, `kmx tools govern --secret %s --secret-namespace %s …` writes it.",
		opt.Secret, opt.Namespace)
	a.notef("Then add the shim to your workload and repoint the client:")
	a.notef("  kubectl --context %s -n %s patch deployment %s --patch-file %s",
		a.Cfg.KubeContext, opt.Namespace, opt.Deployment, patchPath)
	a.notef("  the MCP client's URL becomes %s", spec.SidecarURL())
	a.notef("")
	a.notef("The shim listens on loopback inside your pod, so nothing else in the cluster can")
	a.notef("reach it, and the token is never in a manifest, a values file or an image.")
	return nil
}
