package app

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

func (a *App) createKagentAgent(opt CreateOptions) error {
	if opt.Out == "-" {
		opt.NoApply = true
	}
	if err := validateKagentCreateOptions(&opt); err != nil {
		return err
	}
	if err := resolveOrkaInstructions(&opt); err != nil {
		return err
	}
	tools, err := parseKagentCreateTools(opt.Tools)
	if err != nil {
		return err
	}
	source, bindings, err := portableKagentSource(opt, tools)
	if err != nil {
		return err
	}
	bindingSource, err := agentruntime.EncodeKagentBindings(bindings)
	if err != nil {
		return err
	}
	adapter := kagentRuntimeAdapter{app: a, create: &opt, bindings: &bindings}
	prepared, err := agentruntime.PreparePortableRender(source, adapter)
	if err != nil {
		return err
	}
	rendered, err := adapter.Render(a.operationContext(), prepared, agentruntime.RenderOptions{})
	if err != nil {
		return err
	}
	document, err := scaffold.KagentArtifact(rendered.Documents())
	if err != nil {
		return err
	}
	bundlePath := kagentBundlePath(opt)
	if err := refuseKagentBundleArtifactOverlap(opt, bundlePath); err != nil {
		return err
	}
	if bundlePath != "" {
		if err := preflightKagentBundle(bundlePath, source, bindingSource); err != nil {
			return err
		}
	}
	keepIdentical := !opt.NoApply && !opt.DryRun
	if err := preflightKagentArtifact(opt, document, keepIdentical); err != nil {
		return err
	}
	persist := func() error {
		if bundlePath == "" {
			return nil
		}
		return writeKagentBundle(bundlePath, source, bindingSource)
	}
	if opt.NoApply {
		if err := persist(); err != nil {
			return err
		}
		if err := a.emitKagentArtifact(opt, document, false); err != nil {
			return err
		}
		a.notef("Kagent bundle not applied. It targets exact Kagent v0.10.2.")
		a.notef("Provision the Secret key separately; never apply its metadata-only skeleton. Use kmx agent create online to validate the live exact version and dependencies, create ModelConfig, wait for current-generation Accepted, then create Agent and wait for Accepted and Ready.")
		return nil
	}
	if err := a.preflight(depKubectl); err != nil {
		return err
	}
	// Keep the portable revision even when an online prerequisite or a later
	// create fails. Kagent deployment remains create-only; the files are for
	// review, not authority to adopt a partially created resource on rerun.
	if err := persist(); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(a.operationContext(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	_, err = adapter.Deploy(ctx, rendered, agentruntime.DeployOptions{})
	return err
}

func validateKagentCreateOptions(opt *CreateOptions) error {
	for _, value := range []string{
		opt.Name, opt.Namespace, opt.Description, opt.ProviderType, opt.Model,
		opt.BaseURL, opt.AzureDeployment, opt.AzureAPIVersion, opt.Secret, opt.SecretKey, opt.Instructions, opt.InstructionText,
		opt.Tools, opt.Skills, opt.Task, opt.AgentRequestsPerMinute,
		opt.AgentTokensPerMinute, opt.ProviderRequestsPerMinute,
		opt.ProviderTokensPerMinute, opt.ResultServiceAccount, opt.OrkaAPIService,
		opt.ResultPort, opt.SchemaTarget, opt.Out, opt.BundlePath, opt.KagentRuntime,
	} {
		if err := scaffold.RefuseKeyShapes(value); err != nil {
			return fmt.Errorf("refusing credential-shaped Kagent create input; supply references, never credentials")
		}
	}
	if opt.NoApply && opt.DryRun {
		return fmt.Errorf("--no-apply (including --out -) and --dry-run cannot be used together")
	}
	if opt.Task != "" && (opt.NoApply || opt.DryRun) {
		return fmt.Errorf("Kagent --task requires an applying create; it cannot be used with --no-apply, --out -, or --dry-run")
	}
	if opt.Task != "" && strings.TrimSpace(opt.Task) == "" {
		return fmt.Errorf("Kagent --task must not be blank")
	}
	if opt.Skills != "" {
		return fmt.Errorf("--skills is not supported by Kagent create")
	}
	if opt.Coordination || len(opt.AllowedAgents) > 0 {
		return fmt.Errorf("--coordination and --allowed-agent are Orka-only and are not supported by Kagent create")
	}
	for _, flag := range []struct{ name, value string }{
		{"agent-requests-per-minute", opt.AgentRequestsPerMinute},
		{"agent-tokens-per-minute", opt.AgentTokensPerMinute},
		{"provider-requests-per-minute", opt.ProviderRequestsPerMinute},
		{"provider-tokens-per-minute", opt.ProviderTokensPerMinute},
	} {
		if flag.value != "" {
			return fmt.Errorf("--%s is not supported by Kagent create", flag.name)
		}
	}
	if opt.SchemaTarget != "" {
		return fmt.Errorf("--schema-target is Orka-only and is not supported by Kagent create")
	}
	if opt.AzureDeployment != "" {
		return fmt.Errorf("--azure-deployment is Orka-only and is not supported by Kagent create")
	}
	if opt.AzureAPIVersion != "" {
		return fmt.Errorf("--azure-api-version is Orka-only and is not supported by Kagent create")
	}
	if opt.ResultServiceAccount != "" {
		return fmt.Errorf("--result-service-account is Orka-only and is not supported by Kagent create")
	}
	if opt.OrkaAPIService != "" && opt.OrkaAPIService != "orka-api" {
		return fmt.Errorf("--orka-api-service is Orka-only and cannot be changed for Kagent create")
	}
	if opt.ResultPort != "" && opt.ResultPort != "19180" {
		return fmt.Errorf("--result-port is Orka-only and cannot be changed for Kagent create")
	}
	if opt.KagentRuntime == "" {
		opt.KagentRuntime = "go"
	}
	if opt.KagentRuntime != "go" && opt.KagentRuntime != "python" {
		return fmt.Errorf("--kagent-runtime must be go or python")
	}
	return nil
}

func parseKagentCreateTools(value string) ([]agentruntime.KagentMCPBinding, error) {
	if value == "" {
		return nil, nil
	}
	server, names, ok := strings.Cut(value, ":")
	if !ok || strings.TrimSpace(server) == "" || strings.TrimSpace(names) == "" || strings.Contains(names, ":") {
		return nil, fmt.Errorf("Kagent --tools must be exactly one server:tool1,tool2 binding")
	}
	server = strings.TrimSpace(server)
	items := strings.Split(names, ",")
	for i := range items {
		items[i] = strings.TrimSpace(items[i])
		if items[i] == "" {
			return nil, fmt.Errorf("Kagent --tools must be exactly one server:tool1,tool2 binding with a nonempty allowlist")
		}
	}
	return []agentruntime.KagentMCPBinding{{
		Server: agentruntime.KagentMCPServerRef{
			Kind: scaffold.KagentRemoteMCPServerKind,
			Name: server,
		},
		ToolNames: items,
	}}, nil
}

func portableKagentSource(opt CreateOptions, tools []agentruntime.KagentMCPBinding) ([]byte, agentruntime.KagentBindings, error) {
	instructions, err := resolveOrkaInstructionText(opt)
	if err != nil {
		return nil, agentruntime.KagentBindings{}, err
	}
	if strings.TrimSpace(instructions) == "" {
		instructions = fmt.Sprintf("You are %s, a declarative Kagent agent running on Kubernetes. Answer briefly and in plain text, and say plainly when you do not know something.", opt.Name)
	}
	portable, err := agentruntime.EncodeKagentShorthand(agentruntime.KagentShorthand{
		Name: opt.Name, Namespace: opt.Namespace, Description: opt.Description,
		Instructions: instructions, Runtime: opt.KagentRuntime,
		ProviderType: opt.ProviderType, Model: opt.Model, BaseURL: opt.BaseURL,
		SecretName: opt.Secret, SecretKey: opt.SecretKey, Tools: tools,
	})
	if err != nil {
		return nil, agentruntime.KagentBindings{}, err
	}
	key := opt.SecretKey
	if key == "" {
		key = scaffold.DefaultKagentSecretKey
	}
	bindings := agentruntime.KagentBindings{
		APIVersion: agentruntime.KagentBindingsAPIVersion,
		Kind:       agentruntime.KagentBindingsKind,
		Namespace:  opt.Namespace,
		ModelConfig: agentruntime.KagentModelConfigBindings{
			Provider: opt.ProviderType,
			BaseURL:  opt.BaseURL,
			SecretRef: agentruntime.KagentSecretRefBindings{
				Name: opt.Secret,
				Key:  key,
			},
		},
	}
	if err := agentruntime.ValidateKagentBindings(bindings); err != nil {
		return nil, agentruntime.KagentBindings{}, err
	}
	return portable.Source(), bindings, nil
}

func kagentArtifactPath(opt CreateOptions) string {
	if opt.Out != "" {
		return opt.Out
	}
	return filepath.Join("agents", opt.Name+".yaml")
}

func kagentBundlePath(opt CreateOptions) string {
	if opt.BundlePath != "" {
		return opt.BundlePath
	}
	if opt.Out == "-" {
		return ""
	}
	return filepath.Join("agents", opt.Name)
}

func kagentArtifactExistsError(path string) error {
	return fmt.Errorf("%s already exists - refusing to overwrite it; keep the existing Kagent artifact or choose another --out path", path)
}

func preflightKagentArtifact(opt CreateOptions, document string, keepIdentical bool) error {
	if opt.Out == "-" {
		return nil
	}
	path := kagentArtifactPath(opt)
	if err := scaffold.RefuseKeyShapes(path); err != nil {
		return fmt.Errorf("refusing credential-shaped Kagent artifact path")
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if keepIdentical && info.Mode().IsRegular() {
		existing, readErr := os.ReadFile(path)
		if readErr == nil && string(existing) == document {
			return nil
		}
	}
	return kagentArtifactExistsError(path)
}

func (a *App) emitKagentArtifact(opt CreateOptions, document string, keepIdentical bool) error {
	if opt.Out == "-" {
		_, err := fmt.Fprint(a.Out, document)
		return err
	}
	path := kagentArtifactPath(opt)
	if keepIdentical {
		if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() {
			if existing, err := os.ReadFile(path); err == nil && string(existing) == document {
				a.notef("kept %s; it matches this Kagent render", path)
				return nil
			}
		}
	}
	if err := scaffold.WriteNew(path, document); err != nil {
		if errors.Is(err, os.ErrExist) {
			return kagentArtifactExistsError(path)
		}
		return err
	}
	a.notef("wrote %s", path)
	return nil
}

func refuseKagentBundleArtifactOverlap(opt CreateOptions, bundlePath string) error {
	if bundlePath == "" || opt.Out == "-" {
		return nil
	}
	bundle, err := resolveKagentPath(bundlePath)
	if err != nil {
		return err
	}
	artifact, err := resolveKagentPath(kagentArtifactPath(opt))
	if err != nil {
		return err
	}
	for _, pair := range [][2]string{{bundle, artifact}, {artifact, bundle}} {
		rel, err := filepath.Rel(pair[0], pair[1])
		if err != nil {
			return err
		}
		if rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return fmt.Errorf("rendered Kagent artifact cannot be inside the bundle or contain it")
		}
	}
	return nil
}

func resolveKagentPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	for parent := absolute; ; parent = filepath.Dir(parent) {
		resolved, err := filepath.EvalSymlinks(parent)
		if err == nil {
			suffix, err := filepath.Rel(parent, absolute)
			if err != nil {
				return "", err
			}
			return filepath.Join(resolved, suffix), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		if _, statErr := os.Lstat(parent); statErr == nil || !errors.Is(statErr, os.ErrNotExist) {
			return "", fmt.Errorf("cannot resolve Kagent bundle or artifact path: %w", err)
		}
		if filepath.Dir(parent) == parent {
			return "", err
		}
	}
}

func preflightKagentBundle(path string, agent, bindings []byte) error {
	if err := scaffold.RefuseKeyShapes(path); err != nil {
		return fmt.Errorf("refusing credential-shaped Kagent bundle path")
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("Kagent bundle path %s must be a directory, not a link", path)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return fmt.Errorf("inspect Kagent bundle directory: %w", err)
	}
	files := map[string][]byte{"agent.yaml": agent, "bindings.yaml": bindings}
	seen := map[string]bool{}
	for _, entry := range entries {
		if entry.Name() == "receipts" {
			entryInfo, err := entry.Info()
			if err != nil {
				return fmt.Errorf("inspect Kagent bundle receipts: %w", err)
			}
			if !entryInfo.IsDir() || entryInfo.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("Kagent bundle receipts must be a directory, not a link")
			}
			continue
		}
		expected, ok := files[entry.Name()]
		if !ok {
			if err := scaffold.RefuseKeyShapes(entry.Name()); err != nil {
				return fmt.Errorf("Kagent bundle directory contains a credential-shaped unexpected entry")
			}
			return fmt.Errorf("Kagent bundle directory contains unexpected entry %s", entry.Name())
		}
		entryInfo, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect Kagent bundle %s: %w", entry.Name(), err)
		}
		if !entryInfo.Mode().IsRegular() {
			return fmt.Errorf("Kagent bundle %s must be a regular file", entry.Name())
		}
		existing, err := os.ReadFile(filepath.Join(path, entry.Name()))
		if err != nil {
			return fmt.Errorf("read Kagent bundle %s: %w", entry.Name(), err)
		}
		if !bytes.Equal(existing, expected) {
			return fmt.Errorf("Kagent bundle %s differs; refusing to overwrite it", entry.Name())
		}
		seen[entry.Name()] = true
	}
	for _, name := range []string{"agent.yaml", "bindings.yaml"} {
		if !seen[name] {
			return fmt.Errorf("Kagent bundle is incomplete: missing %s; review or remove it before retrying", name)
		}
	}
	return nil
}

func writeKagentBundle(path string, agent, bindings []byte) error {
	if err := preflightKagentBundle(path, agent, bindings); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create Kagent bundle parent: %w", err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		if !os.IsExist(err) {
			return fmt.Errorf("create Kagent bundle directory: %w", err)
		}
		return preflightKagentBundle(path, agent, bindings)
	}
	files := map[string][]byte{"agent.yaml": agent, "bindings.yaml": bindings}
	for _, name := range []string{"agent.yaml", "bindings.yaml"} {
		if err := scaffold.WriteNew(filepath.Join(path, name), string(files[name])); err != nil {
			return fmt.Errorf("write Kagent bundle %s (partial bundle may remain): %w", name, err)
		}
	}
	return nil
}
