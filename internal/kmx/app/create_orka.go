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
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

// CreateAgent validates the complete Orka artifact before any emission. Offline
// paths deliberately do not load kubeconfig, provision tools or query a cluster.
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
	if err := validateOrkaResultOptions(&opt); err != nil {
		return err
	}
	if err := resolveOrkaInstructions(&opt); err != nil {
		return err
	}
	bundle, err := createOrkaBundle(opt)
	if err != nil {
		return err
	}
	if !opt.NoApply {
		if err := a.preflight(depKubectl); err != nil {
			return err
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		return a.createOrkaOnline(ctx, opt, bundle)
	}
	validator, err := orkaschema.Offline(opt.SchemaTarget)
	if err != nil {
		return err
	}
	if err := validateOrkaBundle(bundle, validator); err != nil {
		return err
	}
	document, err := bundle.YAML(validator.Provenance())
	if err != nil {
		return err
	}
	if err := a.emitOrka(opt, document); err != nil {
		return err
	}
	a.notef("Orka bundle not applied. Schema: %s", validator.Provenance())
	a.notef("Use the namespace the Orka controller watches. Provision the Secret key separately; never write the skeleton.\nCreate Provider only, wait for current-generation Ready; then Agent and wait; then optional Task.\nLocal schema validation does not test admission, result access or execution.")
	return nil
}

func validateOrkaResultOptions(opt *CreateOptions) error {
	// Scan before validation so error paths never echo credential-shaped flags.
	for _, value := range []string{opt.Out, opt.Instructions, opt.SchemaTarget, opt.ResultServiceAccount, opt.OrkaAPIService, opt.ResultPort, opt.AgentRequestsPerMinute, opt.AgentTokensPerMinute, opt.ProviderRequestsPerMinute, opt.ProviderTokensPerMinute} {
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
func createOrkaBundle(opt CreateOptions) (*scaffold.OrkaBundle, error) {
	agentLimits, err := parseOrkaLimits(opt.AgentRequestsPerMinute, opt.AgentTokensPerMinute, "agent")
	if err != nil {
		return nil, err
	}
	providerLimits, err := parseOrkaLimits(opt.ProviderRequestsPerMinute, opt.ProviderTokensPerMinute, "provider")
	if err != nil {
		return nil, err
	}
	instructions := opt.InstructionText
	if opt.Instructions != "" {
		if instructions != "" {
			return nil, fmt.Errorf("supply only one instructions source")
		}
		if opt.instructionFileText == nil {
			return nil, fmt.Errorf("instructions file must be resolved before validation")
		}
		instructions = *opt.instructionFileText
	}
	names := func(value string) []string {
		if value == "" {
			return nil
		}
		items := strings.Split(value, ",")
		for i := range items {
			items[i] = strings.TrimSpace(items[i])
		}
		return items
	}
	return scaffold.GenerateOrka(scaffold.OrkaSpec{
		Name: opt.Name, Namespace: opt.Namespace, Description: opt.Description,
		ProviderType: opt.ProviderType, Model: opt.Model, BaseURL: opt.BaseURL,
		SecretName: opt.Secret, SecretKey: opt.SecretKey, Instructions: instructions,
		Tools: names(opt.Tools), Skills: names(opt.Skills), TaskPrompt: opt.Task,
		AgentRateLimit: agentLimits, ProviderRateLimit: providerLimits,
	})
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
