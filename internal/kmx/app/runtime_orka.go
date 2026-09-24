// Orka lifecycle adapter: DESIGN.md §3 routes Orka creation through the
// shared runtime seam without changing a byte of what it renders or the
// order in which it deploys.
//
//   - Render delegates to the existing scaffold generator and, for offline
//     artifacts, the existing pinned schema validator. It mints the optional
//     Task's random identity exactly once, and returns it inside an immutable
//     RenderedBundle.
//   - Deploy consumes only that bundle. It applies exactly the bundle's
//     explicit deploy documents — never the value-free Secret skeleton the
//     artifact also carries — by decoding those exact rendered bytes back
//     into the existing *scaffold.OrkaBundle shape and handing them to the
//     unchanged staged online path, which keeps the mutation guard, installed
//     CRD schema validation, collision checks, Secret-key proof, strict
//     server dry-runs, artifact emission and Provider → Ready → Agent →
//     Ready → optional Task/result ordering exactly as before.
//
// Online creation validates against the target cluster's installed CRDs, so
// that step stays inside the staged deploy path where it has always run —
// after the mutation guard, never before it, and with no offline fallback.
// Render therefore applies the pinned snapshot only when it is producing an
// offline artifact, which is the one mode with no installed schema to defer
// to (orkaRenderMode records both halves of that rule).
package app

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/orkaschema"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
	"go.yaml.in/yaml/v3"
)

// orkaRenderMode names which schema gate a render applies. It is derived once
// from this create's own shape rather than inferred at each use, so the two
// modes' different guarantees stay explicit.
type orkaRenderMode int

const (
	// orkaRenderOnline renders for a deploy against a live cluster. The
	// authoritative structural schema there is the one that cluster actually
	// serves, so it is validated inside the guarded staged deploy, which
	// reads the installed CRDs and refuses any offline fallback. Validating
	// here against a pinned snapshot instead would either duplicate that gate
	// or, worse, refuse a document the target cluster's own schema accepts.
	orkaRenderOnline orkaRenderMode = iota
	// orkaRenderOffline renders a reviewable artifact for a cluster kmx is
	// not allowed to read. The pinned snapshot named by --schema-target is
	// then the only structural schema available, and is applied here.
	orkaRenderOffline
)

// renderMode reports which schema gate this adapter's renders apply. An
// offline create (--no-apply, including --out -) writes an artifact without
// contacting a cluster; every other create renders for a deploy. Only a
// configured adapter renders at all, so create is never nil here.
func (a orkaRuntimeAdapter) renderMode() orkaRenderMode {
	if a.create.NoApply {
		return orkaRenderOffline
	}
	return orkaRenderOnline
}

// Capabilities declares exactly the lifecycle verbs this adapter instance
// implements. Render and Deploy (Task 5) act on this create's own flags, so
// they are declared only by an instance a create command configured: the
// chat and status registrations build an adapter with no create behind it,
// and an unconfigured instance that advertised Render would render some
// other create's agent from an empty CreateOptions. Status (Task 6) reads
// only the AgentRef it is given and is always available. Evaluate stays
// permanently unsupported, because a native Orka Task supplies no frozen
// target revision and kmx must not fabricate one (DESIGN.md §3).
// Session-level flags remain Session's own concern
// (orkaRuntimeSession.Capabilities), not this static declaration.
func (a orkaRuntimeAdapter) Capabilities() agentruntime.Capabilities {
	configured := a.create != nil
	return agentruntime.Capabilities{Render: configured, Deploy: configured, Status: true}
}

// Render is the neutral seam over renderOrkaBundle. The schema provenance the
// emitted artifact header records is not part of the neutral RenderedBundle,
// so the create command calls renderOrkaBundle directly; both entry points
// enforce the same declared capability.
func (a orkaRuntimeAdapter) Render(_ context.Context, portable agentruntime.PortableAgent, _ agentruntime.RenderOptions) (agentruntime.RenderedBundle, error) {
	rendered, _, err := a.renderOrkaBundle(portable)
	return rendered, err
}

// renderOrkaBundle generates the Orka bundle from the portable document and
// returns it as an immutable RenderedBundle plus, for offline artifacts, the
// provenance of the schema it was validated against.
//
// Every render runs scaffold's schema-independent structural invariant, which
// is stronger than either CRD schema: generation refuses a bundle whose
// documents disagree about names, namespaces or references, or that carries
// anything shaped like a credential. On top of that, each mode applies the
// structural schema that is authoritative for it (see orkaRenderMode).
//
// The rendered documents are the bundle's review order — Secret skeleton,
// Provider, Agent and the optional Task — because those exact bytes are what
// the emitted artifact contains and what the rendered digest must cover. Only
// Provider, Agent and Task are marked for deployment: the skeleton is a
// review-only document naming a prerequisite, and the separately provisioned
// Secret is recorded as one, so Deploy proves it and never writes it.
func (a orkaRuntimeAdapter) renderOrkaBundle(portable agentruntime.PortableAgent) (agentruntime.RenderedBundle, string, error) {
	if err := lifecycleVerbError(a.ID(), a.Capabilities().Render, agentruntime.VerbRender); err != nil {
		return agentruntime.RenderedBundle{}, "", err
	}
	// The portable source is this render's authored identity. Refuse to
	// render an identity-less document rather than digest a constant.
	source := portable.Source()
	if len(source) == 0 {
		return agentruntime.RenderedBundle{}, "", fmt.Errorf("portable agent %q carries no source bytes; its portable digest would identify nothing", portable.Metadata.Name)
	}
	spec, err := orkaSpecFromPortable(portable, *a.create)
	if err != nil {
		return agentruntime.RenderedBundle{}, "", err
	}
	// GenerateOrka runs OrkaBundle.Validate itself, so the structural
	// invariant holds for both modes before any schema is consulted.
	bundle, err := scaffold.GenerateOrka(spec)
	if err != nil {
		return agentruntime.RenderedBundle{}, "", err
	}
	provenance := ""
	if a.renderMode() == orkaRenderOffline {
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
	rendered, err := agentruntime.NewRenderedBundle(
		a.ID(),
		source,
		documents,
		[]agentruntime.Prerequisite{{Kind: "Secret", Namespace: spec.Namespace, Name: orkaObjectName(bundle.Secret)}},
		agentruntime.TargetResolution{Runtime: a.ID(), Namespace: spec.Namespace},
		nil,
	)
	if err != nil {
		return agentruntime.RenderedBundle{}, "", err
	}
	return rendered, provenance, nil
}

// Deploy applies exactly what Render produced. It never regenerates the
// bundle, so the optional Task keeps the single random identity minted at
// render time, and the namespace, Agent name and Secret it acts on come from
// the immutable rendered bytes rather than from the flags this adapter also
// carries.
//
// The returned AgentRef carries the created Agent's UID. A --dry-run deploy
// creates nothing, so it returns the same reference with an empty UID.
func (a orkaRuntimeAdapter) Deploy(ctx context.Context, rendered agentruntime.RenderedBundle, _ agentruntime.DeployOptions) (agentruntime.AgentRef, error) {
	if err := lifecycleVerbError(a.ID(), a.Capabilities().Deploy, agentruntime.VerbDeploy); err != nil {
		return agentruntime.AgentRef{}, err
	}
	bundle, err := orkaBundleFromRendered(rendered)
	if err != nil {
		return agentruntime.AgentRef{}, err
	}
	opt := *a.create
	opt.Namespace = rendered.Target().Namespace
	opt.Name = orkaObjectName(bundle.Agent)
	secret, err := orkaSecretPrerequisite(rendered, bundle, opt.Namespace)
	if err != nil {
		return agentruntime.AgentRef{}, err
	}
	opt.Secret = secret
	agent, err := a.app.createOrkaStaged(ctx, opt, bundle)
	if err != nil {
		return agentruntime.AgentRef{}, err
	}
	kubeContext := ""
	if a.app.Cfg != nil {
		kubeContext = a.app.Cfg.KubeContext
	}
	return agentruntime.AgentRef{Runtime: a.ID(), Context: kubeContext, Namespace: opt.Namespace, Kind: orkaPlural("Agent"), Name: opt.Name, UID: agent.UID}, nil
}

// orkaSecretPrerequisite returns the single Secret this rendered bundle
// requires Deploy to prove exists before it writes anything.
//
// The staged deploy proves one Secret, so the bundle must name exactly one,
// and it must be this bundle's own. Each rejected shape is a different way
// the proof would be about the wrong object: with none, opt.Secret would
// keep whatever --secret this command happened to carry (or nothing at all);
// with several, which one is proved would depend on iteration order; and one
// naming another namespace or another object would prove a Secret the
// rendered documents never reference. The name is checked against the
// rendered Secret skeleton rather than the flags, because the immutable
// rendered bytes are what this deploy applies.
func orkaSecretPrerequisite(rendered agentruntime.RenderedBundle, bundle *scaffold.OrkaBundle, namespace string) (string, error) {
	var secrets []agentruntime.Prerequisite
	for _, prerequisite := range rendered.Prerequisites() {
		if prerequisite.Kind == "Secret" {
			secrets = append(secrets, prerequisite)
		}
	}
	if len(secrets) != 1 {
		return "", fmt.Errorf("rendered Orka bundle names %d Secret prerequisites; deployment proves exactly one separately provisioned Secret", len(secrets))
	}
	secret := secrets[0]
	if secret.Namespace != namespace {
		return "", fmt.Errorf("rendered Orka bundle's Secret prerequisite is in namespace %q, but the bundle targets %q", secret.Namespace, namespace)
	}
	if want := orkaObjectName(bundle.Secret); secret.Name != want {
		return "", fmt.Errorf("rendered Orka bundle's Secret prerequisite names %q, but its rendered documents reference Secret %q", secret.Name, want)
	}
	return secret.Name, nil
}

// Status wraps Orka's own workload state — the exact Ready/active-tasks/
// last-used read `kmx agent show` already exercises via readOrkaAgent — and
// nothing else. Orka has no template/instance split, so DESIGN.md §1's
// PairStatus.Fields carries that workload state directly and Instance stays
// nil; there is never a merged readiness boolean. This is unrelated to and
// does not touch `kmx status`'s aggregate governance/Ollama/MCP/certificate
// sections, which remain entirely app-owned (status.go).
func (a orkaRuntimeAdapter) Status(_ context.Context, ref agentruntime.AgentRef, _ agentruntime.StatusOptions) (agentruntime.LifecycleStatus, error) {
	if err := lifecycleVerbError(a.ID(), a.Capabilities().Status, agentruntime.VerbStatus); err != nil {
		return agentruntime.LifecycleStatus{}, err
	}
	if strings.TrimSpace(ref.Namespace) == "" || strings.TrimSpace(ref.Name) == "" {
		return agentruntime.LifecycleStatus{}, fmt.Errorf("orka status requires an explicit namespace and Agent name")
	}
	if err := a.app.preflight(depKubectl); err != nil {
		return agentruntime.LifecycleStatus{}, err
	}
	agent, err := a.app.readOrkaAgent(ref.Namespace, ref.Name)
	if err != nil {
		return agentruntime.LifecycleStatus{}, err
	}
	fields := []agentruntime.Field{
		{Label: "ready", Value: readyWord(agent.Status.Ready)},
		{Label: "active tasks", Value: fmt.Sprintf("%d", agent.Status.ActiveTasks)},
		{Label: "last used", Value: orDash(agent.Status.LastUsed)},
	}
	return agentruntime.LifecycleStatus{Pair: agentruntime.PairStatus{Fields: fields}}, nil
}

// Evaluate is permanently unsupported for Orka (DESIGN.md §3: "native Orka
// Tasks do not supply the required frozen target revision, and kmx must not
// fabricate one"), so Capabilities().Evaluate is never expected to flip true.
func (a orkaRuntimeAdapter) Evaluate(context.Context, agentruntime.AgentRef, agentruntime.EvaluationRequest) (agentruntime.EvaluationReceipt, error) {
	if err := lifecycleVerbError(a.ID(), a.Capabilities().Evaluate, agentruntime.VerbEvaluate); err != nil {
		return agentruntime.EvaluationReceipt{}, err
	}
	return agentruntime.EvaluationReceipt{}, fmt.Errorf("orka evaluate: not yet implemented")
}

// orkaSpecFromPortable maps the closed portable document onto the existing
// scaffold spec. The document is authoritative for everything it models; only
// the description and the first Task prompt — which the closed schema
// deliberately does not carry, and which the approved matrix keeps legal
// alongside --file — come from the create flags.
func orkaSpecFromPortable(portable agentruntime.PortableAgent, opt CreateOptions) (scaffold.OrkaSpec, error) {
	extension := portable.Extensions.Orka
	if extension == nil {
		return scaffold.OrkaSpec{}, fmt.Errorf("portable agent %q has no extensions.orka block; Orka creation cannot invent a provider, Secret reference or namespace", portable.Metadata.Name)
	}
	if portable.Spec.Model.Name != extension.Provider.DefaultModel {
		return scaffold.OrkaSpec{}, fmt.Errorf("spec.model.name %q and extensions.orka.provider.defaultModel %q name different models; refusing to choose one", portable.Spec.Model.Name, extension.Provider.DefaultModel)
	}
	spec := scaffold.OrkaSpec{
		Name:              portable.Metadata.Name,
		Namespace:         extension.Namespace,
		Description:       opt.Description,
		ProviderType:      extension.Provider.Type,
		Model:             extension.Provider.DefaultModel,
		BaseURL:           extension.Provider.BaseURL,
		SecretName:        extension.Provider.SecretRef.Name,
		SecretKey:         extension.Provider.SecretRef.Key,
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
	}
	return spec, nil
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
// existing bundle shape every unchanged Orka helper already takes. It parses
// the rendered bytes; it never regenerates them, so a Task's random identity
// survives exactly as rendered.
//
// The two lists are read for exactly what they mean: the artifact's first
// document is the review-only Secret skeleton, and the objects creation
// writes are taken from the bundle's own explicit deploy list, in its order.
// Nothing here can turn the artifact's full review order into a bulk apply.
func orkaBundleFromRendered(rendered agentruntime.RenderedBundle) (*scaffold.OrkaBundle, error) {
	if rendered.Adapter() != agentruntime.Orka {
		return nil, fmt.Errorf("rendered bundle targets runtime %q, not %q", rendered.Adapter(), agentruntime.Orka)
	}
	artifact, deploy := rendered.Documents(), rendered.DeployDocuments()
	if len(artifact) < 3 || len(artifact) > 4 {
		return nil, fmt.Errorf("rendered Orka bundle has %d documents; expected Secret, Provider, Agent and an optional Task", len(artifact))
	}
	if len(deploy) != len(artifact)-1 {
		return nil, fmt.Errorf("rendered Orka bundle marks %d of %d documents for deployment; exactly the value-free Secret skeleton must be review-only", len(deploy), len(artifact))
	}
	skeleton := artifact[0]
	for _, doc := range deploy {
		if bytes.Equal(doc, skeleton) {
			return nil, fmt.Errorf("rendered Orka bundle marks the value-free Secret skeleton for deployment")
		}
	}
	bundle := &scaffold.OrkaBundle{}
	secret, err := decodeOrkaDocument(skeleton, "Secret")
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
	return bundle, nil
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

// portableCreateDocument produces the closed portable document this create
// renders from: an explicit --file is parsed strictly and must name the same
// Agent as the command; otherwise the Orka shorthand flags are
// deterministically encoded, exactly as DESIGN.md §2 requires for the
// portable bundle digest.
func (a *App) portableCreateDocument(opt CreateOptions) (*agentruntime.PortableAgent, error) {
	if opt.File != "" {
		data, err := os.ReadFile(opt.File)
		if err != nil {
			return nil, fmt.Errorf("cannot read the portable agent file")
		}
		portable, err := agentruntime.ParsePortableAgent(data)
		if err != nil {
			return nil, err
		}
		if portable.Metadata.Name != opt.Name {
			return nil, fmt.Errorf("portable agent metadata.name %q does not match the requested name %q", portable.Metadata.Name, opt.Name)
		}
		return portable, nil
	}
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
	// The closed portable schema requires instructions, and generation
	// supplies its own default for an empty flag. State that same default
	// here so the document can never disagree with the rendered bytes.
	if strings.TrimSpace(instructions) == "" {
		instructions = scaffold.DefaultOrkaInstructions(opt.Name)
	}
	// The Secret key defaults the same way, and for the same reason: two
	// creates that generation renders identically must carry one portable
	// digest, not one per spelling of the same input.
	secretKey := opt.SecretKey
	if secretKey == "" {
		secretKey = scaffold.DefaultOrkaSecretKey
	}
	return agentruntime.EncodeOrkaShorthand(agentruntime.OrkaShorthand{
		Name:              opt.Name,
		Namespace:         opt.Namespace,
		Instructions:      instructions,
		ProviderType:      opt.ProviderType,
		Model:             opt.Model,
		BaseURL:           opt.BaseURL,
		SecretName:        opt.Secret,
		SecretKey:         secretKey,
		Tools:             orkaNameList(opt.Tools),
		Skills:            orkaNameList(opt.Skills),
		AgentRateLimit:    orkaRateLimitExtension(agentLimits),
		ProviderRateLimit: orkaRateLimitExtension(providerLimits),
	})
}

// orkaRateLimitExtension converts parsed limit flags into the portable
// shape. An entirely unset limit is absent, never an empty block.
func orkaRateLimitExtension(limits *scaffold.OrkaRateLimit) *agentruntime.OrkaRateLimitExtension {
	if limits == nil || limits.RequestsPerMinute == nil && limits.TokensPerMinute == nil {
		return nil
	}
	return &agentruntime.OrkaRateLimitExtension{RequestsPerMinute: limits.RequestsPerMinute, TokensPerMinute: limits.TokensPerMinute}
}

func orkaRateLimitFromExtension(limits *agentruntime.OrkaRateLimitExtension) *scaffold.OrkaRateLimit {
	if limits == nil {
		return nil
	}
	return &scaffold.OrkaRateLimit{RequestsPerMinute: limits.RequestsPerMinute, TokensPerMinute: limits.TokensPerMinute}
}
