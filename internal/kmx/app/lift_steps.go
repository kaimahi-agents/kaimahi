package app

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/admin"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/lift"
)

// aimAtTheCluster points every later read and write at the managed cluster.
//
// kmx passes an explicit --context on every kubectl and helm invocation, so
// this one assignment is what makes the reused command implementations act on
// AKS instead of on kind. It is set from the cluster name because that is
// what `az aks get-credentials` writes the context as, and it is set once,
// centrally, rather than being threaded through every call — a path that
// resolved the context per command would eventually have a command that
// forgot, and that command would silently act on the operator's local
// cluster.
func (a *App) aimAtTheCluster(opt lift.Options) {
	a.Cfg.KubeContext = opt.Cluster
	a.Cfg.ContextSource = "the cluster named on the command line"
}

// credentials writes the kubeconfig entry for the cluster. Run on both
// branches: on a created cluster aks-up.sh has already done it, and doing it
// again is how a resumed `--step` gets a context without re-running the
// create.
func (a *App) liftCredentials(opt lift.Options) error {
	return a.Run.Run("az", "aks", "get-credentials", "--name", opt.Cluster,
		"--resource-group", opt.ResourceGroup, "--overwrite-existing", "--output", "none")
}

// liftBoundary is the phase that decides whether anything else is allowed to
// happen, and on a cluster we did not create it is the whole reason the lift
// has a shape at all.
//
// NetworkPolicy is an API; the CNI enforces it. A cluster whose CNI ignores
// it reports the plane's policies as present while blocking nothing, which
// reads as protection and is worse than having none — an operator would
// believe the model seam and its database were isolated when every pod in
// the cluster can reach them.
//
// So the boundary is proven before the plane is put behind it, in two gates
// of escalating cost:
//
//  1. Ask the control plane which policy engine the cluster has. This is
//     cheap, it runs before anything is written, and it catches the case that
//     actually occurs — a cluster created with no engine at all.
//  2. Deploy the boundary itself and the one workload the existing negative
//     proof execs into, then run that proof unchanged. This is the gate that
//     means something: it asserts connections that must time out, against a
//     control that must succeed, so "blocked" cannot be a dead target or a
//     runner with no internet.
//
// Gate 2 writes before it proves, and that is deliberate rather than
// conceded: what it writes IS the boundary, plus an empty ledger. If the
// proof fails, no governance plane, no credential and no agent has been put
// behind a boundary that does not hold, and the operator is told exactly what
// exists so they can remove it.
func (a *App) liftBoundary(opt lift.Options, work string) error {
	// Gate 1. On a cluster this path created, aks-up.sh already refused
	// anything but an enforcing engine and read it back from the control
	// plane; asking again is cheap and keeps the two branches honest about
	// having the same requirement rather than one of them trusting a script.
	engine, readable := a.clusterPolicyEngine(opt.ResourceGroup, opt.Cluster)
	if err := lift.PolicyEngineVerdict(engine, readable); err != nil {
		return err
	}
	a.notef("policy engine %q reported by the control plane — present, which is not yet enforced", engine)

	resume := opt
	resume.Step = "boundary"
	if err := a.Guard("deploy the network boundary and the ledger", a.liftCommand(resume, false)); err != nil {
		return err
	}

	if err := a.planeSecrets(); err != nil {
		return err
	}
	for _, name := range []string{"plane/network-policy.yaml", "plane/postgres.yaml"} {
		if err := a.apply(name); err != nil {
			return err
		}
	}
	// The hosted model needs the proxy to reach the internet on 443, and the
	// probe below is told to expect exactly that. Applying it here rather
	// than with the credential keeps the boundary one phase: the proof runs
	// against the allowances the plane will actually have.
	if err := a.applyManaged(work, "k8s/egress-copilot.yaml"); err != nil {
		return err
	}
	if err := a.kubectlRun("-n", admin.Namespace, "rollout", "status",
		"deploy/kaimahi-postgres", "--timeout=300s"); err != nil {
		return err
	}

	// Gate 2: the existing negative proof, unchanged. It is handed the same
	// explicit --context every other command here carries, and it re-derives
	// and re-guards that context itself.
	a.notef("proving the boundary is ENFORCED rather than merely present (this creates and deletes a few probe pods)")
	err := a.runScript(work, "scripts/netpol-probe.sh", map[string]string{
		"KUBECTL":         "kubectl --context " + a.Cfg.KubeContext,
		"COPILOT_EGRESS":  "1",
		"KAIMAHI_CONFIRM": a.Cfg.Confirm,
	})
	if err != nil {
		return fmt.Errorf(`the network boundary is NOT enforced on this cluster.

  What exists on it now: the kaimahi namespace, its NetworkPolicies, and an
  empty ledger. No governance plane, no credential and no agent has been put
  behind a boundary that does not hold, which is the whole reason this phase
  runs before those.

  Remove what was created with:
    kubectl --context %s delete namespace %s

  underlying failure: %w`, shellArg(a.Cfg.KubeContext), admin.Namespace, err)
	}
	return nil
}

func (a *App) liftKagent() error {
	return a.installKagent()
}

// The managed cluster runs a hosted model, so it needs a real provider token.
// If the Secret is absent, use the same device-login operation exposed as
// `kmx models credential copilot`; no checkout, Make target, key flag or stdin
// credential path is involved.
//
// The order matters and it is the thing that has bitten this path before. The
// proxy mounts the token as an OPTIONAL Secret volume, so a proxy pod that
// starts before the Secret exists comes up with an empty mount and every
// governed call then fails closed with "upstream credential unavailable"
// until kubelet gets around to projecting it — minutes later, looking exactly
// like a broken deployment rather than a race. Checking here, before the
// plane phase, is what keeps that from happening.
func (a *App) liftCredential(opt lift.Options, work string) error {
	_, err := a.kubectlCapture("-n", admin.Namespace, "get", "secret", copilotSecretName, "-o", "name")
	switch {
	case err == nil:
		a.notef("model credential %s is present in the %s namespace.", copilotSecretName, admin.Namespace)
		return nil
	case !isNotFound(err):
		// An unreachable API server or an RBAC denial must not read as
		// "absent" here: absence sends the operator off to mint a token they
		// may already have, and presence lets the plane start correctly.
		return fmt.Errorf("cannot tell whether the model credential %s exists (refusing to guess): %w", copilotSecretName, err)
	}
	a.notef("model credential %s is absent; starting the direct Copilot device-login flow.", copilotSecretName)
	return a.CaptureCopilotCredential()
}

// liftPlane builds the plane's image IN Azure and deploys it from the private
// registry.
//
// `az acr build` uploads a build context and builds inside the registry, so
// the operator needs no container engine, no `docker push`, and no registry
// login — and the image never leaves a private registry. Two shapes of
// context reach it: a checkout's plane/ directory when kmx is being run from
// one, and otherwise the same fetched-and-packaged context the local path
// builds, produced by the same code.
func (a *App) liftPlane(opt lift.Options, work string) error {
	resume := opt
	resume.Step = "plane"
	if err := a.Guard("deploy the governance plane", a.liftCommand(resume, false)); err != nil {
		return err
	}
	image := planeRegistryImage(opt.Registry)

	// On a cluster this path created, `aks-up.sh` attached the registry and
	// confirmed the role assignment before it returned. On yours it did not,
	// and cannot: granting AcrPull means creating a role assignment on your
	// subscription, against your cluster's own identity, which a demo has no
	// business doing quietly. So this checks and refuses.
	//
	// It is worth a check rather than letting it fail naturally, because the
	// natural failure is ImagePullBackOff on the proxy pod — a symptom two
	// layers away from the cause, on a cluster the operator has just been
	// told is fine.
	if opt.BringYourOwn {
		if err := a.refuseWithoutRegistryPullRights(opt); err != nil {
			return err
		}
	}

	source, err := a.planeSource("")
	if err != nil {
		return err
	}
	if source != "" {
		if err := a.Run.Run("az", "acr", "build", "--registry", opt.Registry,
			"--image", planeRegistryRepoTag(), filepath.Join(source, "plane")); err != nil {
			return err
		}
	} else {
		context, cleanup, err := a.moduleProxyBuildContext()
		if err != nil {
			return err
		}
		defer cleanup()
		if err := a.Run.Run("az", "acr", "build", "--registry", opt.Registry,
			"--image", planeRegistryRepoTag(), context); err != nil {
			return err
		}
	}

	// The certificate the model seam serves with, BEFORE the deploy that
	// mounts it. The proxy's Secret volume is not optional and has no closed
	// state to fall into — without this the kubelet cannot mount it, the pods
	// never leave ContainerCreating, and the rollout below times out after
	// five minutes on a cluster where nothing is actually wrong.
	//
	// Here rather than beside the other plane Secrets in the boundary phase,
	// so that re-running `kmx lift --step plane` on its own is enough to
	// repair a cluster whose certificate is missing or expired.
	if err := a.planeCertificate(false); err != nil {
		return err
	}

	// The committed manifest names a local tag with imagePullPolicy Never,
	// which is right for a side-loaded image and would be ErrImageNeverPull
	// forever on a registry-backed cluster. Rendering it is the carried
	// script's job — it parses the document rather than pattern-matching it,
	// and refuses a render that did not produce exactly the intended change.
	if err := a.runScript(work, "scripts/plane-deploy.sh", map[string]string{
		"KUBECTL":           "kubectl --context " + a.Cfg.KubeContext,
		"PLANE_TARGET":      "registry",
		"PLANE_IMAGE":       image,
		"PLANE_PULL_POLICY": "IfNotPresent",
		"KAIMAHI_CONFIRM":   a.Cfg.Confirm,
	}); err != nil {
		return err
	}
	if err := a.kubectlRun("-n", admin.Namespace, "rollout", "status",
		"deploy/kaimahi-postgres", "--timeout=300s"); err != nil {
		return err
	}
	if err := a.kubectlRun("-n", admin.Namespace, "rollout", "restart", "deploy/kaimahi-proxy"); err != nil {
		return err
	}
	return a.kubectlRun("-n", admin.Namespace, "rollout", "status",
		"deploy/kaimahi-proxy", "--timeout=300s")
}

func planeRegistryRepoTag() string { return PlaneImage }

func planeRegistryImage(registry string) string {
	return registry + ".azurecr.io/" + PlaneImage
}

// liftAgents puts the same two agents on the managed cluster, governed from
// the start rather than governed afterwards.
//
// On the local path agents come up on a keyless model server and are switched
// to a governed preset later. There is no keyless model server here, so an
// agent that started ungoverned would not merely be unaudited — it would have
// no model at all. Governing immediately after applying is what makes the
// managed cluster's first chat a governed one.
func (a *App) liftAgents(opt lift.Options) error {
	resume := opt
	resume.Step = "agents"
	if err := a.Guard("create the agents", a.liftCommand(resume, false)); err != nil {
		return err
	}
	for _, name := range []string{"hello-world.yaml", "tools-agent.yaml"} {
		if err := a.apply(name); err != nil {
			return err
		}
	}
	if err := a.Govern(a.Cfg.Credential, GovernOptions{
		Agent:           "hello-world",
		Preset:          "governed-copilot",
		Secret:          config.GovernedSecret,
		SecretNamespace: "kagent",
	}); err != nil {
		return err
	}
	// The direct MCP agent still needs a hosted model on managed clusters.
	// Its authored tool wiring is not changed.
	return a.UsePreset(config.DefaultToolsAgent, "governed-copilot", nil)
}

// applyManaged applies one of the manifests carried for the managed path.
// They are not in the manifest set the local path applies, so they go through
// the materialised working tree rather than through `apply`.
func (a *App) applyManaged(work, name string) error {
	path := filepath.Join(work, filepath.FromSlash(name))
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.Err, "kubectl --context %s apply -f - # (embedded %s)\n", shellArg(a.Cfg.KubeContext), name)
	quiet := *a.Run
	quiet.Echo = false
	return quiet.RunStdin(body, "kubectl", a.kubectl("apply", "-f", "-")...)
}
