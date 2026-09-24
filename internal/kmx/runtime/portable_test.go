package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"testing"
)

func validPortableYAML(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("testdata/portable-agent.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// minimalPortableYAML has no extensions: it exercises the top-level
// requirements in isolation from Orka/kagent-specific validation.
func minimalPortableYAML() string {
	return `apiVersion: kmx.kaimahi.dev/v1alpha1
kind: PortableAgent
metadata:
  name: hello
spec:
  instructions: Do the thing.
  model:
    name: gpt-4o-mini
`
}

func mustReplace(t *testing.T, doc, old, new string) string {
	t.Helper()
	if !strings.Contains(doc, old) {
		t.Fatalf("fixture does not contain %q", old)
	}
	return strings.Replace(doc, old, new, 1)
}

func TestPortableValidDocumentParses(t *testing.T) {
	src := validPortableYAML(t)
	agent, err := ParsePortableAgent([]byte(src))
	if err != nil {
		t.Fatalf("a TARGETS.md §7-conforming document must parse: %v", err)
	}
	if agent.Metadata.Name != "hello" {
		t.Errorf("metadata.name = %q", agent.Metadata.Name)
	}
	if agent.Spec.Model.Name != "gpt-4o-mini" {
		t.Errorf("spec.model.name = %q", agent.Spec.Model.Name)
	}

	orka := agent.Extensions.Orka
	if orka == nil || orka.Namespace != "orka-system" {
		t.Fatalf("extensions.orka: %#v", orka)
	}
	if orka.Provider.DefaultModel != "gpt-4o-mini" {
		t.Errorf("extensions.orka.provider.defaultModel = %q", orka.Provider.DefaultModel)
	}
	if orka.Provider.SecretRef.Name != "hello-key" {
		t.Errorf("extensions.orka.provider.secretRef.name = %q", orka.Provider.SecretRef.Name)
	}
	if orka.Agent == nil || len(orka.Agent.Tools) != 1 || orka.Agent.Tools[0].Name != "web-search" {
		t.Errorf("extensions.orka.agent.tools = %#v", orka.Agent)
	}
	if orka.Agent == nil || len(orka.Agent.Skills) != 1 || orka.Agent.Skills[0].Name != "triage" {
		t.Errorf("extensions.orka.agent.skills = %#v", orka.Agent)
	}

	kagent := agent.Extensions.Kagent
	if kagent == nil || kagent.HarnessRef.Name != "kagent" {
		t.Fatalf("extensions.kagent.harnessRef: %#v", kagent)
	}
	if kagent.ModelConfigRef.Name != "local-ollama" {
		t.Errorf("extensions.kagent.modelConfigRef.name = %q", kagent.ModelConfigRef.Name)
	}
	if kagent.Tools == nil || len(kagent.Tools.MCP) != 1 ||
		kagent.Tools.MCP[0].Server.Kind != "RemoteMCPServer" || kagent.Tools.MCP[0].Server.Name != "filesystem" {
		t.Errorf("extensions.kagent.tools.mcp = %#v", kagent.Tools)
	}
	if kagent.Tools == nil || len(kagent.Tools.Agents) != 1 {
		t.Fatalf("extensions.kagent.tools.agents = %#v", kagent.Tools)
	}
	agentBinding := kagent.Tools.Agents[0]
	if agentBinding.Name != "researcher" || agentBinding.Description == "" ||
		agentBinding.TemplateRef.Name != "research-template" || agentBinding.Isolation != "Shared" {
		t.Errorf("extensions.kagent.tools.agents[0] = %#v", agentBinding)
	}
	const ociDigest = "ghcr.io/kaimahi/skills/triage@sha256:a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1"
	if len(kagent.Skills) != 1 || kagent.Skills[0].Name != "triage" || kagent.Skills[0].Source.OCI != ociDigest {
		t.Errorf("extensions.kagent.skills = %#v", kagent.Skills)
	}
	if len(kagent.Plugins) != 1 || kagent.Plugins[0].Source.Git == nil ||
		kagent.Plugins[0].Source.Git.Commit != "b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2" ||
		len(kagent.Plugins[0].Skills) != 1 || kagent.Plugins[0].Skills[0] != "audit-log" {
		t.Errorf("extensions.kagent.plugins = %#v", kagent.Plugins)
	}

	if string(agent.Source()) != src {
		t.Errorf("Source() does not return the exact input bytes")
	}
}

func TestPortableRequiresExactAPIVersionAndKind(t *testing.T) {
	cases := []struct {
		name string
		old  string
		new  string
	}{
		{"wrong apiVersion", "apiVersion: kmx.kaimahi.dev/v1alpha1\n", "apiVersion: kmx.kaimahi.dev/v2\n"},
		{"wrong kind", "kind: PortableAgent\n", "kind: Agent\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := mustReplace(t, minimalPortableYAML(), tc.old, tc.new)
			if _, err := ParsePortableAgent([]byte(doc)); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestPortableRejectsMultipleDocuments(t *testing.T) {
	t.Run("two documents", func(t *testing.T) {
		doc := minimalPortableYAML() + "---\n" + minimalPortableYAML()
		if _, err := ParsePortableAgent([]byte(doc)); err == nil {
			t.Fatal("expected an error for a second YAML document")
		}
	})
	t.Run("empty input", func(t *testing.T) {
		if _, err := ParsePortableAgent([]byte("")); err == nil {
			t.Fatal("expected an error for an empty document")
		}
	})
}

func TestPortableRejectsDuplicateKeysAtEveryLevel(t *testing.T) {
	cases := []struct {
		name string
		doc  string
	}{
		{
			"top level",
			"apiVersion: kmx.kaimahi.dev/v1alpha1\nkind: PortableAgent\nkind: PortableAgent\n",
		},
		{
			"metadata",
			"metadata:\n  name: hello\n  name: hello\n",
		},
		{
			"spec",
			"spec:\n  instructions: a\n  instructions: a\n",
		},
		{
			"spec.model",
			"spec:\n  model:\n    name: a\n    name: a\n",
		},
		{
			"extensions",
			"extensions:\n  orka:\n    namespace: a\n  orka:\n    namespace: b\n",
		},
		{
			"extensions.orka",
			"extensions:\n  orka:\n    namespace: a\n    namespace: b\n",
		},
		{
			"extensions.orka.provider",
			"extensions:\n  orka:\n    provider:\n      type: openai\n      type: openai\n",
		},
		{
			"extensions.orka.provider.secretRef",
			"extensions:\n  orka:\n    provider:\n      secretRef:\n        name: a\n        name: b\n",
		},
		{
			"extensions.orka.agent",
			"extensions:\n  orka:\n    agent:\n      rateLimit:\n        requestsPerMinute: 1\n      rateLimit:\n        requestsPerMinute: 2\n",
		},
		{
			"extensions.kagent",
			"extensions:\n  kagent:\n    namespace: a\n    namespace: b\n",
		},
		{
			"extensions.kagent.tools.mcp list entry",
			"extensions:\n  kagent:\n    tools:\n      mcp:\n        - server: {kind: RemoteMCPServer, name: a}\n          requireApproval: false\n          requireApproval: false\n",
		},
		{
			"extensions.kagent.tools.agents list entry",
			"extensions:\n  kagent:\n    tools:\n      agents:\n        - name: a\n          name: a\n",
		},
		{
			"extensions.kagent.skills.source",
			"extensions:\n  kagent:\n    skills:\n      - name: a\n        source:\n          oci: a@sha256:1111111111111111111111111111111111111111111111111111111111111111\n          oci: b@sha256:2222222222222222222222222222222222222222222222222222222222222222\n",
		},
		{
			"extensions.kagent.plugins entry",
			"extensions:\n  kagent:\n    plugins:\n      - source: {oci: a@sha256:1111111111111111111111111111111111111111111111111111111111111111}\n        skills: [a]\n        skills: [a]\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParsePortableAgent([]byte(tc.doc))
			if err == nil {
				t.Fatal("expected a duplicate-key error")
			}
			if !strings.Contains(err.Error(), "duplicate") {
				t.Errorf("error does not mention duplicate: %v", err)
			}
		})
	}
}

// A YAML merge key injects another mapping's keys into this one, which is
// exactly what strict decoding and duplicate-key detection exist to prevent:
// the merged keys are never written where they take effect, so a document can
// say two things about one field — or carry a field the schema does not model
// — without either gate seeing it. The merge is refused wherever it appears,
// naming the field path and line so the author can find it.
func TestPortableRejectsYAMLMergeKeysAnywhere(t *testing.T) {
	cases := []struct {
		name  string
		doc   string
		where string
	}{
		{
			"top level",
			"apiVersion: kmx.kaimahi.dev/v1alpha1\nkind: PortableAgent\nmetadata: &meta\n  name: hello\n<<: *meta\nspec:\n  instructions: Do the thing.\n  model:\n    name: gpt-4o-mini\n",
			"<<",
		},
		{
			"nested under spec, silently supplying a modeled field",
			"apiVersion: kmx.kaimahi.dev/v1alpha1\nkind: PortableAgent\nmetadata:\n  name: hello\nspec:\n  <<: &base\n    instructions: From the anchor.\n  instructions: Do the thing.\n  model:\n    name: gpt-4o-mini\n",
			"spec.<<",
		},
		{
			"nested under an extension",
			"apiVersion: kmx.kaimahi.dev/v1alpha1\nkind: PortableAgent\nmetadata:\n  name: hello\nspec:\n  instructions: Do the thing.\n  model:\n    name: gpt-4o-mini\nextensions:\n  orka:\n    apiVersion: core.orka.ai/v1alpha1\n    namespace: orka-system\n    provider: &provider\n      type: openai\n      defaultModel: gpt-4o-mini\n      secretRef:\n        name: hello-key\n    agent:\n      <<: *provider\n",
			"extensions.orka.agent.<<",
		},
		{
			"inside a sequence entry",
			"apiVersion: kmx.kaimahi.dev/v1alpha1\nkind: PortableAgent\nmetadata:\n  name: hello\nspec:\n  instructions: Do the thing.\n  model:\n    name: gpt-4o-mini\nextensions:\n  orka:\n    apiVersion: core.orka.ai/v1alpha1\n    namespace: orka-system\n    provider:\n      type: openai\n      defaultModel: gpt-4o-mini\n      secretRef: &ref\n        name: hello-key\n    agent:\n      tools:\n        - <<: *ref\n",
			"extensions.orka.agent.tools[0].<<",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParsePortableAgent([]byte(tc.doc))
			if err == nil {
				t.Fatal("a YAML merge key was accepted")
			}
			if !strings.Contains(err.Error(), "merge key") {
				t.Errorf("error does not name the merge key: %v", err)
			}
			if !strings.Contains(err.Error(), tc.where) {
				t.Errorf("error does not name the field path %q: %v", tc.where, err)
			}
		})
	}
}

func TestPortableRejectsUnknownFieldsAtEveryLevel(t *testing.T) {
	base := validPortableYAML(t)
	cases := []struct {
		name string
		old  string
		new  string
	}{
		{"top level", "kind: PortableAgent\n", "kind: PortableAgent\nbogus: true\n"},
		{"metadata", "metadata:\n  name: hello\n", "metadata:\n  name: hello\n  bogus: true\n"},
		{"spec", "spec:\n  instructions:", "spec:\n  bogus: true\n  instructions:"},
		{"spec.model", "model:\n    name: gpt-4o-mini\n", "model:\n    name: gpt-4o-mini\n    bogus: true\n"},
		{"extensions", "extensions:\n  orka:", "extensions:\n  bogus: true\n  orka:"},
		{"extensions.orka", "namespace: orka-system\n", "namespace: orka-system\n    bogus: true\n"},
		{"extensions.orka.provider", "type: openai\n", "type: openai\n      bogus: true\n"},
		{"extensions.orka.provider.secretRef", "name: hello-key\n", "name: hello-key\n        bogus: true\n"},
		{"extensions.orka.agent", "agent:\n      tools:", "agent:\n      bogus: true\n      tools:"},
		{"extensions.orka.agent.rateLimit", "rateLimit:\n        requestsPerMinute: 30\n", "rateLimit:\n        requestsPerMinute: 30\n        bogus: true\n"},
		{"extensions.kagent", "namespace: kagent-system\n", "namespace: kagent-system\n    bogus: true\n"},
		{"extensions.kagent.harnessRef", "harnessRef:\n      name: kagent\n", "harnessRef:\n      name: kagent\n      bogus: true\n"},
		{"extensions.kagent.tools", "tools:\n      mcp:", "tools:\n      bogus: true\n      mcp:"},
		{"extensions.kagent.tools.mcp entry", "requireApproval: false\n", "requireApproval: false\n          bogus: true\n"},
		{"extensions.kagent.tools.mcp.server", "kind: RemoteMCPServer\n            name: filesystem\n", "kind: RemoteMCPServer\n            name: filesystem\n            bogus: true\n"},
		{"extensions.kagent.tools.agents entry", "isolation: Shared\n", "isolation: Shared\n          bogus: true\n"},
		{"extensions.kagent.tools.agents.templateRef", "templateRef:\n            name: research-template\n", "templateRef:\n            name: research-template\n            bogus: true\n"},
		{"extensions.kagent.skills entry", "name: triage\n        source:\n", "name: triage\n        bogus: true\n        source:\n"},
		{"extensions.kagent.skills.source", "oci: ghcr.io/kaimahi/skills/triage@sha256:a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1\n", "oci: ghcr.io/kaimahi/skills/triage@sha256:a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1\n          bogus: true\n"},
		{"extensions.kagent.plugins entry", "plugins:\n      - source:\n", "plugins:\n      - bogus: true\n        source:\n"},
		{"extensions.kagent.plugins entry has no name field (old lossy shape)", "plugins:\n      - source:\n", "plugins:\n      - name: audit-log\n        source:\n"},
		{"extensions.kagent.plugins.source.git", "url: https://github.com/kaimahi-agents/plugins\n", "url: https://github.com/kaimahi-agents/plugins\n            bogus: true\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := mustReplace(t, base, tc.old, tc.new)
			_, err := ParsePortableAgent([]byte(doc))
			if err == nil {
				t.Fatal("expected an unknown-field error")
			}
			if !strings.Contains(err.Error(), "not found") {
				t.Errorf("error does not mention the unknown field: %v", err)
			}
		})
	}
}

func TestPortableRequiresNameInstructionsModel(t *testing.T) {
	cases := []struct {
		name string
		old  string
		new  string
	}{
		{"missing metadata.name", "  name: hello\n", "  name: \"\"\n"},
		{"missing spec.instructions", "instructions: Do the thing.\n", "instructions: \"\"\n"},
		{"missing spec.model.name", "    name: gpt-4o-mini\n", "    name: \"\"\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := mustReplace(t, minimalPortableYAML(), tc.old, tc.new)
			if _, err := ParsePortableAgent([]byte(doc)); err == nil {
				t.Fatal("expected a required-field error")
			}
		})
	}
}

func TestPortableExtensionsRequireAPIVersionAndNamespace(t *testing.T) {
	base := validPortableYAML(t)
	cases := []struct {
		name string
		old  string
		new  string
	}{
		{"orka missing apiVersion", "apiVersion: core.orka.ai/v1alpha1\n", "apiVersion: \"\"\n"},
		{"orka missing namespace", "namespace: orka-system\n", "namespace: \"\"\n"},
		{"kagent missing apiVersion", "apiVersion: kagent.dev/v1alpha3\n", "apiVersion: \"\"\n"},
		{"kagent missing namespace", "namespace: kagent-system\n", "namespace: \"\"\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := mustReplace(t, base, tc.old, tc.new)
			if _, err := ParsePortableAgent([]byte(doc)); err == nil {
				t.Fatal("expected an extension apiVersion/namespace error")
			}
		})
	}
}

func TestPortableRejectsInlineSecretValues(t *testing.T) {
	// Assembled at run time, never as a literal credential shape in the tree.
	secret := "sk-" + strings.Repeat("A", 24)

	t.Run("top-level instructions", func(t *testing.T) {
		doc := mustReplace(t, minimalPortableYAML(), "Do the thing.", "Do the thing. Token "+secret)
		_, err := ParsePortableAgent([]byte(doc))
		if err == nil {
			t.Fatal("expected a credential-shape error")
		}
		if !strings.Contains(err.Error(), "credential") {
			t.Errorf("error does not name the credential: %v", err)
		}
	})

	t.Run("nested extension field", func(t *testing.T) {
		doc := mustReplace(t, validPortableYAML(t), "Answer plainly and cite sources.", "Answer plainly. Token "+secret)
		if _, err := ParsePortableAgent([]byte(doc)); err == nil {
			t.Fatal("expected a credential-shape error from a nested extension field")
		}
	})
}

func TestPortableRejectsRuntimeExtensionMismatch(t *testing.T) {
	base := validPortableYAML(t)
	t.Run("orka apiVersion is kagent's", func(t *testing.T) {
		doc := mustReplace(t, base, "apiVersion: core.orka.ai/v1alpha1\n", "apiVersion: kagent.dev/v1alpha3\n")
		if _, err := ParsePortableAgent([]byte(doc)); err == nil {
			t.Fatal("expected a runtime/extension mismatch error")
		}
	})
	t.Run("kagent apiVersion is orka's", func(t *testing.T) {
		doc := mustReplace(t, base, "apiVersion: kagent.dev/v1alpha3\n", "apiVersion: core.orka.ai/v1alpha1\n")
		if _, err := ParsePortableAgent([]byte(doc)); err == nil {
			t.Fatal("expected a runtime/extension mismatch error")
		}
	})
}

func TestPortableKagentSkillAndPluginIdentitiesAreImmutable(t *testing.T) {
	base := validPortableYAML(t)
	cases := []struct {
		name string
		old  string
		new  string
	}{
		{
			"missing skill name",
			"skills:\n      - name: triage\n        source:",
			"skills:\n      - name: \"\"\n        source:",
		},
		{
			"duplicate skill name",
			"      - name: triage\n        source:\n          oci: ghcr.io/kaimahi/skills/triage@sha256:a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1\n",
			"      - name: triage\n        source:\n          oci: ghcr.io/kaimahi/skills/triage@sha256:a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1\n      - name: triage\n        source:\n          oci: ghcr.io/kaimahi/skills/triage@sha256:a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1\n",
		},
		{
			"skill with no source at all (bare name)",
			"      - name: triage\n        source:\n          oci: ghcr.io/kaimahi/skills/triage@sha256:a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1\n",
			"      - name: triage\n        source: {}\n",
		},
		{
			"plugin bundle selects a blank skill name",
			"skills:\n          - audit-log\n",
			"skills:\n          - \"\"\n",
		},
		{
			"plugin bundle duplicates a skill selection",
			"skills:\n          - audit-log\n",
			"skills:\n          - audit-log\n          - audit-log\n",
		},
		{
			"plugin git commit is a branch, not a full commit ID",
			"commit: b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2\n",
			"commit: main\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := mustReplace(t, base, tc.old, tc.new)
			if _, err := ParsePortableAgent([]byte(doc)); err == nil {
				t.Fatal("expected an immutable-identity error")
			}
		})
	}
}

func TestPortableKagentAgentToolBindingRequiresAllFields(t *testing.T) {
	base := validPortableYAML(t)
	cases := []struct {
		name string
		old  string
		new  string
	}{
		{
			"bare name-only binding (the old lossy shape) is rejected",
			"agents:\n        - name: researcher\n          description: Delegate deep research subtasks to a specialized agent.\n          templateRef:\n            name: research-template\n          isolation: Shared\n",
			"agents:\n        - name: researcher\n",
		},
		{
			"missing description",
			"description: Delegate deep research subtasks to a specialized agent.\n",
			"description: \"\"\n",
		},
		{
			"missing templateRef.name",
			"            name: research-template\n",
			"            name: \"\"\n",
		},
		{
			"invalid isolation value",
			"isolation: Shared\n",
			"isolation: Bogus\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := mustReplace(t, base, tc.old, tc.new)
			if _, err := ParsePortableAgent([]byte(doc)); err == nil {
				t.Fatal("expected an AgentToolBinding validation error")
			}
		})
	}
}

// TestPortableKagentArtifactSourceMirrorsPinnedForms proves the closed
// source union matches the pinned ArtifactSource exactly: oci is one
// "ref@sha256:<64hex>" string, git is {url, commit}, bucket is
// {s3: {endpoint, bucket, key, versionId, region?}}, and the shared `path`
// must be relative with no '..' segment — including the two valid commit
// ID lengths the pinned regex accepts — while the previously invented
// reference/digest, repository/version, bare s3, absolute-path and '..'
// forms fail.
func TestPortableKagentArtifactSourceMirrorsPinnedForms(t *testing.T) {
	header := `apiVersion: kmx.kaimahi.dev/v1alpha1
kind: PortableAgent
metadata:
  name: hello
spec:
  instructions: Do the thing.
  model:
    name: gpt-4o-mini
extensions:
  kagent:
    apiVersion: kagent.dev/v1alpha3
    namespace: kagent-system
    harnessRef:
      name: kagent
    modelConfigRef:
      name: local-ollama
    skills:
      - name: triage
        source:
`
	valid := []struct {
		name   string
		source string
	}{
		{
			"oci ref@sha256 string",
			"          oci: ghcr.io/kaimahi/skills/triage@sha256:a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1\n",
		},
		{
			"git with a 40-hex commit",
			"          git:\n            url: https://github.com/kaimahi-agents/plugins\n            commit: b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2\n",
		},
		{
			"git with a 64-hex commit",
			"          git:\n            url: https://github.com/kaimahi-agents/plugins\n            commit: c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3\n",
		},
		{
			"bucket.s3 with every required field",
			"          bucket:\n            s3:\n              endpoint: https://s3.example.com\n              bucket: kaimahi-skills\n              key: triage/skill.tar.gz\n              versionId: v1\n",
		},
		{
			"oci with a relative shared path",
			"          oci: ghcr.io/kaimahi/skills/triage@sha256:a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1\n          path: skills/triage\n",
		},
	}
	for _, tc := range valid {
		t.Run("valid: "+tc.name, func(t *testing.T) {
			if _, err := ParsePortableAgent([]byte(header + tc.source)); err != nil {
				t.Fatalf("a pinned-form source must decode: %v", err)
			}
		})
	}

	invalid := []struct {
		name   string
		source string
	}{
		{
			"oci as the old invented {reference, digest} object is lossy and must fail",
			"          oci:\n            reference: ghcr.io/kaimahi/skills/triage\n            digest: sha256:a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1\n",
		},
		{
			"oci without an @sha256 digest",
			"          oci: ghcr.io/kaimahi/skills/triage:latest\n",
		},
		{
			"git commit is a branch name",
			"          git:\n            url: https://github.com/kaimahi-agents/plugins\n            commit: main\n",
		},
		{
			"the old invented bare s3 object (repository/version, no endpoint or bucket wrapper) is lossy and must fail",
			"          s3:\n            bucket: kaimahi-skills\n            key: triage/skill.tar.gz\n            version: v1\n",
		},
		{
			"bucket.s3 missing versionId",
			"          bucket:\n            s3:\n              endpoint: https://s3.example.com\n              bucket: kaimahi-skills\n              key: triage/skill.tar.gz\n",
		},
		{
			"path is absolute, not relative",
			"          oci: ghcr.io/kaimahi/skills/triage@sha256:a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1\n          path: /skills/triage\n",
		},
		{
			"path contains a '..' segment",
			"          oci: ghcr.io/kaimahi/skills/triage@sha256:a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1\n          path: skills/../triage\n",
		},
	}
	for _, tc := range invalid {
		t.Run("invalid: "+tc.name, func(t *testing.T) {
			if _, err := ParsePortableAgent([]byte(header + tc.source)); err == nil {
				t.Fatal("expected a pinned-source-shape error")
			}
		})
	}
}

func TestPortableRejectsLossyBareForms(t *testing.T) {
	t.Run("bare-string orka agent tools/skills no longer decode", func(t *testing.T) {
		doc := mustReplace(t, validPortableYAML(t),
			"tools:\n        - name: web-search\n",
			"tools:\n        - web-search\n")
		if _, err := ParsePortableAgent([]byte(doc)); err == nil {
			t.Fatal("a bare tool name string must not decode into the required {name: ...} object")
		}
	})
	t.Run("bare-string kagent skill name no longer decodes", func(t *testing.T) {
		doc := mustReplace(t, validPortableYAML(t),
			"skills:\n      - name: triage\n        source:\n          oci: ghcr.io/kaimahi/skills/triage@sha256:a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1\n",
			"skills:\n      - triage\n")
		if _, err := ParsePortableAgent([]byte(doc)); err == nil {
			t.Fatal("a bare skill name string must not decode into the required immutable-identity object")
		}
	})
	t.Run("bare-name-only kagent agent tool binding no longer decodes", func(t *testing.T) {
		doc := mustReplace(t, validPortableYAML(t),
			"agents:\n        - name: researcher\n          description: Delegate deep research subtasks to a specialized agent.\n          templateRef:\n            name: research-template\n          isolation: Shared\n",
			"agents:\n        - name: researcher\n")
		if _, err := ParsePortableAgent([]byte(doc)); err == nil {
			t.Fatal("a bare agent-tool name must not satisfy the required AgentToolBinding fields")
		}
	})
}

func TestPortableSourceIsDefensivelyCopied(t *testing.T) {
	original := []byte(validPortableYAML(t))
	input := make([]byte, len(original))
	copy(input, original)

	agent, err := ParsePortableAgent(input)
	if err != nil {
		t.Fatalf("valid document must parse: %v", err)
	}

	// Mutating the caller's slice after Decode must not change the result.
	for i := range input {
		input[i] = '#'
	}
	if string(agent.Source()) != string(original) {
		t.Fatal("Source() reflects mutation of the caller's original slice")
	}

	// Mutating one returned copy must not change the next.
	first := agent.Source()
	for i := range first {
		first[i] = '#'
	}
	if string(agent.Source()) != string(original) {
		t.Fatal("Source() returned a shared, not a defensive, copy")
	}
}

func TestPortableOrkaShorthandRoundTrip(t *testing.T) {
	shorthand := OrkaShorthand{
		Name:         "hello",
		Namespace:    "orka-system",
		Instructions: "You are a helpful, careful agent.",
		ProviderType: "openai",
		Model:        "gpt-4o-mini",
		SecretName:   "hello-key",
		SecretKey:    "api-key",
		Tools:        []string{"web-search"},
		Skills:       []string{"triage"},
	}

	agent, err := EncodeOrkaShorthand(shorthand)
	if err != nil {
		t.Fatalf("EncodeOrkaShorthand: %v", err)
	}

	first, err := agent.YAML()
	if err != nil {
		t.Fatalf("YAML: %v", err)
	}
	second, err := agent.YAML()
	if err != nil {
		t.Fatalf("YAML (second call): %v", err)
	}
	if string(first) != string(second) {
		t.Fatal("Orka shorthand serialization is not deterministic")
	}

	parsed, err := ParsePortableAgent(first)
	if err != nil {
		t.Fatalf("round-trip parse: %v", err)
	}
	if parsed.Metadata.Name != shorthand.Name {
		t.Errorf("metadata.name = %q", parsed.Metadata.Name)
	}
	if parsed.Spec.Model.Name != shorthand.Model {
		t.Errorf("spec.model.name = %q", parsed.Spec.Model.Name)
	}
	if parsed.Extensions.Kagent != nil {
		t.Errorf("Orka shorthand must not synthesize a kagent extension")
	}
	if parsed.Extensions.Orka == nil {
		t.Fatal("Orka shorthand must produce an orka extension")
	}
	if parsed.Extensions.Orka.Provider.DefaultModel != shorthand.Model {
		t.Errorf("extensions.orka.provider.defaultModel = %q", parsed.Extensions.Orka.Provider.DefaultModel)
	}
	if parsed.Extensions.Orka.Provider.SecretRef.Name != shorthand.SecretName {
		t.Errorf("extensions.orka.provider.secretRef.name = %q", parsed.Extensions.Orka.Provider.SecretRef.Name)
	}
	if parsed.Extensions.Orka.Agent == nil || len(parsed.Extensions.Orka.Agent.Tools) != 1 ||
		parsed.Extensions.Orka.Agent.Tools[0].Name != "web-search" {
		t.Errorf("extensions.orka.agent.tools = %#v", parsed.Extensions.Orka.Agent)
	}
	if parsed.Extensions.Orka.Agent == nil || len(parsed.Extensions.Orka.Agent.Skills) != 1 ||
		parsed.Extensions.Orka.Agent.Skills[0].Name != "triage" {
		t.Errorf("extensions.orka.agent.skills = %#v", parsed.Extensions.Orka.Agent)
	}
}

// DESIGN.md §2 frames the portable bundle digest over "the exact validated
// portable source bytes", and says flag-based shorthand "is deterministically
// encoded first and framed under the same logical path". An encoded document
// with no source bytes would therefore have no identity to hash at all.
func TestPortableOrkaShorthandCarriesItsEncodedSource(t *testing.T) {
	agent, err := EncodeOrkaShorthand(OrkaShorthand{
		Name:         "hello",
		Namespace:    "orka-system",
		Instructions: "You are a helpful, careful agent.",
		ProviderType: "openai",
		Model:        "gpt-4o-mini",
		SecretName:   "hello-key",
	})
	if err != nil {
		t.Fatalf("EncodeOrkaShorthand: %v", err)
	}
	source := agent.Source()
	if len(source) == 0 {
		t.Fatal("shorthand encoding produced no source bytes")
	}
	rendered, err := agent.YAML()
	if err != nil {
		t.Fatalf("YAML: %v", err)
	}
	if string(source) != string(rendered) {
		t.Fatalf("Source() and YAML() disagree:\n--- source ---\n%s\n--- yaml ---\n%s", source, rendered)
	}
	// The source must be exactly what a strict parse accepts, so the same
	// document authored by hand digests identically.
	parsed, err := ParsePortableAgent(source)
	if err != nil {
		t.Fatalf("shorthand source does not parse strictly: %v", err)
	}
	if PortableBundleDigest(parsed.Source()) != PortableBundleDigest(source) {
		t.Fatal("re-parsing the shorthand source changed its portable digest")
	}
	// Mutating the returned copy must not change the document.
	source[0] = '#'
	if agent.Source()[0] == '#' {
		t.Fatal("Source() returned a shared, not a defensive, copy")
	}
}

// Independent expectation: the digest is the SHA-256 of DESIGN.md §2's exact
// framing over the encoded document, computed here without calling the
// framing helper under test.
func TestPortableOrkaShorthandDigestMatchesIndependentFraming(t *testing.T) {
	shorthand := OrkaShorthand{
		Name:         "hello",
		Namespace:    "orka-system",
		Instructions: "You are a helpful, careful agent.",
		ProviderType: "openai",
		Model:        "gpt-4o-mini",
		SecretName:   "hello-key",
	}
	agent, err := EncodeOrkaShorthand(shorthand)
	if err != nil {
		t.Fatalf("EncodeOrkaShorthand: %v", err)
	}
	source := agent.Source()
	sum := sha256.Sum256([]byte(fmt.Sprintf("portable-agent.yaml %d\n%s\n", len(source), source)))
	if want := hex.EncodeToString(sum[:]); PortableBundleDigest(source) != want {
		t.Fatalf("PortableBundleDigest(shorthand source) = %q, want %q", PortableBundleDigest(source), want)
	}
	// An empty source hashes a constant that identifies nothing; the encoded
	// document must never collide with it.
	if PortableBundleDigest(source) == PortableBundleDigest(nil) {
		t.Fatal("shorthand digest equals the digest of an absent source")
	}
}

// Two different shorthand inputs must never share a portable digest, so the
// digest actually identifies authored behavior rather than a constant.
func TestPortableOrkaShorthandDigestDiffersPerInput(t *testing.T) {
	base := OrkaShorthand{
		Name:         "hello",
		Namespace:    "orka-system",
		Instructions: "You are a helpful, careful agent.",
		ProviderType: "openai",
		Model:        "gpt-4o-mini",
		SecretName:   "hello-key",
	}
	for _, tc := range []struct {
		name  string
		apply func(*OrkaShorthand)
	}{
		{"model", func(s *OrkaShorthand) { s.Model = "gpt-4o" }},
		{"instructions", func(s *OrkaShorthand) { s.Instructions = "Answer in one sentence." }},
		{"namespace", func(s *OrkaShorthand) { s.Namespace = "agents" }},
		{"tools", func(s *OrkaShorthand) { s.Tools = []string{"web-search"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first, err := EncodeOrkaShorthand(base)
			if err != nil {
				t.Fatalf("EncodeOrkaShorthand: %v", err)
			}
			changed := base
			tc.apply(&changed)
			second, err := EncodeOrkaShorthand(changed)
			if err != nil {
				t.Fatalf("EncodeOrkaShorthand: %v", err)
			}
			if PortableBundleDigest(first.Source()) == PortableBundleDigest(second.Source()) {
				t.Fatalf("changing %s did not change the portable digest", tc.name)
			}
			// The same input twice must still be identical.
			repeat, err := EncodeOrkaShorthand(base)
			if err != nil {
				t.Fatalf("EncodeOrkaShorthand: %v", err)
			}
			if PortableBundleDigest(first.Source()) != PortableBundleDigest(repeat.Source()) {
				t.Fatal("identical shorthand inputs produced different portable digests")
			}
		})
	}
}
