package app

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/guard"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

// UpSteps are the individually addressable steps of `kmx up`. The first
// four are the supported sequence; `kagent`, `agent` and `tools-agent`
// remain addressable only so the legacy retirement slices that delete them
// can each be independently green. There is no flag that puts them back
// into a bare run.
var UpSteps = []string{"cluster", "ollama", "model", "orka", "kagent", "agent", "tools-agent"}

// UpDefaultSteps is what a bare `kmx up` runs: a cluster, a keyless model
// server and the pinned Orka runtime. It deploys no agent — `kmx quickstart`
// is the command that ends with one answering, and `kmx agent create` is the
// one that authors your own.
var UpDefaultSteps = []string{"cluster", "ollama", "model", "orka"}

// upLegacySteps are the steps that put the kagent runtime on the cluster.
// They are the ONLY reason `kmx up` reads `agents.kagent.dev`: the final
// status collection is a kagent read, and a cluster that never installed
// kagent answers it with "the server doesn't have a resource type", which
// would fail an Orka-only run at its last phase over something the run
// deliberately did not deploy.
var upLegacySteps = []string{"kagent", "agent", "tools-agent"}

// stepsIncludeLegacy reports whether this invocation asked for the legacy
// runtime, and so whether the legacy status is about something it did.
func stepsIncludeLegacy(steps []string) bool {
	for _, step := range steps {
		for _, legacy := range upLegacySteps {
			if step == legacy {
				return true
			}
		}
	}
	return false
}

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
	guard := a.Guard
	switch step {
	case "":
		// A bare run is the Orka runtime and nothing else, so its banner
		// names the two namespaces it writes to. An explicitly requested
		// legacy step keeps the wider list, because that step really does
		// land in kagent and kaimahi.
		guard = func(action, command string) error {
			return a.GuardCreateIn(action, command, OrkaPathNamespaces)
		}
	case "cluster":
		guard = a.GuardCreate
	}
	if err := guard(action, command); err != nil {
		return err
	}

	legacy := stepsIncludeLegacy(steps)
	if step != "" {
		total := 1
		if legacy {
			total = 2
		}
		if err := a.runPhase(phase{current: 1, total: total, name: upPhaseName(step)}, func() error {
			return a.runUpStep(step)
		}); err != nil {
			return err
		}
		if legacy {
			// Kept exactly here: the legacy runtime is on this cluster
			// because this invocation asked for it, so the kagent read the
			// status collection makes is a read of what just happened.
			if err := a.runPhase(phase{current: 2, total: 2, name: "Collect runtime status"}, a.Status); err != nil {
				return err
			}
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
		// deploys no kagent Agent, so `kmx govern <credential>` would name
		// nothing; putting an application's model traffic on the seam is
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
		"cluster":     "Prepare kind cluster",
		"ollama":      "Deploy Ollama",
		"model":       "Pull model",
		"orka":        "Install Orka",
		"kagent":      "Install kagent",
		"agent":       "Deploy hello-world agent",
		"tools-agent": "Deploy hello-tools agent",
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
	case "kagent":
		return a.stepKagent()
	case "agent":
		return a.stepAgent()
	case "tools-agent":
		return a.stepToolsAgent()
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
// runtime's demonstration agents, which a bare run no longer deploys, and
// the measurements that lane rested on said the remaining steps gain nothing
// from running together — they pull ~2GB of images between them, so starting
// them at once splits the bandwidth instead of saving time.
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
	wanted := map[string]bool{}
	for _, step := range steps {
		wanted[step] = true
	}
	var dependencies []dependency
	if wanted["cluster"] {
		dependencies = append(dependencies, depKind, depKubectl, a.engineDependency())
	}
	if wanted["ollama"] || wanted["model"] || wanted["orka"] || wanted["agent"] || wanted["tools-agent"] {
		dependencies = append(dependencies, depKubectl)
	}
	if wanted["kagent"] {
		dependencies = append(dependencies, depHelm, depKubectl)
	}
	return a.preflight(dependencies...)
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

// ---- kagent ---------------------------------------------------------------
//
// EXPLICIT ONLY. `kmx up --step kagent` still installs the legacy runtime so
// the slices that retire it can each be green on their own; nothing a bare
// `kmx up` or `kmx quickstart` runs reaches this code, and there is no flag
// that puts it back.

func (a *App) stepKagent() error { return a.installKagent() }

// installKagent installs the chart.
//
// It once took `--set` overlays for one caller — the quickstart profile that
// deferred the console, the bundled tool server and the MCP controller. That
// caller is gone: the first answer comes from Orka, so there is no longer a
// reduced kagent release to describe, reconcile or restore.
func (a *App) installKagent() error {
	version := a.Cfg.KagentVersion
	if err := a.Run.Run("helm", "upgrade", "--install", "kagent-crds",
		"oci://ghcr.io/kagent-dev/kagent/helm/kagent-crds",
		"--version", version, "--namespace", "kagent", "--create-namespace",
		"--kube-context", a.Cfg.KubeContext); err != nil {
		return err
	}

	// helm -f wants a path, and the values file lives inside the binary.
	values, err := a.renderModelManifest("kagent-values.yaml")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp("", "kagent-values-*.yaml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(values); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	args := []string{"upgrade", "--install", "kagent",
		"oci://ghcr.io/kagent-dev/kagent/helm/kagent",
		"--version", version, "--namespace", "kagent",
		"--kube-context", a.Cfg.KubeContext, "-f", tmp.Name(),
		"--wait", "--wait-for-jobs", "--timeout", "420s"}
	return a.Run.Run("helm", args...)
}

// ---- the agents -----------------------------------------------------------

// isNotFound reports whether a kubectl failure was "the object is not there",
// as opposed to "the cluster could not be reached" or "you may not read it".
//
// The distinction is the whole point of the capture-then-restore dance: a
// fresh cluster legitimately has no agent yet, but an unreachable API server
// must NEVER be read as "nothing to preserve" — that is how a re-apply
// silently un-governs a live agent.
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "NotFound") || strings.Contains(message, `" not found`)
}

// liveModelConfig reads an agent's current modelConfig.
//
// Re-applying the committed YAML must not silently drop governance (or any
// preset switch) from a live agent, so the current value is captured first
// and a non-default one is restored after the apply, with a warning. Only a
// NotFound (fresh cluster) may skip the capture — ANY other read failure
// aborts rather than risk silently un-governing an agent.
func (a *App) liveModelConfig(agent string) (string, error) {
	out, err := a.kubectlCapture("-n", "kagent", "get", "agents.kagent.dev", agent,
		"-o", "jsonpath={.spec.declarative.modelConfig}")
	if err != nil {
		if isNotFound(err) {
			return "", nil
		}
		// Deliberately NOT treated as absence, however it reads. The most
		// common cause is that kagent is not installed yet — the Agent CRD
		// does not exist, so kubectl says "the server doesn't have a resource
		// type", not NotFound — and applying an Agent to that cluster would
		// fail a moment later anyway. Name the likely cause; do not guess.
		// The hint is on its own line, as every other operator-facing refusal
		// in this package is: these are printed to a terminal, not wrapped by
		// a caller.
		return "", fmt.Errorf("cannot read %s's live modelConfig (refusing to risk un-governing it): %w\n"+
			"  if kagent is not installed on this cluster yet, that is `kmx up`", agent, err)
	}
	return strings.TrimSpace(out), nil
}

// desiredModelConfig applies the preservation rule: a live non-default
// modelConfig wins over the committed one.
func (a *App) desiredModelConfig(agent, current string) (string, bool) {
	if current != "" && current != config.KeylessModelConfig {
		a.notef("NOTE: %s was on modelConfig %q — preserving it ('make use PRESET=ollama' resets)", agent, current)
		return current, true
	}
	return config.KeylessModelConfig, false
}

func (a *App) patchModelConfig(agent, modelConfig string) error {
	patch := fmt.Sprintf(`{"spec":{"declarative":{"modelConfig":%q}}}`, modelConfig)
	return a.kubectlRun("-n", "kagent", "patch", "agents.kagent.dev", agent, "--type", "merge", "-p", patch)
}

func (a *App) waitAgentReady(agent string) error {
	return a.kubectlRun("-n", "kagent", "wait",
		`--for=jsonpath={.status.conditions[?(@.type=="Ready")].status}=True`,
		"agents.kagent.dev/"+agent, "--timeout=300s")
}

func (a *App) stepAgent() error {
	current, err := a.liveModelConfig("hello-world")
	if err != nil {
		return err
	}
	desired, changed := a.desiredModelConfig("hello-world", current)
	if current == config.KeylessModelConfig && a.selectedLocalModel == nil && !a.Cfg.ModelExplicit {
		a.selectedLocalModel, err = a.liveKeylessLocalModel()
		if err != nil {
			return err
		}
	}
	body, err := a.renderModelManifest("hello-world.yaml")
	if err != nil {
		return err
	}
	if err := a.applyBytes("hello-world.yaml", body); err != nil {
		return err
	}
	if changed {
		if err := a.patchModelConfig("hello-world", desired); err != nil {
			return err
		}
	}
	return a.waitAgentReady("hello-world")
}

func (a *App) liveKeylessLocalModel() (*localModel, error) {
	raw, err := a.kubectlCapture("-n", "kagent", "get", "modelconfig", config.KeylessModelConfig, "-o", "json")
	if isNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cannot read live ModelConfig %q: %w", config.KeylessModelConfig, err)
	}
	return parseLiveKeylessLocalModel([]byte(raw))
}

func parseLiveKeylessLocalModel(raw []byte) (*localModel, error) {
	var live struct {
		Spec struct {
			Provider string `json:"provider"`
			Model    string `json:"model"`
			Ollama   struct {
				Host string `json:"host"`
			} `json:"ollama"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(raw, &live); err != nil {
		return nil, fmt.Errorf("cannot parse live ModelConfig %q: %w", config.KeylessModelConfig, err)
	}
	host := strings.TrimSpace(live.Spec.Ollama.Host)
	if !strings.EqualFold(strings.TrimSpace(live.Spec.Provider), "ollama") || host == "" || host == "http://ollama.ollama.svc.cluster.local:11434" {
		return nil, nil
	}
	return &localModel{Provider: "ollama", Model: strings.TrimSpace(live.Spec.Model), Endpoint: host}, nil
}

// agentJSON is the sliver of an Agent kmx reads back.
type agentJSON struct {
	Spec struct {
		Declarative struct {
			ModelConfig string          `json:"modelConfig"`
			Tools       json.RawMessage `json:"tools"`
		} `json:"declarative"`
	} `json:"spec"`
}

// stepToolsAgent preserves the non-default model and existing owner tool
// wiring. A retired gateway reference still belongs to the owner: applying
// the default manifest must not silently repoint it at a direct server.
func (a *App) stepToolsAgent() error {
	// The RemoteMCPServer the chart publishes has to be accepted before an
	// Agent can wire to it.
	if err := a.kubectlRun("-n", "kagent", "wait",
		`--for=jsonpath={.status.conditions[?(@.type=="Accepted")].status}=True`,
		"remotemcpserver/kagent-tool-server", "--timeout=300s"); err != nil {
		return err
	}

	var live agentJSON
	raw, err := a.kubectlCapture("-n", "kagent", "get", "agents.kagent.dev", "hello-tools", "-o", "json")
	switch {
	case isNotFound(err):
		// Fresh cluster: nothing to preserve.
	case err != nil:
		return fmt.Errorf("cannot read hello-tools' live tool wiring (refusing to risk un-governing it): %w", err)
	default:
		if err := json.Unmarshal([]byte(raw), &live); err != nil {
			return fmt.Errorf("cannot parse hello-tools: %w", err)
		}
	}

	// Is the live agent wired to the GATEWAY? Every entry is inspected, not
	// just the first: an agent with a second tool that happens to sort ahead
	// of the governed one would otherwise read as ungoverned and be quietly
	// pointed back at the unaudited server. And a decode failure is an
	// error, not a "no": failing to understand the live wiring is exactly
	// when this code must not act.
	governedByGateway := false
	if len(live.Spec.Declarative.Tools) > 0 {
		var tools []struct {
			McpServer struct {
				Name string `json:"name"`
			} `json:"mcpServer"`
		}
		if err := json.Unmarshal(live.Spec.Declarative.Tools, &tools); err != nil {
			return fmt.Errorf("cannot read hello-tools' live tool wiring (refusing to risk un-governing it): %w", err)
		}
		for _, tool := range tools {
			if tool.McpServer.Name == "kaimahi-tools" {
				governedByGateway = true
				break
			}
		}
	}

	desired, changed := a.desiredModelConfig("hello-tools", live.Spec.Declarative.ModelConfig)
	if err := a.apply("tools-agent.yaml"); err != nil {
		return err
	}
	if changed {
		if err := a.patchModelConfig("hello-tools", desired); err != nil {
			return err
		}
	}
	if governedByGateway {
		a.notef("NOTE: preserving hello-tools' existing kaimahi-tools wiring. The gateway is retired; review and replace this owner-managed route deliberately.")
		patch := fmt.Sprintf(`{"spec":{"declarative":{"tools":%s}}}`, string(live.Spec.Declarative.Tools))
		if err := a.kubectlRun("-n", "kagent", "patch", "agents.kagent.dev", "hello-tools", "--type", "merge", "-p", patch); err != nil {
			return err
		}
	}
	return a.waitAgentReady("hello-tools")
}
