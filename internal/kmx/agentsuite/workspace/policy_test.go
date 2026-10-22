package workspace_test

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	suitecore "github.com/agent-primitives/suite/core"
	"github.com/agent-primitives/suite/policy"
	suiteoras "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"github.com/opencontainers/go-digest"
)

const policyEndpointA = `{"address":"10.20.30.40","port":443,"protocol":"TCP"}`
const policyEndpointB = `{"address":"10.20.30.41","port":80,"protocol":"TCP"}`

func networkPolicy(endpoints string) string {
	return `{"network":{"egressAllow":[` + endpoints + `]},"requiredCapabilities":["network/egress-allow/v1"],"version":"agentsuite.policy/v2"}`
}
func declarePolicy(t *testing.T, root, id, document string) {
	t.Helper()
	member := filepath.Join(root, "agents", id, "member.yaml")
	writeFile(t, member, string(readFile(t, member))+"policy: policy.json\n")
	writeFile(t, filepath.Join(root, "agents", id, "policy.json"), document)
}
func preparedPolicy(t *testing.T, root, cache, id string) (*workspace.Prepared, string, suitecore.Agent) {
	t.Helper()
	p, err := workspace.Prepare(t.Context(), root, cache)
	if err != nil {
		t.Fatal(err)
	}
	out := extract(t, p)
	var a suitecore.Agent
	if err := suitecore.Unmarshal(readFile(t, filepath.Join(out, "agents", id+".json")), &a); err != nil {
		t.Fatal(err)
	}
	return p, out, a
}

// Dropping the binding, using an effective digest, or normalizing authored arrays breaks this round-trip.
func TestWorkspacePolicyBindingPreservesAuthoredArrays(t *testing.T) {
	root, cache := createWorkspace(t)
	original := networkPolicy(policyEndpointB + "," + policyEndpointA + "," + policyEndpointB)
	declarePolicy(t, root, "greeter", original)
	p, out, agent := preparedPolicy(t, root, cache, "greeter")
	if agent.Policy == nil {
		t.Fatal("policy binding missing")
	}
	ref := *agent.Policy
	if ref.Path != "policies/greeter.json" || ref.Descriptor.MediaType != suitecore.MediaTypePolicyV2 || ref.Descriptor.Digest != digest.FromString(original).String() || ref.Descriptor.Size != int64(len(original)) {
		t.Fatalf("wrong authored descriptor: %+v", ref)
	}
	if got := readFile(t, filepath.Join(out, ref.Path)); !bytes.Equal(got, []byte(original)) {
		t.Fatalf("authored arrays changed: %s", got)
	}
	inputs, err := workspace.SourceFiles(t.Context(), root, p)
	if err != nil || string(inputs["agents/greeter/policy.json"].Data) != original {
		t.Fatalf("policy source not fingerprint-bound: %v", err)
	}
	loaded, err := workspace.Load(t.Context(), p)
	if err != nil {
		t.Fatal("offline load refused policy:", err)
	}
	if loaded.Policy == nil || string(loaded.Policy.Bytes) != original || !reflect.DeepEqual(loaded.Policy.Descriptor, ref.Descriptor) || loaded.Policy.Path != ref.Path {
		t.Fatal("offline loader dropped authored policy")
	}
	if loaded.RequirePolicyFree() != workspace.ErrPolicyExecutionUnsupported {
		t.Fatal("execution gate accepted declared policy")
	}
	loaded.Policy.Bytes[0] = 'x'
	again, err := workspace.Load(t.Context(), p)
	if err != nil || string(again.Policy.Bytes) != original {
		t.Fatal("loaded policy bytes alias retained input")
	}
	if _, err := workspace.LoadInventory(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	artifact, err := suiteoras.ReadArtifactLayout(t.Context(), p.Path, p.Descriptor.Digest.String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.LoadArtifactInventory(t.Context(), artifact); err != nil {
		t.Fatal(err)
	}
	decoded, err := policy.DecodeSuitePolicy([]byte(original))
	if err != nil {
		t.Fatal(err)
	}
	effective, err := policy.Resolve(decoded, policy.AdministrativePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := policy.Payload(effective)
	if err != nil {
		t.Fatal(err)
	}
	if ref.Descriptor.Digest == payload.PolicyDigest || bytes.Equal(payload.PolicyBytes, []byte(original)) {
		t.Fatal("authored and effective identities conflated")
	}
	// JSON whitespace changes selected source identity, not the public graph/OCI.
	writeFile(t, filepath.Join(root, "agents/greeter/policy.json"), "\n "+original+"\n")
	formatted, _, _ := preparedPolicy(t, root, cache, "greeter")
	if p.Fingerprint == formatted.Fingerprint || p.Descriptor.Digest != formatted.Descriptor.Digest {
		t.Fatal("format-only policy edit changed package or failed to change fingerprint")
	}
	// Sorting/deduplicating authored arrays is NOT a formatting-only edit.
	writeFile(t, filepath.Join(root, "agents/greeter/policy.json"), networkPolicy(policyEndpointA+","+policyEndpointB))
	reordered, _, changedAgent := preparedPolicy(t, root, cache, "greeter")
	if reordered.Descriptor.Digest == p.Descriptor.Digest || changedAgent.Policy.Descriptor.Digest == ref.Descriptor.Digest {
		t.Fatal("authored order/duplicates were normalized away")
	}
	if err := workspace.CheckCurrent(t.Context(), root, p); err == nil {
		t.Fatal("stale prepared policy accepted")
	}
}
func TestWorkspacePolicyDenyAllAndMultiInventory(t *testing.T) {
	for _, count := range []int{1, 2, 16} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			root, cache := createWorkspace(t)
			ids := []string{"greeter"}
			if count > 1 {
				cases := 1
				if count == 16 {
					cases = 8
				}
				root, cache = multiWorkspace(t, count, cases)
				ids = nil
				for i := range count {
					ids = append(ids, fmt.Sprintf("member-%02d", i))
				}
			}
			for _, id := range ids {
				declarePolicy(t, root, id, networkPolicy(""))
			}
			p, out, _ := preparedPolicy(t, root, cache, ids[0])
			inv, err := workspace.LoadInventory(t.Context(), p)
			if err != nil || len(inv.Members) != count {
				t.Fatalf("inventory: %v", err)
			}
			for _, id := range ids {
				if got := string(readFile(t, filepath.Join(out, "policies", id+".json"))); got != networkPolicy("") {
					t.Fatal("deny-all lost:", got)
				}
			}
			artifact, err := suiteoras.ReadArtifactLayout(t.Context(), p.Path, p.Descriptor.Digest.String())
			if err != nil {
				t.Fatal(err)
			}
			fromArtifact, err := workspace.LoadArtifactInventory(t.Context(), artifact)
			if err != nil || len(fromArtifact.Members) != count {
				t.Fatalf("source-free policy inventory: %v", err)
			}
			for _, member := range fromArtifact.Members {
				if member.Policy == nil || string(member.Policy.Bytes) != networkPolicy("") {
					t.Fatal("source-free inventory dropped deny-all")
				}
			}
			if count > 1 {
				if _, err := workspace.Load(t.Context(), p); err != workspace.ErrMultiMemberUnsupported {
					t.Fatalf("multi execution gate lost: %v", err)
				}
			}
		})
	}
}
func TestWorkspacePolicyRefusesInvalidSource(t *testing.T) {
	for name, document := range map[string]string{
		"malformed": "{", "v1": `{"version":"agentsuite.policy/v1","denyWritePaths":[],"requiredCapabilities":["filesystem/deny-write/v1"]}`,
		"hostname":           networkPolicy(strings.ReplaceAll(policyEndpointA, "10.20.30.40", "example.com")),
		"UDP":                networkPolicy(strings.ReplaceAll(policyEndpointA, "TCP", "UDP")),
		"CIDR":               networkPolicy(strings.ReplaceAll(policyEndpointA, "10.20.30.40", "10.20.30.40/32")),
		"unknown capability": strings.ReplaceAll(networkPolicy(""), "network/egress-allow/v1", "tools/allow/v1"),
		"oversized":          strings.Repeat(" ", (1<<20)+1),
	} {
		t.Run(name, func(t *testing.T) {
			root, cache := createWorkspace(t)
			declarePolicy(t, root, "greeter", document)
			if _, err := workspace.Prepare(t.Context(), root, cache); err == nil {
				t.Fatal("invalid policy accepted")
			}
			if _, err := os.Stat(cache); !os.IsNotExist(err) {
				t.Fatal("invalid policy wrote cache")
			}
		})
	}
	for _, declaration := range []string{"policy: ''\n", "policy: null\n", "policy: ../policy.json\n", "policy: /tmp/policy.json\n", "policy: 'nested\\policy.json'\n", "policy: missing.json\n"} {
		t.Run(declaration, func(t *testing.T) {
			root, cache := createWorkspace(t)
			member := filepath.Join(root, "agents/greeter/member.yaml")
			writeFile(t, member, string(readFile(t, member))+declaration)
			if _, err := workspace.Prepare(t.Context(), root, cache); err == nil {
				t.Fatal("invalid path accepted")
			}
		})
	}
	for _, runtime := range []string{"runtime:\n  id: kagent\n  version: v0.10.3\n  engine: go\n", "runtime:\n  id: agentsessions\n  version: " + workspace.AgentSessionsVersion + "\n  engine: chat\n"} {
		root, cache := createWorkspace(t)
		setMemberRuntime(t, root, runtime)
		declarePolicy(t, root, "greeter", networkPolicy(""))
		if _, err := workspace.Prepare(t.Context(), root, cache); err == nil {
			t.Fatal("non-Orka declaration accepted")
		}
	}
}
func TestWorkspacePolicyRefusesStaticSymlinkAndFIFO(t *testing.T) {
	for _, kind := range []string{"symlink", "FIFO"} {
		t.Run(kind, func(t *testing.T) {
			root, cache := createWorkspace(t)
			declarePolicy(t, root, "greeter", networkPolicy(""))
			name := filepath.Join(root, "agents/greeter/policy.json")
			if err := os.Remove(name); err != nil {
				t.Fatal(err)
			}
			if kind == "symlink" {
				target := filepath.Join(root, "other.json")
				writeFile(t, target, networkPolicy(""))
				if err := os.Symlink(target, name); err != nil {
					t.Fatal(err)
				}
			} else {
				tool, err := exec.LookPath("mkfifo")
				if err != nil {
					t.Skip("mkfifo unavailable")
				}
				if b, err := exec.Command(tool, name).CombinedOutput(); err != nil {
					t.Fatal(err, string(b))
				}
			}
			if _, err := workspace.Prepare(t.Context(), root, cache); err == nil {
				t.Fatal("nonregular policy accepted")
			}
		})
	}
}
func TestWorkspacePolicyRejectsGraphDrift(t *testing.T) {
	for _, change := range []string{"stale bytes", "wrong path", "wrong media", "orphan", "foreign member"} {
		t.Run(change, func(t *testing.T) {
			root, cache := createWorkspace(t)
			declarePolicy(t, root, "greeter", networkPolicy(""))
			_, out, _ := preparedPolicy(t, root, cache, "greeter")
			switch change {
			case "stale bytes":
				writeFile(t, filepath.Join(out, "policies/greeter.json"), networkPolicy(policyEndpointA))
			case "orphan":
				writeFile(t, filepath.Join(out, "policies/orphan.json"), networkPolicy(""))
			default:
				rebindAgent(t, out, func(a *suitecore.Agent) {
					switch change {
					case "wrong path":
						a.Policy.Path = "instructions/greeter.md"
					case "wrong media":
						a.Policy.Descriptor.MediaType = suitecore.MediaTypePolicyV1
					case "foreign member":
						a.Policy.Path = "policies/other.json"
					}
				})
			}
			if _, err := suiteoras.Push(t.Context(), out, filepath.Join(t.TempDir(), "layout"), "invalid"); err == nil {
				t.Fatal("policy graph drift accepted")
			}
		})
	}
}
