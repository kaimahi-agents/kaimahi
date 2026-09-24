package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/orkaschema"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

// CreateAgent validates the complete artifact before any emission. Offline
// paths deliberately do not load kubeconfig, provision tools or query a
// cluster. Creation is routed through the shared runtime seam: the selected
// runtime's adapter renders the portable document and then deploys those
// exact bytes.
//
// The portable document is produced here, before either path runs, because
// it is the one input every runtime renders from: parsing an explicit --file
// (or encoding the shorthand flags) needs no cluster, while resolving an
// omitted --runtime reads one. A document that cannot be rendered at all is
// therefore reported as itself rather than as a detection failure against a
// cluster this create was never going to reach.
func (a *App) CreateAgent(opt CreateOptions) error {
	if opt.Out == "-" {
		opt.NoApply = true
	}
	if opt.NoApply && opt.DryRun {
		return fmt.Errorf("--no-apply (including --out -) and --dry-run cannot be used together")
	}
	if opt.SchemaTarget != "" && !opt.NoApply {
		return fmt.Errorf("--schema-target is offline only; online creation uses installed CRDs")
	}
	if err := validateCreateRuntimeSelection(opt); err != nil {
		return err
	}
	if err := validateOrkaResultOptions(&opt); err != nil {
		return err
	}
	if err := resolveOrkaInstructions(&opt); err != nil {
		return err
	}
	portable, err := a.portableCreateDocument(opt)
	if err != nil {
		return err
	}
	if opt.NoApply {
		return a.createAgentOffline(opt, portable)
	}
	return a.createAgentOnline(opt, portable)
}

// createAgentOffline renders the artifact and writes it for review. No
// runtime detection happens here: detection reads a cluster, and this path
// never contacts one, so an omitted runtime keeps selecting Orka and its
// pinned offline schema. An explicit --runtime still overrides that.
func (a *App) createAgentOffline(opt CreateOptions, portable *agentruntime.PortableAgent) error {
	adapter, err := a.createRuntimeAdapter(context.Background(), opt)
	if err != nil {
		return err
	}
	orka, isOrka := adapter.(orkaRuntimeAdapter)
	if !isOrka {
		return fmt.Errorf("runtime %s cannot write an offline artifact", adapter.ID())
	}
	rendered, provenance, err := orka.renderOrkaBundle(*portable)
	if err != nil {
		return err
	}
	bundle, err := orkaBundleFromRendered(rendered)
	if err != nil {
		return err
	}
	document, err := bundle.YAML(provenance)
	if err != nil {
		return err
	}
	if err := a.emitOrka(opt, document); err != nil {
		return err
	}
	a.notef("Orka bundle not applied. Schema: %s", provenance)
	a.notef("Use the namespace the Orka controller watches. Provision the Secret key separately; never write the skeleton.\nCreate Provider only, wait for current-generation Ready; then Agent and wait; then optional Task.\nLocal schema validation does not test admission, result access or execution.")
	return nil
}

// createAgentOnline renders and then deploys the exact rendered bytes.
//
// The dependency check and runtime resolution come after the document is in
// hand (CreateAgent): an omitted --runtime is resolved by shared platform
// detection, which reads the cluster, so anything decidable from the
// document alone is already decided. Which runtime validates these inputs is
// still not known until the platform is, so the render itself stays here.
func (a *App) createAgentOnline(opt CreateOptions, portable *agentruntime.PortableAgent) error {
	if err := a.preflight(depKubectl); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(a.operationContext(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	adapter, err := a.createRuntimeAdapter(ctx, opt)
	if err != nil {
		return err
	}
	rendered, err := adapter.Render(ctx, *portable, agentruntime.RenderOptions{})
	if err != nil {
		return err
	}
	_, err = adapter.Deploy(ctx, rendered, agentruntime.DeployOptions{})
	return err
}

// createRuntimeRegistry holds exactly the runtimes this build can be asked to
// create with. Legacy kagent is registered so that an explicit --runtime
// kagent resolves to its own declared capabilities — and therefore to the one
// shared typed unsupported-verb error — rather than to an unknown runtime.
//
// The Orka adapter is configured with this create's flags, which is what
// makes it declare Render and Deploy at all (runtime_orka.go).
func (a *App) createRuntimeRegistry(opt CreateOptions) (*agentruntime.Registry, error) {
	return agentruntime.NewRegistry(orkaRuntimeAdapter{app: a, create: &opt}, kagentRuntimeAdapter{app: a})
}

// createRuntimeAdapter resolves the runtime this create targets and proves it
// declares Render. An unknown runtime and an unsupported verb are both typed
// errors from the shared registry's own model; neither ever falls back to a
// different runtime.
func (a *App) createRuntimeAdapter(ctx context.Context, opt CreateOptions) (agentruntime.LifecycleAdapter, error) {
	registry, err := a.createRuntimeRegistry(opt)
	if err != nil {
		return nil, err
	}
	id := agentruntime.ID(opt.Runtime)
	if id == "" {
		if id, err = a.detectCreateRuntime(ctx, opt); err != nil {
			return nil, err
		}
	}
	adapter, err := registry.Lookup(id)
	if err != nil {
		return nil, err
	}
	lifecycle, ok := adapter.(agentruntime.LifecycleAdapter)
	if !ok || !lifecycle.Capabilities().Render {
		return nil, &agentruntime.UnsupportedVerbError{Runtime: id, Verb: agentruntime.VerbRender}
	}
	return lifecycle, nil
}

// detectCreateRuntime applies DESIGN.md §1's shared platform detection to an
// omitted --runtime: Orka first, then kagent-v1, with both install
// prerequisites named when neither is installed.
//
// Recorded conservative ruling: an offline create contacts no cluster at all
// — kmx deliberately never loads kubeconfig, provisions tools or queries a
// cluster to write a reviewable artifact — so there is nothing to detect
// against. An omitted runtime there keeps selecting Orka and its pinned
// offline schema, which is also the only runtime with an offline artifact
// path. An explicit --runtime still overrides that, and every create that
// does contact a cluster (including --dry-run) uses the detector.
func (a *App) detectCreateRuntime(ctx context.Context, opt CreateOptions) (agentruntime.ID, error) {
	if opt.NoApply {
		return agentruntime.Orka, nil
	}
	return a.detectPlatformRuntime(ctx)
}

// createFileConflicts lists DESIGN.md §4's approved matrix: with --file every
// portable-defined input conflicts, because the document already states it.
// Deployment and output flags (--task, --result-service-account,
// --orka-api-service, --result-port, --out, --no-apply, --dry-run and Orka's
// --schema-target) describe what to do with the document, not what it says,
// and stay legal.
func createFileConflicts(opt CreateOptions) []string {
	// Both instruction sources map to the same flag, so they are reported
	// once: --instructions names a file, and its resolved text is the same
	// input by the time it reaches here.
	instructions := opt.Instructions
	if instructions == "" {
		instructions = opt.InstructionText
	}
	portable := []struct {
		flag  string
		value string
	}{
		{"--namespace", opt.Namespace},
		{"--description", opt.Description},
		{"--provider-type", opt.ProviderType},
		{"--model", opt.Model},
		{"--secret", opt.Secret},
		{"--secret-key", opt.SecretKey},
		{"--base-url", opt.BaseURL},
		{"--instructions", instructions},
		{"--tools", opt.Tools},
		{"--skills", opt.Skills},
		{"--agent-requests-per-minute", opt.AgentRequestsPerMinute},
		{"--agent-tokens-per-minute", opt.AgentTokensPerMinute},
		{"--provider-requests-per-minute", opt.ProviderRequestsPerMinute},
		{"--provider-tokens-per-minute", opt.ProviderTokensPerMinute},
	}
	var conflicts []string
	for _, flag := range portable {
		if flag.value != "" {
			conflicts = append(conflicts, flag.flag)
		}
	}
	return conflicts
}

// validateCreateRuntimeSelection enforces the file and runtime halves of the
// approved matrix before anything is read, rendered or emitted.
func validateCreateRuntimeSelection(opt CreateOptions) error {
	if agentruntime.ID(opt.Runtime) == agentruntime.KagentV1 && opt.File == "" {
		return fmt.Errorf("--runtime %s requires --file naming a portable agent document with a kagent extension", agentruntime.KagentV1)
	}
	if opt.File == "" {
		return nil
	}
	if opt.Name == "" {
		return fmt.Errorf("--file requires a name argument matching the document's metadata.name")
	}
	if conflicts := createFileConflicts(opt); len(conflicts) > 0 {
		return fmt.Errorf("--file already defines %s; supply those values in the document, not as flags", strings.Join(conflicts, ", "))
	}
	return nil
}

func validateOrkaResultOptions(opt *CreateOptions) error {
	// Scan before validation so error paths never echo credential-shaped flags.
	for _, value := range []string{opt.Out, opt.Instructions, opt.SchemaTarget, opt.ResultServiceAccount, opt.OrkaAPIService, opt.ResultPort, opt.AgentRequestsPerMinute, opt.AgentTokensPerMinute, opt.ProviderRequestsPerMinute, opt.ProviderTokensPerMinute, opt.File, opt.Runtime} {
		if err := scaffold.RefuseKeyShapes(value); err != nil {
			return fmt.Errorf("refusing credential-shaped create input; supply references, never credentials")
		}
	}
	if opt.OrkaAPIService == "" {
		opt.OrkaAPIService = "orka-api"
	}
	if opt.ResultPort == "" {
		opt.ResultPort = "19180"
	}
	// Services use DNS labels with an alphabetic first character (RFC 1035),
	// not the embedded kagent Agent reservations in scaffold.ValidateName.
	if err := scaffold.ValidateNamespace(opt.OrkaAPIService); err != nil || opt.OrkaAPIService[0] < 'a' || opt.OrkaAPIService[0] > 'z' {
		return fmt.Errorf("--orka-api-service must be a valid Service name")
	}
	if opt.ResultServiceAccount != "" {
		if err := scaffold.ValidateObjectName(opt.ResultServiceAccount); err != nil {
			return fmt.Errorf("--result-service-account must be a valid ServiceAccount name")
		}
	}
	port, err := strconv.ParseUint(opt.ResultPort, 10, 16)
	if err != nil || port == 0 {
		return fmt.Errorf("--result-port must be an integer from 1 to 65535")
	}
	opt.ResultPort = strconv.FormatUint(port, 10)
	if opt.Task == "" && opt.ResultServiceAccount != "" {
		return fmt.Errorf("--result-service-account requires --task")
	}
	if opt.Task != "" && !opt.NoApply && !opt.DryRun && opt.ResultServiceAccount == "" {
		return fmt.Errorf("applying --task requires --result-service-account naming an existing account in the selected namespace")
	}
	return nil
}

// Resolve user file I/O only before terminal collection or named creation.
// No signal handler is installed here: a stalled read remains interruptible by
// the process's default signal behavior, without a blocked reader goroutine or
// touching borrowed stdin. Wizard validation and final generation reuse bytes.
func resolveOrkaInstructions(opt *CreateOptions) error {
	if opt.Instructions == "" {
		return nil
	}
	if opt.InstructionText != "" {
		return fmt.Errorf("supply only one instructions source")
	}
	if err := scaffold.RefuseKeyShapes(opt.Instructions); err != nil {
		return fmt.Errorf("refusing credential-shaped create input; supply references, never credentials")
	}
	if opt.instructionFileText != nil {
		return nil
	}
	body, err := os.ReadFile(opt.Instructions)
	if err != nil {
		return fmt.Errorf("cannot read the instructions file")
	}
	text := string(body)
	opt.instructionFileText = &text
	return nil
}

// Bundle construction is memory-only, including when called from Bubble Tea's
// synchronous Update. Callers resolve file inputs before entering that loop.
// It remains the wizard's local validation helper; creation itself renders
// through the runtime adapter (runtime_orka.go).
func createOrkaBundle(opt CreateOptions) (*scaffold.OrkaBundle, error) {
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
	return scaffold.GenerateOrka(scaffold.OrkaSpec{
		Name: opt.Name, Namespace: opt.Namespace, Description: opt.Description,
		ProviderType: opt.ProviderType, Model: opt.Model, BaseURL: opt.BaseURL,
		SecretName: opt.Secret, SecretKey: opt.SecretKey, Instructions: instructions,
		Tools: orkaNameList(opt.Tools), Skills: orkaNameList(opt.Skills), TaskPrompt: opt.Task,
		AgentRateLimit: agentLimits, ProviderRateLimit: providerLimits,
	})
}

// resolveOrkaInstructionText returns the one instruction source this create
// supplies. An empty result means the caller supplied none, and generation's
// own default applies.
func resolveOrkaInstructionText(opt CreateOptions) (string, error) {
	instructions := opt.InstructionText
	if opt.Instructions != "" {
		if instructions != "" {
			return "", fmt.Errorf("supply only one instructions source")
		}
		if opt.instructionFileText == nil {
			return "", fmt.Errorf("instructions file must be resolved before validation")
		}
		instructions = *opt.instructionFileText
	}
	return instructions, nil
}

// orkaNameList splits a comma-separated tool or skill flag into the explicit
// names it lists. Empty entries are preserved so that validation refuses them
// rather than silently dropping one.
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

func parseOrkaLimits(requests, tokens, owner string) (*scaffold.OrkaRateLimit, error) {
	limits := &scaffold.OrkaRateLimit{}
	if requests != "" {
		n, err := strconv.ParseInt(requests, 10, 32)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("--%s-requests-per-minute must be a positive int32", owner)
		}
		value := int32(n)
		limits.RequestsPerMinute = &value
	}
	if tokens != "" {
		n, err := strconv.ParseInt(tokens, 10, 64)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("--%s-tokens-per-minute must be a positive int64", owner)
		}
		limits.TokensPerMinute = &n
	}
	return limits, nil
}

func validateOrkaBundle(bundle *scaffold.OrkaBundle, validator *orkaschema.Validator) error {
	if err := bundle.Validate(); err != nil {
		return err
	}
	for _, doc := range bundle.Documents()[1:] {
		if err := validator.Validate(doc); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) emitOrka(opt CreateOptions, document string) error {
	if opt.Out == "-" {
		_, err := fmt.Fprint(a.Out, document)
		return err
	}
	path := opt.Out
	if path == "" {
		path = filepath.Join("agents", opt.Name+".yaml")
	}
	if err := scaffold.WriteNew(path, document); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%s already exists — refusing to overwrite it.\n"+
				"  Keep the existing artifact or choose another --out <path>.\n"+
				"  Do not bulk-apply an Orka bundle or write its Secret skeleton.\n"+
				"  Create Provider only and wait for current-generation Ready; then Agent and wait; then optional Task.", path)
		}
		return err
	}
	a.notef("wrote %s", path)
	return nil
}
