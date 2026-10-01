// Orka lifecycle adapter. It routes creation through the neutral seam
// without changing a byte of what this repository renders or the order in
// which it deploys.
//
//   - Render parses the exact portable source it is handed, combines it with
//     explicit target bindings in the existing scaffold spec, generates the
//     bundle once, and returns those
//     documents inside an immutable RenderedBundle. The optional Task's
//     random identity is minted there, exactly once.
//   - Create-only Deploy consumes only that bundle. It decodes those exact
//     rendered bytes back into the bundle shape the unchanged staged path
//     already takes, so the mutation guard, installed-CRD validation, collision
//     checks, the Secret-key proof, strict server dry-runs, artifact
//     emission and Provider → Ready → Agent → Ready → optional Task ordering
//     all stay as they were.
//   - The create command deploys in reconcile mode through that same staged
//     path, so a rerun after a failed create reuses what it wrote: Provider
//     and Agent go through reconcile Deploy's ownership decisions, and the
//     optional Task stays create-only.
//
// Online creation validates against the target cluster's installed CRDs, so
// that step stays inside the staged deploy where it has always run — after
// the mutation guard, never before it, and with no offline fallback. Render
// therefore applies the pinned snapshot only when it is producing an offline
// artifact, which is the one mode with no installed schema to defer to.
//
// The neutral seam carries no prerequisite list, deliberately: a prerequisite
// stated beside the documents is a second place that can disagree with them.
// The Secret this deploy must prove is therefore read out of the rendered
// bytes themselves — the artifact's single review-only document — and checked
// against the Provider that is going to read it.
package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/orkaschema"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
	"go.yaml.in/yaml/v3"
)

// Capabilities declares exactly the lifecycle verbs this adapter instance
// implements. Render and Deploy act on this create's own flags, so they are
// declared only by an instance a create command configured: the chat
// registration builds an adapter with no create behind it, and an
// unconfigured instance that advertised Render would render some other
// create's agent out of an empty CreateOptions. Status and Evaluate read only
// the AgentRef and request they are given and are always available: Evaluate
// is bound to a revision because its request names one, and it refuses an
// Agent not deployed at exactly that portable digest. Session-level chat flags
// remain Session's own concern, not this static declaration.
func (a orkaRuntimeAdapter) Capabilities() agentruntime.Capabilities {
	configured := a.create != nil
	return agentruntime.Capabilities{Render: configured, Deploy: configured, Status: true, Evaluate: true}
}

func (orkaRuntimeAdapter) ConsumedExtensions() []agentruntime.ID {
	return []agentruntime.ID{agentruntime.Orka}
}

// lifecycleVerbError returns the one shared typed error for a verb this
// adapter does not declare, and nil for one it does.
func (a orkaRuntimeAdapter) lifecycleVerbError(declared bool, verb string) error {
	if declared {
		return nil
	}
	return &agentruntime.UnsupportedVerbError{Runtime: a.ID(), Verb: verb}
}

// Render is the neutral seam over renderOrka. The schema provenance the
// emitted artifact header records is not part of the neutral RenderedBundle,
// so the create command calls renderOrka directly; both entry points enforce
// the same declared capability.
func (a orkaRuntimeAdapter) Render(_ context.Context, prepared *agentruntime.PreparedPortableRender, _ agentruntime.RenderOptions) (agentruntime.RenderedBundle, error) {
	rendered, _, err := a.renderOrka(prepared)
	return rendered, err
}

// renderOrka generates the Orka bundle from a prepared portable document
// and returns it as an immutable RenderedBundle plus, for offline artifacts,
// the provenance of the schema it was validated against.
//
// Every render runs scaffold's schema-independent structural invariant, which
// is stronger than either CRD schema: generation refuses a bundle whose
// documents disagree about names, namespaces or references, or that carries
// anything shaped like a credential. On top of that, an offline render
// applies the pinned snapshot, which is the only structural schema available
// for a cluster kmx is not allowed to read.
//
// The rendered documents are the bundle's review order — Secret skeleton,
// Provider, Agent and the optional Task — because those exact bytes are what
// the emitted artifact contains and what the rendered digest must cover. Only
// Provider, Agent and Task are marked for deployment: the skeleton is a
// review-only document naming a prerequisite an operator provisions
// separately, and applying it would create an empty Secret.
func (a orkaRuntimeAdapter) renderOrka(prepared *agentruntime.PreparedPortableRender) (agentruntime.RenderedBundle, string, error) {
	if err := a.lifecycleVerbError(a.Capabilities().Render, agentruntime.VerbRender); err != nil {
		return agentruntime.RenderedBundle{}, "", err
	}
	portable, source, err := prepared.ForAdapter(a)
	if err != nil {
		return agentruntime.RenderedBundle{}, "", err
	}
	bindings := a.bindings
	if bindings == nil {
		fromFlags := orkaBindingsFromCreate(*a.create)
		bindings = &fromFlags
	}
	if err := agentruntime.ValidateOrkaBindings(*bindings); err != nil {
		return agentruntime.RenderedBundle{}, "", fmt.Errorf("Orka bindings: %w", err)
	}
	spec, err := orkaSpecFromPortable(portable, *bindings, *a.create)
	if err != nil {
		return agentruntime.RenderedBundle{}, "", err
	}
	// GenerateOrka runs OrkaBundle.Validate itself, so the structural
	// invariant holds for both modes before any schema is consulted. It is
	// called once: the optional Task's 16 random bytes are minted here and
	// nowhere else, so Deploy writes the Task the artifact shows.
	bundle, err := scaffold.GenerateOrka(spec)
	if err != nil {
		return agentruntime.RenderedBundle{}, "", err
	}
	provenance := ""
	// An offline create (--no-apply, including --out -) writes an artifact
	// without contacting a cluster, so the pinned snapshot named by
	// --schema-target is the only structural schema there is. Applying it to
	// an online render instead would either duplicate the installed-CRD gate
	// or refuse a document the target cluster's own schema accepts.
	if a.create.NoApply {
		validator, err := orkaschema.Offline(a.create.SchemaTarget)
		if err != nil {
			return agentruntime.RenderedBundle{}, "", err
		}
		if err := validateOrkaBundle(bundle, validator); err != nil {
			return agentruntime.RenderedBundle{}, "", err
		}
		provenance = validator.Provenance()
	}
	documents, err := orkaRenderedDocuments(bundle)
	if err != nil {
		return agentruntime.RenderedBundle{}, "", err
	}
	rendered, err := agentruntime.NewRenderedBundle(a.ID(), source, documents)
	if err != nil {
		return agentruntime.RenderedBundle{}, "", err
	}
	return rendered, provenance, nil
}

// Deploy uses what Render produced without regenerating its spec. In
// reconcile mode ownership annotations are added to write payloads, not to
// the immutable rendered digest. The optional Task keeps its identity minted at
// render time, and the namespace, Agent name and Secret it acts on come from
// the immutable rendered bytes rather than from the flags this adapter also
// carries.
//
// A --dry-run create returns a reference with no UID and no receipt.
func (a orkaRuntimeAdapter) Deploy(ctx context.Context, rendered agentruntime.RenderedBundle, options agentruntime.DeployOptions) (agentruntime.DeployResult, error) {
	if err := a.lifecycleVerbError(a.Capabilities().Deploy, agentruntime.VerbDeploy); err != nil {
		return agentruntime.DeployResult{}, err
	}
	bundle, err := orkaBundleFromRendered(rendered)
	if err != nil {
		return agentruntime.DeployResult{}, err
	}
	opt := *a.create
	opt.Namespace = orkaObjectNamespace(bundle.Agent)
	opt.Name = orkaObjectName(bundle.Agent)
	opt.Secret = orkaObjectName(bundle.Secret)
	var reconcile *agentruntime.RenderedBundle
	if options.Reconcile {
		if !a.staged {
			return a.reconcileOrka(ctx, rendered, bundle, opt)
		}
		reconcile = &rendered
	}
	var resources []orkaStagedResource
	agent, err := a.app.createOrkaStaged(ctx, opt, bundle, func(provenance string) (string, error) {
		// The artifact an operator reads is assembled from the same exact
		// bytes this deploy applies, never from a re-serialized copy.
		return scaffold.OrkaArtifact(provenance, rendered.Documents())
	}, &resources, reconcile)
	if err != nil {
		return agentruntime.DeployResult{}, err
	}
	kubeContext := ""
	if a.app.Cfg != nil {
		kubeContext = a.app.Cfg.KubeContext
	}
	result := agentruntime.DeployResult{Ref: agentruntime.AgentRef{Runtime: a.ID(), Context: kubeContext, Namespace: opt.Namespace,
		Kind: orkaPlural("Agent"), Name: opt.Name, UID: agent.UID}}
	if !opt.DryRun {
		for i, resource := range resources {
			id := resource.id
			if id.Kind == "Task" {
				continue // Task completion is checked by the staged path, not Ready.
			}
			// An earlier resource can change while a later one becomes Ready.
			if reconcile != nil {
				desired := []map[string]any{bundle.Provider, bundle.Agent}[i]
				err = a.app.verifyOrkaReconcile(ctx, opt.Namespace, desired, rendered, id)
			} else {
				err = a.app.verifyOrkaReadyNow(ctx, opt.Namespace, id)
			}
			if err != nil {
				return agentruntime.DeployResult{}, err
			}
		}
		result.Receipt = agentruntime.DeployReceipt{Bundle: opt.Name, PortableDigest: rendered.PortableDigest(), RenderedDigest: rendered.RenderedDigest(),
			Target: agentruntime.DeployTarget{Runtime: a.ID(), Context: kubeContext, Namespace: opt.Namespace}}
		for _, resource := range resources {
			id := resource.id
			result.Receipt.Resources = append(result.Receipt.Resources, agentruntime.ResourceResult{Kind: id.Kind, Name: id.Name, Namespace: opt.Namespace,
				UID: id.UID, Generation: id.Generation, Outcome: resource.outcome})
		}
	}
	return result, nil
}

// Status reads Orka's workload state through readOrkaAgent, as `kmx agent
// show` does, but reports ready only when Orka's Ready condition observes
// the current generation. It is unrelated to `kmx status`'s aggregate governance,
// Ollama, MCP and certificate sections, which remain app-owned.
//
// The caller's context bounds everything this read does: preflight's probe
// and the kubectl read go through a Runner bound to it, and an already
// cancelled context returns before provisioning, so a cancelled status never
// starts fetching a toolchain it is not going to use.
func (a orkaRuntimeAdapter) Status(ctx context.Context, ref agentruntime.AgentRef, _ agentruntime.StatusOptions) (agentruntime.Status, error) {
	if err := a.lifecycleVerbError(a.Capabilities().Status, agentruntime.VerbStatus); err != nil {
		return agentruntime.Status{}, err
	}
	// Orka watches namespaces explicitly, so there is no safe namespace to
	// guess: a defaulted one would report "not found" about a namespace the
	// caller never meant.
	if strings.TrimSpace(ref.Namespace) == "" || strings.TrimSpace(ref.Name) == "" {
		return agentruntime.Status{}, fmt.Errorf("Orka status requires an explicit namespace and Agent name")
	}
	if err := a.statusTargetError(ref); err != nil {
		return agentruntime.Status{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// Checked here rather than left to the child process: preflight may FETCH
	// a missing kubectl before it runs one, and a download is not what a
	// cancelled status should start. Saying so is also the only honest answer
	// — a cancelled child exits like a broken tool, and "kubectl is unusable"
	// would send an operator to fix a machine that is fine.
	if err := ctx.Err(); err != nil {
		return agentruntime.Status{}, fmt.Errorf("Orka status for %s/%s: %w", ref.Namespace, ref.Name, err)
	}
	app := a.app.withRunContext(ctx)
	if err := app.preflight(depKubectl); err != nil {
		return agentruntime.Status{}, err
	}
	agent, err := app.readOrkaAgent(ref.Namespace, ref.Name)
	if err != nil {
		return agentruntime.Status{}, err
	}
	if err := statusIdentityError(ref, agent); err != nil {
		return agentruntime.Status{}, err
	}
	ready := false
	if agent.Status.Ready && agent.Metadata.Generation > 0 {
		for _, condition := range agent.Status.Conditions {
			if condition.Type == "Ready" && condition.Status == "True" && condition.ObservedGeneration == agent.Metadata.Generation {
				ready = true
				break
			}
		}
	}
	return agentruntime.Status{Agent: ref, Fields: []agentruntime.Field{
		{Label: "ready", Value: readyWord(ready)},
		{Label: "active tasks", Value: fmt.Sprintf("%d", agent.Status.ActiveTasks)},
		{Label: "last used", Value: orDash(agent.Status.LastUsed)},
	}}, nil
}

// statusTargetError refuses a reference this adapter cannot report on, before
// it reads anything. Runtime, Context and Kind are optional — a caller that
// holds only a namespace and a name still gets a status — but a value that
// disagrees with the one canonical Orka form is a mistake rather than a hint:
// this read reaches exactly one kube context, always through
// agents.core.orka.ai, so answering anyway would report on a different object
// than the reference names while looking like it had honoured it.
func (a orkaRuntimeAdapter) statusTargetError(ref agentruntime.AgentRef) error {
	if named := agentruntime.ID(strings.TrimSpace(string(ref.Runtime))); named != "" && named != a.ID() {
		return fmt.Errorf("Orka status cannot report on a %q reference", named)
	}
	if kind := strings.TrimSpace(ref.Kind); kind != "" && kind != orkaPlural("Agent") {
		return fmt.Errorf("Orka status reads %s, but the reference names kind %q", orkaPlural("Agent"), kind)
	}
	kubeContext := ""
	if a.app != nil && a.app.Cfg != nil {
		kubeContext = a.app.Cfg.KubeContext
	}
	if target := strings.TrimSpace(ref.Context); target != "" && target != kubeContext {
		return fmt.Errorf("Orka status reads context %q, but the reference names context %q", kubeContext, target)
	}
	return nil
}

// statusIdentityError proves the object that was read is the object the
// reference named. A UID is optional in a reference and authoritative when it
// is present: names are reused, so an Agent deleted and recreated under the
// same name is a different workload, and reporting its state against the old
// reference would say the original one recovered.
func statusIdentityError(ref agentruntime.AgentRef, agent *orkaAgentSpec) error {
	want := strings.TrimSpace(ref.UID)
	if want == "" || agent.Metadata.UID == want {
		return nil
	}
	if agent.Metadata.UID == "" {
		return fmt.Errorf("Orka Agent %s/%s reports no UID, so the UID %s this reference names cannot be confirmed",
			ref.Namespace, ref.Name, want)
	}
	return fmt.Errorf("Orka Agent %s/%s has UID %s, not the %s this reference names; the name now belongs to a different Agent",
		ref.Namespace, ref.Name, agent.Metadata.UID, want)
}

// Evaluate runs one case as exactly one Orka Task against the Agent the
// reference names, and only while that Agent is deployed at the request's
// portable digest: a result is recorded against a revision, so it must never
// be taken from a different one.
//
// It reuses create --task's execution path: a short-lived orka-result-reader
// session over a pinned loopback forward, a probe that the result endpoint
// answers for this Task name before anything is created, one strict create,
// and a wait for a terminal state bounded by ctx's deadline. The Task is never
// retried or cleaned up, because a Task can have side effects.
//
// An error means nothing was executed: the reference, revision or result
// access was refused before any Task was created. Once a create is attempted
// every outcome is a receipt instead — pass or fail when the Task's outcome
// was observed, unknown when it was not, including an ambiguous create. The
// answer is written to the terminal and never into the receipt.
func (a orkaRuntimeAdapter) Evaluate(ctx context.Context, ref agentruntime.AgentRef, request agentruntime.EvaluationRequest) (agentruntime.EvaluationReceipt, error) {
	if err := a.lifecycleVerbError(a.Capabilities().Evaluate, agentruntime.VerbEvaluate); err != nil {
		return agentruntime.EvaluationReceipt{}, err
	}
	if strings.TrimSpace(ref.Namespace) == "" || strings.TrimSpace(ref.Name) == "" || strings.TrimSpace(ref.UID) == "" {
		return agentruntime.EvaluationReceipt{}, fmt.Errorf("Orka evaluate requires an explicit namespace, Agent name and Agent UID")
	}
	if strings.TrimSpace(request.PortableDigest) == "" {
		return agentruntime.EvaluationReceipt{}, fmt.Errorf("Orka evaluate requires the portable digest of the revision under test")
	}
	if strings.TrimSpace(request.CaseID) == "" || strings.TrimSpace(request.Input) == "" || len(request.ExpectContains) == 0 {
		return agentruntime.EvaluationReceipt{}, fmt.Errorf("Orka evaluate requires a case id, an input and at least one expected string")
	}
	if err := a.statusTargetError(ref); err != nil {
		return agentruntime.EvaluationReceipt{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if _, bounded := ctx.Deadline(); !bounded {
		return agentruntime.EvaluationReceipt{}, fmt.Errorf("Orka evaluate requires a per-case deadline")
	}
	if err := ctx.Err(); err != nil {
		return agentruntime.EvaluationReceipt{}, fmt.Errorf("Orka evaluate for %s/%s: %w", ref.Namespace, ref.Name, err)
	}
	app := a.app.withRunContext(ctx)
	if err := app.preflight(depKubectl); err != nil {
		return agentruntime.EvaluationReceipt{}, err
	}
	if err := app.orkaEvaluationRevisionError(ctx, ref, request.PortableDigest); err != nil {
		return agentruntime.EvaluationReceipt{}, err
	}
	suffix, err := randomHex(8)
	if err != nil {
		return agentruntime.EvaluationReceipt{}, err
	}
	name := strings.TrimRight(ref.Name[:min(len(ref.Name), 40)], "-.") + "-eval-" + suffix
	port := a.resultPort
	if port == "" {
		port = "19180"
	}
	// The session's authority notice is printed once by the caller, not once
	// per case.
	quiet := *app
	quiet.Err = io.Discard
	session, err := quiet.openOrkaResultSession(ctx, CreateOptions{Namespace: ref.Namespace, ResultServiceAccount: orkaResultAccount, ResultPort: port})
	if err != nil {
		return agentruntime.EvaluationReceipt{}, err
	}
	defer session.close()
	// Losing the forward or its one connection stops the case; it is never
	// recovered by resubmitting the Task.
	ctx = session.ctx
	if err := session.probe(ctx, ref.Namespace, name); err != nil {
		return agentruntime.EvaluationReceipt{}, err
	}
	receipt := agentruntime.EvaluationReceipt{CaseID: request.CaseID, TaskName: name, Verdict: agentruntime.EvaluationUnknown}
	task := map[string]any{
		"apiVersion": "core.orka.ai/v1alpha1", "kind": "Task",
		"metadata": map[string]any{"name": name, "namespace": ref.Namespace},
		"spec":     map[string]any{"type": "ai", "prompt": request.Input, "agentRef": map[string]any{"name": ref.Name, "namespace": ref.Namespace}, "resources": map[string]any{}},
	}
	id, err := app.createOrkaObject(ctx, ref.Namespace, task)
	if err != nil {
		receipt.Detail = "Task create failed or was ambiguous; it may exist or be running, and was not retried"
		return receipt, nil
	}
	receipt.TaskUID = id.UID
	answer, err := app.waitOrkaTaskResult(ctx, ref.Namespace, id, session)
	var ended *orkaTaskEndedError
	switch {
	case errors.As(err, &ended):
		receipt.Verdict = agentruntime.EvaluationFail
		receipt.Detail = "Task ended in " + ended.Phase
		return receipt, nil
	case errors.Is(err, context.DeadlineExceeded):
		receipt.Detail = "no readable terminal result within the case timeout"
		return receipt, nil
	case err != nil:
		receipt.Detail = "result could not be read: " + err.Error()
		return receipt, nil
	}
	if _, err := fmt.Fprintln(a.app.Out, answer); err != nil {
		receipt.Detail = "the answer could not be written to the terminal"
		return receipt, nil
	}
	receipt.AnswerSHA256 = agentruntime.EvaluationAnswerDigest(answer)
	receipt.Matched, receipt.Missing = agentruntime.MatchExpectations(answer, request.ExpectContains)
	// The answer counts only if the Agent it came from is still the revision
	// under test: a lift that landed while the Task ran makes it unattributable.
	if err := app.orkaEvaluationRevisionError(ctx, ref, request.PortableDigest); err != nil {
		receipt.Detail = "the Agent's revision could not be confirmed after the Task finished"
		return receipt, nil
	}
	if len(receipt.Missing) > 0 {
		receipt.Verdict = agentruntime.EvaluationFail
		receipt.Detail = fmt.Sprintf("answer lacks %d of %d expected strings", len(receipt.Missing), len(request.ExpectContains))
		return receipt, nil
	}
	receipt.Verdict = agentruntime.EvaluationPass
	return receipt, nil
}

// orkaEvaluationRevisionError proves the live Agent is the one the reference
// names, carries the given portable-digest marker, and is Ready for its
// current generation.
func (a *App) orkaEvaluationRevisionError(ctx context.Context, ref agentruntime.AgentRef, portableDigest string) error {
	raw, err := a.orkaCapture(ctx, nil, "-n", ref.Namespace, "get", orkaPlural("Agent"), ref.Name, "--ignore-not-found=true", "-o", "json")
	if err != nil {
		return fmt.Errorf("cannot read Agent %s/%s: %w", ref.Namespace, ref.Name, err)
	}
	live := describeOrkaResource(raw)
	switch {
	case !live.Found:
		return fmt.Errorf("Agent %s/%s is not deployed; lift first", ref.Namespace, ref.Name)
	case live.UID != ref.UID:
		return fmt.Errorf("Agent %s/%s is no longer UID %s; the name now belongs to a different Agent", ref.Namespace, ref.Name, ref.UID)
	case live.PortableDigest != portableDigest:
		return fmt.Errorf("deployed revision differs; lift first")
	case !live.Ready:
		return fmt.Errorf("Agent %s/%s is not Ready for its current generation", ref.Namespace, ref.Name)
	}
	return nil
}

// orkaSpecFromPortable maps the closed portable document onto the existing
// scaffold spec. The document is authoritative for everything it models; only
// the first Task prompt, which is invocation input rather than the Agent's
// definition, comes from the create flags.
//
// spec.model.name becomes the Provider's defaultModel. It is the document's
// one statement of which model to use: the Orka extension does not restate
// it, so a document cannot say two different things about the same model and
// this mapping never has to choose between them.
func orkaSpecFromPortable(portable *agentruntime.PortableAgent, bindings agentruntime.OrkaBindings, opt CreateOptions) (scaffold.OrkaSpec, error) {
	extension := portable.Extensions.Orka
	if extension == nil {
		extension = &agentruntime.OrkaExtension{}
	}
	spec := scaffold.OrkaSpec{
		Name:              portable.Metadata.Name,
		Namespace:         bindings.Namespace,
		Description:       portable.Spec.Description,
		ProviderType:      bindings.Provider.Type,
		Model:             portable.Spec.Model.Name,
		BaseURL:           bindings.Provider.BaseURL,
		AzureDeployment:   bindings.Provider.Azure.DeploymentName,
		AzureAPIVersion:   bindings.Provider.Azure.APIVersion,
		SecretName:        bindings.Provider.SecretRef.Name,
		SecretKey:         bindings.Provider.SecretRef.Key,
		Instructions:      portable.Spec.Instructions,
		TaskPrompt:        opt.Task,
		ProviderRateLimit: orkaRateLimitFromExtension(extension.Provider.RateLimit),
	}
	if extension.Agent != nil {
		for _, tool := range extension.Agent.Tools {
			spec.Tools = append(spec.Tools, tool.Name)
		}
		for _, skill := range extension.Agent.Skills {
			spec.Skills = append(spec.Skills, skill.Name)
		}
		spec.AgentRateLimit = orkaRateLimitFromExtension(extension.Agent.RateLimit)
		if c := extension.Agent.Coordination; c != nil {
			mapped := &scaffold.OrkaCoordination{Enabled: c.Enabled, MaxConcurrentChildren: c.MaxConcurrentChildren, MaxDepth: c.MaxDepth}
			for _, ref := range c.AllowedAgents {
				mapped.AllowedAgents = append(mapped.AllowedAgents, ref.Name)
			}
			spec.Coordination = mapped
		}
	}
	return spec, nil
}

func orkaRateLimitFromExtension(limits *agentruntime.OrkaRateLimit) *scaffold.OrkaRateLimit {
	if limits == nil {
		return nil
	}
	return &scaffold.OrkaRateLimit{RequestsPerMinute: limits.RequestsPerMinute, TokensPerMinute: limits.TokensPerMinute}
}

// orkaRenderedDocuments serializes the bundle's review order into the exact
// per-document bytes the artifact contains and the rendered digest covers,
// marking every document except the value-free Secret skeleton for
// deployment.
func orkaRenderedDocuments(bundle *scaffold.OrkaBundle) ([]agentruntime.Document, error) {
	documents := make([]agentruntime.Document, 0, 4)
	for _, doc := range bundle.Documents() {
		encoded, err := yaml.Marshal(doc)
		if err != nil {
			return nil, fmt.Errorf("render Orka bundle: %w", err)
		}
		if kind, _ := doc["kind"].(string); kind == "Secret" {
			documents = append(documents, agentruntime.ReviewDocument(encoded))
			continue
		}
		documents = append(documents, agentruntime.ApplyDocument(encoded))
	}
	return documents, nil
}

// orkaBundleFromRendered decodes an immutable RenderedBundle back into the
// bundle shape every unchanged Orka helper already takes. It parses the
// rendered bytes; it never regenerates them, so a Task's random identity
// survives exactly as rendered.
//
// The two lists are read for exactly what they mean. The artifact's
// review-only documents are the prerequisites an operator provisions
// separately, and there must be exactly one — the Secret the Provider reads.
// With none, the proof would fall back to whatever --secret this command
// happened to carry; with several, which one is proved would depend on
// iteration order. The objects creation writes are taken from the bundle's
// own explicit deploy list, in its order, so nothing here can turn the
// artifact's full review order into a bulk apply.
func orkaBundleFromRendered(rendered agentruntime.RenderedBundle) (*scaffold.OrkaBundle, error) {
	if rendered.AdapterID() != agentruntime.Orka {
		return nil, fmt.Errorf("rendered bundle targets runtime %q, not %q", rendered.AdapterID(), agentruntime.Orka)
	}
	artifact, deploy := rendered.Documents(), rendered.DeployDocuments()
	review := orkaReviewDocuments(artifact, deploy)
	if len(review) != 1 {
		return nil, fmt.Errorf("rendered Orka bundle carries %d review-only documents; deployment proves exactly one separately provisioned Secret", len(review))
	}
	if len(deploy) < 2 || len(deploy) > 3 {
		return nil, fmt.Errorf("rendered Orka bundle marks %d documents for deployment; expected a Provider, an Agent and an optional Task", len(deploy))
	}
	bundle := &scaffold.OrkaBundle{}
	secret, err := decodeOrkaDocument(review[0], "Secret")
	if err != nil {
		return nil, err
	}
	bundle.Secret = secret
	for i, raw := range deploy {
		kind := []string{"Provider", "Agent", "Task"}[i]
		doc, err := decodeOrkaDocument(raw, kind)
		if err != nil {
			return nil, err
		}
		switch kind {
		case "Provider":
			bundle.Provider = doc
		case "Agent":
			bundle.Agent = doc
		case "Task":
			bundle.Task = doc
		}
	}
	// Validate is the bundle's own schema-independent invariant, and it is
	// what ties the skeleton to the rest: it refuses a bundle whose documents
	// disagree about namespace, whose Provider.spec.secretRef.name is not the
	// skeleton's name, or whose Secret carries a value. Without it, a
	// substituted skeleton would have Deploy prove that some other Secret
	// exists and then write a Provider that cannot authenticate.
	if err := bundle.Validate(); err != nil {
		return nil, fmt.Errorf("rendered Orka bundle is not internally consistent: %w", err)
	}
	return bundle, nil
}

// orkaReviewDocuments returns the artifact documents that are not marked for
// deployment. The neutral bundle exposes the two lists rather than a flag per
// document, so membership is what distinguishes them.
func orkaReviewDocuments(artifact, deploy [][]byte) [][]byte {
	remaining := append([][]byte(nil), deploy...)
	var review [][]byte
	for _, doc := range artifact {
		matched := false
		for i, candidate := range remaining {
			if bytes.Equal(candidate, doc) {
				remaining = append(remaining[:i], remaining[i+1:]...)
				matched = true
				break
			}
		}
		if !matched {
			review = append(review, doc)
		}
	}
	return review
}

// decodeOrkaDocument parses one rendered document and proves it is the kind
// its position claims, so a reordered or substituted bundle is refused
// instead of deployed.
func decodeOrkaDocument(raw []byte, want string) (map[string]any, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("rendered Orka %s document is not valid YAML: %w", want, err)
	}
	if kind, _ := doc["kind"].(string); kind != want {
		return nil, fmt.Errorf("rendered Orka document is %q where %q was expected", kind, want)
	}
	return doc, nil
}

func orkaObjectNamespace(doc map[string]any) string {
	metadata, _ := doc["metadata"].(map[string]any)
	namespace, _ := metadata["namespace"].(string)
	return namespace
}

// RenderOrkaBundleFile renders agent.yaml against caller-supplied target
// bindings without selecting an offline schema. A portable bundle created for
// v0.1.3 may contain fields removed in v0.2.0; lift validates against the
// destination's installed CRDs before any write, while status only compares
// live resources. Neither path reads a cluster during this render.
func RenderOrkaBundleFile(path string, bindings agentruntime.OrkaBindings) (agentruntime.RenderedBundle, error) {
	if err := scaffold.RefuseKeyShapes(path); err != nil {
		return agentruntime.RenderedBundle{}, fmt.Errorf("refusing credential-shaped bundle path")
	}
	source, err := os.ReadFile(filepath.Join(path, "agent.yaml"))
	if err != nil {
		return agentruntime.RenderedBundle{}, fmt.Errorf("read portable agent: %w", err)
	}
	adapter := orkaRuntimeAdapter{create: &CreateOptions{}, bindings: &bindings}
	prepared, err := agentruntime.PreparePortableRender(source, adapter)
	if err != nil {
		return agentruntime.RenderedBundle{}, err
	}
	rendered, _, err := adapter.renderOrka(prepared)
	return rendered, err
}

// orkaBindingsFromCreate constructs target-only bindings from creation flags.
func orkaBindingsFromCreate(opt CreateOptions) agentruntime.OrkaBindings {
	secretKey := opt.SecretKey
	if secretKey == "" {
		secretKey = scaffold.DefaultOrkaSecretKey
	}
	return agentruntime.OrkaBindings{Namespace: opt.Namespace, Provider: agentruntime.OrkaProviderBindings{
		Type: opt.ProviderType, BaseURL: opt.BaseURL,
		Azure:     agentruntime.OrkaAzureBindings{DeploymentName: opt.AzureDeployment, APIVersion: opt.AzureAPIVersion},
		SecretRef: agentruntime.OrkaSecretRefBindings{Name: opt.Secret, Key: secretKey},
	}}
}

// portableOrkaSource encodes only this create's behavior flags into the
// closed revision Render parses. Instructions receive their scaffold default
// here; a missing Secret key is defaulted in orkaBindingsFromCreate instead.
// Target flags are validated by the shorthand but cannot alter the bytes.
func portableOrkaSource(opt CreateOptions) ([]byte, error) {
	agentLimits, err := parseOrkaLimits(opt.AgentRequestsPerMinute, opt.AgentTokensPerMinute, "agent")
	if err != nil {
		return nil, err
	}
	providerLimits, err := parseOrkaLimits(opt.ProviderRequestsPerMinute, opt.ProviderTokensPerMinute, "provider")
	if err != nil {
		return nil, err
	}
	instructions, err := resolveOrkaInstructionText(opt)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(instructions) == "" {
		instructions = scaffold.DefaultOrkaInstructions(opt.Name)
	}
	portable, err := agentruntime.EncodeOrkaShorthand(agentruntime.OrkaShorthand{
		Name:              opt.Name,
		Namespace:         opt.Namespace,
		Instructions:      instructions,
		Description:       opt.Description,
		ProviderType:      opt.ProviderType,
		Model:             opt.Model,
		BaseURL:           opt.BaseURL,
		AzureDeployment:   opt.AzureDeployment,
		AzureAPIVersion:   opt.AzureAPIVersion,
		SecretName:        opt.Secret,
		SecretKey:         opt.SecretKey,
		Tools:             orkaNameList(opt.Tools),
		Skills:            orkaNameList(opt.Skills),
		Coordination:      opt.Coordination,
		AllowedAgents:     append([]string(nil), opt.AllowedAgents...),
		AgentRateLimit:    orkaRateLimitExtension(agentLimits),
		ProviderRateLimit: orkaRateLimitExtension(providerLimits),
	})
	if err != nil {
		return nil, err
	}
	return portable.Source(), nil
}

// orkaRateLimitExtension converts parsed limit flags into the portable shape.
// An entirely unset limit is absent, never an empty block a renderer then has
// to decide what to do with.
func orkaRateLimitExtension(limits *scaffold.OrkaRateLimit) *agentruntime.OrkaRateLimit {
	if limits == nil || limits.RequestsPerMinute == nil && limits.TokensPerMinute == nil {
		return nil
	}
	return &agentruntime.OrkaRateLimit{RequestsPerMinute: limits.RequestsPerMinute, TokensPerMinute: limits.TokensPerMinute}
}

func orkaNameList(value string) []string {
	if value == "" {
		return nil
	}
	items := strings.Split(value, ",")
	for i := range items {
		items[i] = strings.TrimSpace(items[i])
	}
	return items
}
