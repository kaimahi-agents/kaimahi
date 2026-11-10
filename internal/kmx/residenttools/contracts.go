package residenttools

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/agent-primitives/sessions/api"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
)

const (
	ToolContractVersion = "kmx.tool-contract/v1alpha1"
	LocalBindingVersion = "kmx.local-tool-binding/v1alpha1"

	ResidentProcess = "resident-process"
	AppleContainer  = "apple-container-vm"
	LinuxNative     = "linux-native"
	RootlessOCI     = "rootless-oci"
)

// ToolContract is portable model-facing authority. It contains no executable,
// endpoint, credential, filesystem path or environment-specific resolution.
type ToolContract struct {
	Version  string         `json:"version"`
	Provider Provider       `json:"provider"`
	Tools    []ContractTool `json:"tools"`
}

type Provider struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}

type ContractTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Effect      string         `json:"effect"`
	Approval    string         `json:"approval"`
}

// LocalBinding resolves one exact contract for one local runtime. It is private
// KMX state and is never AgentSuite content or remote deployment authority.
type LocalBinding struct {
	Version          string           `json:"version"`
	EnvironmentClass string           `json:"environmentClass"`
	ContractDigest   string           `json:"contractDigest"`
	ResolutionDigest string           `json:"resolutionDigest"`
	AgentRuntime     RuntimeBinding   `json:"agentRuntime"`
	ToolRuntime      RuntimeBinding   `json:"toolRuntime"`
	Limits           InvocationLimits `json:"limits"`
	CredentialRefs   []CredentialRef  `json:"credentialRefs,omitempty"`
}

type RuntimeBinding struct {
	Backend    string           `json:"backend"`
	Filesystem FilesystemPolicy `json:"filesystem"`
	Network    NetworkPolicy    `json:"network"`
}

type FilesystemPolicy struct {
	Mode             string `json:"mode"`
	WorkingDirectory string `json:"workingDirectory"`
	AmbientRead      bool   `json:"ambientRead"`
	AmbientWrite     bool   `json:"ambientWrite"`
}

type NetworkPolicy struct {
	Mode                  string   `json:"mode"`
	Incoming              string   `json:"incoming"`
	Outgoing              string   `json:"outgoing"`
	Origins               []string `json:"origins,omitempty"`
	MaxRequestsPerCall    int      `json:"maxRequestsPerCall,omitempty"`
	MaxRequestsPerSession int      `json:"maxRequestsPerSession,omitempty"`
	MaxResponseBytes      int      `json:"maxResponseBytes,omitempty"`
}

type InvocationLimits struct {
	MaxConcurrent       int `json:"maxConcurrent"`
	MaxCallsPerTurn     int `json:"maxCallsPerTurn"`
	MaxCallsPerSession  int `json:"maxCallsPerSession"`
	MaxCallsPerInstance int `json:"maxCallsPerInstance"`
	TimeoutMilliseconds int `json:"timeoutMilliseconds"`
}

// CredentialRef identifies a resolver-owned credential without containing its
// value. No credential resolver is enabled by the initial command-tool slice.
type CredentialRef struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

func digest(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("sha256:%x", sha256.Sum256(raw)), nil
}

func validDigest(value string) bool {
	hex := strings.TrimPrefix(value, "sha256:")
	return strings.HasPrefix(value, "sha256:") && len(hex) == 64 && strings.Trim(hex, "0123456789abcdef") == ""
}

func (c ToolContract) Validate() error {
	if c.Version != ToolContractVersion || !localtools.ValidToolName(c.Provider.ID) || c.Provider.Version != "1.0.0" || len(c.Tools) == 0 || len(c.Tools) > 32 {
		return errors.New("invalid resident tool contract")
	}
	prior := ""
	for _, tool := range c.Tools {
		if !localtools.ValidToolName(tool.Name) || tool.Name <= prior || tool.Description == "" || tool.InputSchema == nil || tool.Effect != "unspecified" || tool.Approval != "policy-controlled" {
			return errors.New("invalid resident tool contract")
		}
		prior = tool.Name
	}
	definitions := make([]api.ToolDefinition, 0, len(c.Tools))
	for _, tool := range c.Tools {
		definitions = append(definitions, api.ToolDefinition{Name: tool.Name, Description: tool.Description, InputSchema: tool.InputSchema})
	}
	if _, err := localtools.CopyDefinitions(definitions); err != nil {
		return errors.New("invalid resident tool contract")
	}
	return nil
}

func (c ToolContract) Digest() (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	return digest(c)
}

func (b LocalBinding) Validate() error {
	if b.Version != LocalBindingVersion || b.EnvironmentClass != "local" || !validDigest(b.ContractDigest) || !validDigest(b.ResolutionDigest) {
		return errors.New("invalid resident local tool binding")
	}
	if validateRuntimeBinding(b.AgentRuntime, true) != nil || validateRuntimeBinding(b.ToolRuntime, false) != nil {
		return errors.New("invalid resident local tool binding")
	}
	limits := b.Limits
	if limits.MaxConcurrent < 1 || limits.MaxConcurrent > 8 || limits.MaxCallsPerTurn < 1 || limits.MaxCallsPerTurn > localtools.MaxCalls || limits.MaxCallsPerSession < limits.MaxCallsPerTurn || limits.MaxCallsPerSession > 1024 || limits.MaxCallsPerInstance < limits.MaxCallsPerSession || limits.MaxCallsPerInstance > 65536 || limits.TimeoutMilliseconds < 1000 || limits.TimeoutMilliseconds > 120000 {
		return errors.New("invalid resident local invocation limits")
	}
	if len(b.CredentialRefs) > 16 {
		return errors.New("invalid resident local credential references")
	}
	seen := map[string]bool{}
	for _, ref := range b.CredentialRefs {
		if !localtools.ValidToolName(ref.Name) || seen[ref.Name] || ref.Type != "gh-auth" && ref.Type != "os-keychain" && ref.Type != "env" && ref.Type != "workload-identity" {
			return errors.New("invalid resident local credential references")
		}
		seen[ref.Name] = true
	}
	return nil
}

func validateRuntimeBinding(runtime RuntimeBinding, agent bool) error {
	host := runtime.Backend == ResidentProcess || runtime.Backend == BoundedProcess
	if agent && runtime.Backend == BoundedProcess || !agent && runtime.Backend == ResidentProcess || !host && runtime.Backend != AppleContainer && runtime.Backend != LinuxNative && runtime.Backend != RootlessOCI {
		return errors.New("invalid local runtime backend")
	}
	incoming := "broker-channel-only"
	working := "agent-subtree"
	if agent {
		incoming, working = "client-channel-only", "snapshot-cache"
	}
	if host {
		if runtime.Filesystem.Mode != "host-user" || runtime.Filesystem.WorkingDirectory != working || !runtime.Filesystem.AmbientRead || !runtime.Filesystem.AmbientWrite || runtime.Network.Mode != "host-user" || runtime.Network.Incoming != incoming || runtime.Network.Outgoing != "unrestricted" || len(runtime.Network.Origins) != 0 || runtime.Network.MaxRequestsPerCall != 0 || runtime.Network.MaxRequestsPerSession != 0 || runtime.Network.MaxResponseBytes != 0 {
			return errors.New("invalid host-user runtime policy")
		}
		return nil
	}
	if runtime.Filesystem.Mode != "declared-mounts" || runtime.Filesystem.AmbientRead || runtime.Filesystem.AmbientWrite || runtime.Network.Mode != "isolated" || runtime.Network.Incoming != incoming || runtime.Network.Outgoing != "none" && runtime.Network.Outgoing != "brokered" {
		return errors.New("invalid isolated runtime policy")
	}
	if runtime.Network.Outgoing == "none" {
		if len(runtime.Network.Origins) != 0 || runtime.Network.MaxRequestsPerCall != 0 || runtime.Network.MaxRequestsPerSession != 0 || runtime.Network.MaxResponseBytes != 0 {
			return errors.New("invalid isolated runtime network policy")
		}
		return nil
	}
	if len(runtime.Network.Origins) == 0 || len(runtime.Network.Origins) > 32 || runtime.Network.MaxRequestsPerCall < 1 || runtime.Network.MaxRequestsPerCall > 32 || runtime.Network.MaxRequestsPerSession < runtime.Network.MaxRequestsPerCall || runtime.Network.MaxRequestsPerSession > 4096 || runtime.Network.MaxResponseBytes < 1 || runtime.Network.MaxResponseBytes > 4<<20 {
		return errors.New("invalid brokered runtime network policy")
	}
	prior := ""
	for _, origin := range runtime.Network.Origins {
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" && parsed.Path != "/" || origin <= prior {
			return errors.New("invalid brokered runtime network policy")
		}
		prior = origin
	}
	return nil
}

func (b LocalBinding) Digest() (string, error) {
	if err := b.Validate(); err != nil {
		return "", err
	}
	return digest(b)
}

func boundedProcessContract(plan localtools.CommandPlan) (ToolContract, error) {
	definitions, err := localtools.CommandDefinitions(plan)
	if err != nil {
		return ToolContract{}, err
	}
	tools := make([]ContractTool, 0, len(definitions))
	for _, definition := range definitions {
		tools = append(tools, ContractTool{Name: definition.Name, Description: definition.Description, InputSchema: definition.InputSchema, Effect: "unspecified", Approval: "policy-controlled"})
	}
	slices.SortFunc(tools, func(a, b ContractTool) int { return strings.Compare(a.Name, b.Name) })
	contract := ToolContract{Version: ToolContractVersion, Provider: Provider{ID: "local-command", Version: "1.0.0"}, Tools: tools}
	return contract, contract.Validate()
}

// NewBoundedProcessPlan adapts the current fixed commandTools source into the
// portable-contract/private-binding split without claiming sandbox isolation.
func NewBoundedProcessPlan(commands localtools.CommandPlan) (Plan, error) {
	contract, err := boundedProcessContract(commands)
	if err != nil {
		return Plan{}, err
	}
	contractDigest, err := contract.Digest()
	if err != nil {
		return Plan{}, err
	}
	binding := LocalBinding{
		Version: LocalBindingVersion, EnvironmentClass: "local", ContractDigest: contractDigest, ResolutionDigest: commands.Revision,
		AgentRuntime: RuntimeBinding{Backend: ResidentProcess, Filesystem: FilesystemPolicy{Mode: "host-user", WorkingDirectory: "snapshot-cache", AmbientRead: true, AmbientWrite: true}, Network: NetworkPolicy{Mode: "host-user", Incoming: "client-channel-only", Outgoing: "unrestricted"}},
		ToolRuntime:  RuntimeBinding{Backend: BoundedProcess, Filesystem: FilesystemPolicy{Mode: "host-user", WorkingDirectory: "agent-subtree", AmbientRead: true, AmbientWrite: true}, Network: NetworkPolicy{Mode: "host-user", Incoming: "broker-channel-only", Outgoing: "unrestricted"}},
		Limits:       InvocationLimits{MaxConcurrent: 1, MaxCallsPerTurn: localtools.MaxCalls, MaxCallsPerSession: 64, MaxCallsPerInstance: 1024, TimeoutMilliseconds: 30000},
	}
	if err := binding.Validate(); err != nil {
		return Plan{}, err
	}
	plan := Plan{Backend: BoundedProcess, Contract: contract, Binding: binding, Commands: commands}
	if err := plan.validate(); err != nil {
		return Plan{}, err
	}
	return plan, nil
}
