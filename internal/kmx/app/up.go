package app

import (
	"fmt"
	"strings"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/guard"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

// UpSteps are the individually addressable steps of `kmx up`, and they are
// exactly the supported sequence. The three legacy installer steps that once
// sat beyond them are gone rather than hidden: naming one is an unknown step,
// refused locally, not a runtime kmx still knows how to put on a cluster.
var UpSteps = []string{"cluster", "ollama", "model", "orka"}

// UpDefaultSteps is what a bare `kmx up` runs: a cluster, a keyless model
// server and the pinned Orka runtime. It deploys no agent — `kmx quickstart`
// is the command that ends with one answering, and `kmx agent create` is the
// one that authors your own.
//
// It holds the same four names as UpSteps, and that is the point rather than
// a duplication: a bare run does everything that can be asked for one step at
// a time, so there is nothing addressable that a bare run skips. They are
// separate slices rather than one aliased twice, because two exported
// variables sharing a backing array is a way for a caller to change one by
// touching the other; a test pins them equal instead.
var UpDefaultSteps = []string{"cluster", "ollama", "model", "orka"}

// Up runs the whole journey, or the single named step.
//
// On kind this IS the journey: `make up` is one line delegating here, so the
// sequence CI runs is the sequence this function implements rather than a
// list of targets that could drift from it. RUNTIME ONLY: `kmx up` does not
// deploy the governance plane, and says so at the end rather than leaving
// anyone to discover it from an empty ledger.
func (a *App) Up(step string) error {
	started := a.timeNow()
	steps := UpDefaultSteps
	if step != "" {
		found := false
		for _, s := range UpSteps {
			if s == step {
				found, steps = true, []string{s}
				break
			}
		}
		if !found {
			return fmt.Errorf("unknown step %q — one of: %s", step, strings.Join(UpSteps, ", "))
		}
	}
	if step == "" || step == "cluster" {
		if err := a.validateKindTarget(); err != nil {
			return err
		}
	}
	if err := a.preflightUp(steps); err != nil {
		return err
	}
	if step == "" {
		if err := a.maybeSelectLocalModel(true); err != nil {
			return err
		}
	}

	action := "bring up the kmx runtime (kind, Ollama, Orka)"
	command := "kmx up"
	if step != "" {
		action, command = "run the '"+step+"' step", "kmx up --step "+step
	}
	// Every step writes to the Orka path's two namespaces, so the banner
	// names them exactly — for a bare run and for a single step alike. The
	// wider list existed for the legacy install, which landed in a namespace
	// of its own as well, and naming a namespace nothing is written to is its
	// own untruth.
	guard := func(action, command string) error {
		return a.GuardCreateIn(action, command, OrkaPathNamespaces)
	}
	if step == "cluster" {
		guard = a.GuardCreate
	}
	if err := guard(action, command); err != nil {
		return err
	}

	if step != "" {
		if err := a.runPhase(phase{current: 1, total: 1, name: upPhaseName(step)}, func() error {
			return a.runUpStep(step)
		}); err != nil {
			return err
		}
	} else if err := a.upDefault(); err != nil {
		return err
	}

	if step == "" {
		a.complete("Runtime setup finished", started)
		// One line: `kmx up` is the RUNTIME. Governance is a deliberate
		// second step, and saying nothing here would leave an operator to
		// infer it from an empty ledger.
		// The route offered is the one this cluster can take. A bare run
		// deploys no governed Agent, so the old governance operation would
		// name nothing; putting an application's model traffic on the seam is
		// `kmx migrate`, one workload at a time.
		if a.selectedLocalModel == nil {
			a.notef("\nNEXT  Runtime only: this command does not enable governance.\n"+
				"Existing governance is not assessed by this setup. To configure it:\n"+
				"  %s  # the proxy and its ledger\n"+
				"  %s --namespace <ns> --model %s/%s  # route an application's model traffic (docs/migrate.md)",
				a.operationCommand("plane"), a.operationCommand("migrate", "<deployment>"),
				orkaDefaultProvider, a.Cfg.Model)
		} else {
			a.notef("\nNEXT  Host Ollama reuse is a direct route; the bundled plane preset requires in-cluster Ollama.")
		}
		a.notef("\nTRY   %s", a.operationCommand("agent", "create"))
	}
	return nil
}

func upPhaseName(step string) string {
	return map[string]string{
		"cluster": "Prepare kind cluster",
		"ollama":  "Deploy Ollama",
		"model":   "Pull model",
		"orka":    "Install Orka",
	}[step]
}

func (a *App) runUpStep(step string) error {
	switch step {
	case "cluster":
		return a.stepCluster()
	case "ollama":
		return a.stepOllama()
	case "model":
		return a.stepModel()
	case "orka":
		return a.stepOrka()
	}
	return fmt.Errorf("unknown up step %q", step)
}

// upDefault is the whole supported journey, in the order the steps are
// DECLARED in (UpDefaultSteps) — the order an operator reads them in and the
// order `--step` runs them in.
//
// Ollama is deployed and its model pulled before Orka is installed, so a
// cluster that cannot pull at all still fails on the smaller download first.
// Nothing here overlaps: the earlier two-agent lane existed for the legacy
// runtime's demonstration agents, which are gone, and the measurements that
// lane rested on said the remaining steps gain nothing from running together
// — they pull ~2GB of images between them, so starting them at once splits
// the bandwidth instead of saving time.
func (a *App) upDefault() error {
	if err := a.runPhase(phase{current: 1, total: 4, name: upPhaseName("cluster")}, a.stepCluster); err != nil {
		return err
	}
	a.verifySelectedLocalModel()
	if a.selectedLocalModel == nil {
		if err := a.runPhase(phase{current: 2, total: 4, name: upPhaseName("ollama")}, a.stepOllama); err != nil {
			return err
		}
		if err := a.runPhase(phase{current: 3, total: 4, name: upPhaseName("model")}, a.stepModel); err != nil {
			return err
		}
	} else {
		a.notef("SKIP   Reusing %s/%s; no in-cluster Ollama or model pull", a.selectedLocalModel.Provider, a.selectedLocalModel.Model)
	}
	return a.runPhase(phase{current: 4, total: 4, name: upPhaseName("orka")}, a.stepOrka)
}

func (a *App) preflightUp(steps []string) error {
	return a.preflight(a.upDependencies(steps)...)
}

// upDependencies is what this selection of steps shells out to. It is
// separate from preflightUp so the list is readable — and assertable —
// without provisioning anything: Helm was fetched onto operators' machines
// for the legacy chart alone, and nothing here may quietly ask for it again.
func (a *App) upDependencies(steps []string) []dependency {
	wanted := map[string]bool{}
	for _, step := range steps {
		wanted[step] = true
	}
	var dependencies []dependency
	if wanted["cluster"] {
		dependencies = append(dependencies, depKind, depKubectl, a.engineDependency())
	}
	if wanted["ollama"] || wanted["model"] || wanted["orka"] {
		dependencies = append(dependencies, depKubectl)
	}
	return dependencies
}

// ---- cluster --------------------------------------------------------------

// rememberInventedContext turns the one context nobody chose into a choice,
// at the moment kmx creates the cluster it names.
//
// It fires only for the invented default. A machine with no clusters is the
// only place that default survives the guard, and creating the cluster is
// what puts a context in the kubeconfig — so without this, the NEXT bare
// command would be refused for pointing at a name nobody picked, on a
// machine where kmx had just picked it. An explicit `kmx ctx` choice is
// never overwritten, and a per-invocation KIND_CLUSTER or --context never
// becomes sticky.
func (a *App) rememberInventedContext() {
	if a.Cfg.ContextSource != config.SourceDefault {
		return
	}
	path, err := config.WriteSelectedContext(a.Cfg.KubeContext)
	if err != nil {
		// Not fatal: the cluster is up, and the cost of failing here is one
		// `kmx ctx` the operator types themselves.
		a.notef("could not record %q as the context kmx acts on (%v) — set it with: kmx ctx %s",
			a.Cfg.KubeContext, err, shellArg(a.Cfg.KubeContext))
		return
	}
	a.Cfg.ContextSource = config.SourceSelected
	a.notef("kmx will act on context %q from now on (recorded in %s); change it with: kmx ctx <name>",
		a.Cfg.KubeContext, path)
}

// stepCluster brings up the kind cluster: create it, or on Podman recover it.
//
// `kind get clusters` failing is NOT "no clusters": a broken or absent kind,
// or a docker socket the user cannot reach, would otherwise read as "the
// cluster is missing" and send us straight into a create that fails later
// and less clearly.
//
// The Podman half is #53's, carried across when this recipe became a
// delegation so the fix is not lost: on that engine "listed" does not mean
// "running", and a cluster whose nodes were stopped has to be started rather
// than re-created.
func (a *App) stepCluster() (err error) {
	if err := a.validateKindTarget(); err != nil {
		return err
	}
	// Only on success: recording a context for a cluster that failed to come
	// up would point every later command at something that is not there.
	defer func() {
		if err == nil {
			a.rememberInventedContext()
		}
	}()
	existing, err := a.Run.Capture("kind", "get", "clusters")
	if err != nil {
		return fmt.Errorf("`kind get clusters` failed — refusing to guess whether %q exists (is the %s daemon running?): %w",
			a.Cfg.KindCluster, a.Cfg.ContainerEngine, err)
	}
	listed := false
	for _, line := range strings.Split(existing, "\n") {
		if strings.TrimSpace(line) == a.Cfg.KindCluster {
			listed = true
			break
		}
	}

	// The Docker path: create it if it is not there.
	if a.Cfg.ContainerEngine != "podman" {
		if listed {
			a.notef("kind cluster %q already exists", a.Cfg.KindCluster)
			if err := a.ensureKindContext(); err != nil {
				return err
			}
			return a.waitClusterServing()
		}
		if err := a.Run.Run("kind", "create", "cluster", "--name", a.Cfg.KindCluster); err != nil {
			return err
		}
		return a.waitClusterServing()
	}

	// The Podman path (#53, by @sajayantony): restarting the podman machine
	// stops kind's node containers, after which `kind get clusters` still
	// lists the cluster and nothing can reach it. Listed-but-stopped is
	// therefore not "already exists" — start the nodes back up.
	if listed {
		if err := a.ensureKindContext(); err != nil {
			return err
		}
		nodes, err := a.Run.Capture("podman", "ps", "-a",
			"--filter", "label=io.x-k8s.kind.cluster="+a.Cfg.KindCluster,
			"--format", "{{.Names}}")
		if err != nil {
			return fmt.Errorf("cannot list podman's nodes for kind cluster %q: %w", a.Cfg.KindCluster, err)
		}
		names, err := podmanNodes(nodes, a.Cfg.KindCluster)
		if err != nil {
			return err
		}
		if err := a.Run.Run("podman", append([]string{"start"}, names...)...); err != nil {
			return err
		}
	} else if err := a.Run.Run("kind", "create", "cluster", "--name", a.Cfg.KindCluster); err != nil {
		return err
	}

	return a.waitClusterServing()
}

func (a *App) ensureKindContext() error {
	cfg, err := a.kubeconfig()
	if err != nil {
		return err
	}
	posture, err := guard.Classify(cfg, a.Cfg.KubeContext)
	if err != nil {
		return err
	}
	if posture.Host != "" {
		return nil
	}
	a.notef("kind cluster %q exists but context %q is missing; repairing kubeconfig", a.Cfg.KindCluster, a.Cfg.KubeContext)
	if err := a.Run.Run("kind", "export", "kubeconfig", "--name", a.Cfg.KindCluster); err != nil {
		return fmt.Errorf("cannot repair kubeconfig for kind cluster %q: %w", a.Cfg.KindCluster, err)
	}
	return nil
}

// kind acts on a container cluster name, while kubectl and the guard act on
// a context. Check only kind-specific paths, not shared managed-cluster steps.
func (a *App) validateKindTarget() error {
	want := "kind-" + a.Cfg.KindCluster
	if a.Cfg.KindCluster == "" || a.Cfg.KubeContext != want {
		return fmt.Errorf("refusing: context %q does not identify kind cluster %q (expected context %q).\n"+
			"Set KIND_CLUSTER and --context/KUBE_CTX consistently before creating a cluster or loading an image",
			a.Cfg.KubeContext, a.Cfg.KindCluster, want)
	}
	return nil
}

// waitClusterServing refuses to leave the cluster step until the API server
// answers and CoreDNS is actually serving.
//
// This used to run on the Podman path only, where a restarted machine made
// the need obvious. It is not engine-specific: on a nested-runtime
// machine where kube-proxy crash-looped, the cluster came up "successfully"
// and the run died two minutes later on
//
//	Error: pull model manifest: ... dial tcp: lookup registry.ollama.ai: i/o timeout
//
// which reads as "the internet is broken" and is really "this cluster has no
// working DNS". The failure belongs at the step that caused it, in words that
// say so — a first run that fails is survivable, a first run that fails
// somewhere unrelated is what makes people give up.
func (a *App) waitClusterServing() error {
	ready := run.PollContext(a.operationContext(), 60, 2*time.Second, func() bool {
		return a.kubectlQuiet("get", "--raw=/readyz")
	})
	if !ready {
		if err := a.operationContext().Err(); err != nil {
			return err
		}
		return fmt.Errorf("kind cluster %q API did not become ready after 120s", a.Cfg.KindCluster)
	}
	if err := a.kubectlRun("-n", "kube-system", "rollout", "status", "deployment/coredns", "--timeout=180s"); err != nil {
		return fmt.Errorf("kind cluster %q came up but its DNS did not: %w\n"+
			"  nothing that needs to resolve a name will work until it does — check `kubectl -n kube-system get pods`\n"+
			"  (a crash-looping kube-proxy or CNI is the usual cause, and is a problem with the container runtime, not with what kmx applied)", a.Cfg.KindCluster, err)
	}
	return nil
}

// podmanNodes turns `podman ps -a --format {{.Names}}` output into the node
// list to start, and refuses an empty one.
//
// Empty is the case worth naming: kind still lists the cluster, so something
// exists as far as kind is concerned, but Podman has no containers for it —
// kind's record and Podman's reality disagree. Starting nothing and carrying
// on would walk into a cluster nothing can reach, so it fails closed here
// instead.
func podmanNodes(listing, cluster string) ([]string, error) {
	names := strings.Fields(listing)
	if len(names) == 0 {
		return nil, fmt.Errorf("kind lists cluster %q, but podman has no nodes for it", cluster)
	}
	return names, nil
}

// ---- ollama ---------------------------------------------------------------

func (a *App) stepOllama() error {
	if err := a.apply("ollama.yaml"); err != nil {
		return err
	}
	return a.kubectlRun("-n", "ollama", "rollout", "status", "deploy/ollama", "--timeout=300s")
}

// stepModel pulls the pinned model into the running Ollama, retrying a
// transient failure.
//
// The retry is not defensive padding — it was measured. Two of five clean-machine
// first runs died here with
//
//	Error: pull model manifest: Get "https://registry.ollama.ai/...": dial tcp: lookup registry.ollama.ai: i/o timeout
//
// which is the cluster's DNS not being ready for the outside world yet, a
// minute or two into a first run. Failing the whole bring-up on it costs the
// operator everything done so far and reads as "this does not work", which is
// the abandonment this command exists to prevent. Three attempts, and the
// failure is still the real one if the network genuinely is not there.
//
// It is safe to repeat: `ollama pull` of a model already present is a no-op,
// and a partial pull resumes.
func (a *App) stepModel() error {
	var err error
	for attempt := 1; attempt <= 3; attempt++ {
		if err = a.kubectlRun("-n", "ollama", "exec", "deploy/ollama", "--", "ollama", "pull", a.Cfg.Model); err == nil {
			return nil
		}
		if err := a.operationContext().Err(); err != nil {
			return err
		}
		if attempt != 3 {
			a.notef("the model pull failed (attempt %d/3) — retrying in 10s", attempt)
			select {
			case <-a.operationContext().Done():
				return a.operationContext().Err()
			case <-time.After(10 * time.Second):
			}
		}
	}
	return err
}
