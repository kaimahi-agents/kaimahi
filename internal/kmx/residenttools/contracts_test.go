package residenttools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
)

func contractFixture(t *testing.T) Plan {
	t.Helper()
	commands := localtools.CommandPlan{
		ConfigRevision: "sha256:" + strings.Repeat("a", 64),
		Revision:       "sha256:" + strings.Repeat("b", 64),
		Commands:       []localtools.CommandConfig{{Name: "check"}},
	}
	plan, err := NewBoundedProcessPlan(commands)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestBoundedPlanSeparatesPortableContractAndLocalBinding(t *testing.T) {
	plan := contractFixture(t)
	contract, err := json.Marshal(plan.Contract)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"directory", "credential", "endpoint", "bounded-process", "/tmp/", "/usr/"} {
		if strings.Contains(strings.ToLower(string(contract)), forbidden) {
			t.Fatalf("portable contract contains %q: %s", forbidden, contract)
		}
	}
	if plan.Binding.EnvironmentClass != "local" || plan.Binding.AgentRuntime.Backend != ResidentProcess || plan.Binding.ToolRuntime.Backend != BoundedProcess || plan.Binding.ToolRuntime.Filesystem.Mode != "host-user" || plan.Binding.ToolRuntime.Network.Incoming != "broker-channel-only" || plan.Binding.ToolRuntime.Network.Outgoing != "unrestricted" || plan.ContractDigest() == "" || plan.BindingDigest() == "" || plan.Revision() == "" {
		t.Fatalf("plan=%+v", plan)
	}
	changed := plan
	changed.Contract.Tools[0].Effect = "read-only"
	if changed.Revision() != "" {
		t.Fatal("binding accepted a changed portable contract")
	}
}

func TestContainerBindingRepresentsStrongerPolicyWithoutAdmission(t *testing.T) {
	plan := contractFixture(t)
	binding := plan.Binding
	binding.ToolRuntime.Backend = AppleContainer
	binding.ToolRuntime.Filesystem = FilesystemPolicy{Mode: "declared-mounts", WorkingDirectory: "/workspace"}
	binding.ToolRuntime.Network = NetworkPolicy{Mode: "isolated", Incoming: "broker-channel-only", Outgoing: "none"}
	if err := binding.Validate(); err != nil {
		t.Fatal(err)
	}
	plan.Backend, plan.Binding = AppleContainer, binding
	if plan.Revision() == "" {
		t.Fatal("portable plan could not identify an Apple-container binding")
	}
	binding.ToolRuntime.Network = NetworkPolicy{Mode: "isolated", Incoming: "broker-channel-only", Outgoing: "brokered", Origins: []string{"https://api.example.com"}, MaxRequestsPerCall: 2, MaxRequestsPerSession: 32, MaxResponseBytes: 64 << 10}
	if err := binding.Validate(); err != nil {
		t.Fatal(err)
	}
	binding.AgentRuntime = RuntimeBinding{Backend: AppleContainer, Filesystem: FilesystemPolicy{Mode: "declared-mounts", WorkingDirectory: "/agent"}, Network: NetworkPolicy{Mode: "isolated", Incoming: "client-channel-only", Outgoing: "brokered", Origins: []string{"https://models.example.com"}, MaxRequestsPerCall: 2, MaxRequestsPerSession: 64, MaxResponseBytes: 1 << 20}}
	if err := binding.Validate(); err != nil {
		t.Fatal(err)
	}
	binding.ToolRuntime.Network.Origins = []string{"http://api.example.com"}
	if err := binding.Validate(); err == nil {
		t.Fatal("brokered network accepted a non-HTTPS origin")
	}
	// Runtime admission remains separate: process_unix accepts only the
	// implemented bounded-process backend and must not silently downgrade.
}
