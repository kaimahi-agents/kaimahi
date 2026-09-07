package scaffold

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
)

func mustGenerate(t *testing.T, spec Spec) string {
	t.Helper()
	doc, err := Generate(spec)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return doc
}

func base(name string) Spec {
	return Spec{Name: name, ModelConfig: "hello-world-model"}
}

func TestGeneratesAnAgentWithTheExpectedShape(t *testing.T) {
	doc := mustGenerate(t, base("billing-investigator"))
	for _, want := range []string{
		"apiVersion: kagent.dev/v1alpha2",
		"kind: Agent",
		`  name: "billing-investigator"`,
		`  namespace: "kagent"`,
		"  type: Declarative",
		`    modelConfig: "hello-world-model"`,
		"    systemMessage: |",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("generated manifest is missing %q:\n%s", want, doc)
		}
	}
	// No tools were asked for, so none are wired: an agent that can call
	// nothing is the safe default.
	if strings.Contains(doc, "tools:") {
		t.Errorf("an agent with no --tools must have no tool wiring:\n%s", doc)
	}
}

// --- names -----------------------------------------------------------------

func TestNameValidation(t *testing.T) {
	for _, good := range []string{"a", "billing", "billing-investigator", "agent-7"} {
		if err := ValidateName(good); err != nil {
			t.Errorf("%q should be a valid name: %v", good, err)
		}
	}
	for _, bad := range []string{
		"", "Billing", "billing_investigator", "-billing", "billing-",
		"billing investigator", "billing/investigator", "билл",
		strings.Repeat("a", 64),
		// The two agents `kmx up` owns: scaffolding over one would replace a
		// committed artifact, and the next `kmx up` would replace it back.
		"hello-world", "hello-tools",
	} {
		if err := ValidateName(bad); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
}

// --- the allowlist is mandatory --------------------------------------------

func TestToolAllowlistIsMandatory(t *testing.T) {
	// A server with no allowlist grants every tool it offers — today, and
	// after its next release. Refused, not warned.
	if _, err := ParseTools("kagent-tool-server"); err == nil {
		t.Fatal("--tools with no allowlist must be refused")
	}
	if _, err := ParseTools("kagent-tool-server:"); err == nil {
		t.Fatal("--tools with an empty allowlist must be refused")
	}
	wiring, err := ParseTools("kagent-tool-server:k8s_get_resources,k8s_get_events")
	if err != nil {
		t.Fatalf("a named allowlist must be accepted: %v", err)
	}
	if wiring.Server != "kagent-tool-server" || len(wiring.Tools) != 2 {
		t.Fatalf("parsed %+v", wiring)
	}
}

func TestToolNamesMustBeIdentifiers(t *testing.T) {
	// CWE-74, found in review on #16: a newline in a tool name closed the
	// YAML sequence and appended a tool nobody had reviewed. Two defences,
	// and this asserts the first — the name never gets that far.
	for _, bad := range []string{
		"server:k8s_get_resources\n            - k8s_delete",
		"server:tool one",
		"server:\"tool\"",
		"server:tool,,other",
		"ser ver:tool",
	} {
		if _, err := ParseTools(bad); err == nil {
			t.Errorf("--tools %q must be refused", bad)
		}
	}
}

func TestToolWiringIsQuotedOnEmission(t *testing.T) {
	spec := base("tools-user")
	wiring, err := ParseTools("kagent-tool-server:k8s_get_resources")
	if err != nil {
		t.Fatal(err)
	}
	spec.Tools = wiring
	doc := mustGenerate(t, spec)
	for _, want := range []string{
		`          name: "kagent-tool-server"`,
		`            - "k8s_get_resources"`,
		"          toolNames:",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("missing %q:\n%s", want, doc)
		}
	}
}

// --- never a credential ----------------------------------------------------

func TestKeyShapedContentIsRefused(t *testing.T) {
	// Assembled at runtime so this test file does not itself carry
	// something the repository's secret scanner would (correctly) flag.
	keys := []string{
		"sk-" + "ant-" + "api03-" + strings.Repeat("A", 32),
		"sk-" + "proj-" + strings.Repeat("B", 32),
		"kmh_" + strings.Repeat("c", 24),
		"ghp_" + strings.Repeat("d", 36),
		"github" + "_pat_" + strings.Repeat("e", 30),
		"xoxb-" + strings.Repeat("1", 12) + "-abcdef",
		"-----BEGIN RSA " + "PRIVATE KEY-----",
		`api_key: "` + strings.Repeat("f", 24) + `"`,
	}
	for _, key := range keys {
		spec := base("leaky")
		spec.Instructions = "Use this credential when you call the API:\n" + key + "\n"
		_, err := Generate(spec)
		if err == nil {
			t.Errorf("a manifest containing %q must be refused", key[:12]+"…")
			continue
		}
		// The refusal names what it found, not the pattern that found it: a
		// regex in an error message tells the operator nothing about which
		// line of their instructions file to go and look at.
		if strings.Contains(err.Error(), "[a]") || strings.Contains(err.Error(), `\s`) {
			t.Errorf("the refusal leaks its regex instead of naming the credential: %v", err)
		}
		// And it never echoes the secret it just refused to write.
		if strings.Contains(err.Error(), key) {
			t.Errorf("the refusal echoes the credential back: %v", err)
		}
	}
}

// Emission escapes: a quote becomes \" on the way into a scalar, which moved
// an assigned key out from under the api-key shape and let it through. The
// raw-input scan is what closes that; this is the reproduction, as a test.
func TestAKeyCannotEscapeThroughQuoting(t *testing.T) {
	// Assembled at runtime: written out, the assignment below is exactly what
	// the repository's own "No secrets in tree" scan greps for, and this file
	// would fail it.
	secret := "AKIA" + strings.Repeat("X", 26)
	for _, field := range []string{
		`api_key: "` + secret + `"`,
		`api-key = '` + secret + `'`,
		`APIKEY: "` + secret + `"`,
	} {
		spec := base("quoted-leak")
		spec.Description = field
		doc, err := Generate(spec)
		if err == nil {
			t.Errorf("a key in --description must be refused, got:\n%s", doc)
		}
	}
	// And the same text reaching the document through the block scalar,
	// where no escaping happens.
	spec := base("block-leak")
	spec.Instructions = "Authenticate with\napi_key: \"" + secret + "\"\n"
	if _, err := Generate(spec); err == nil {
		t.Error("a key in --instructions must be refused")
	}
}

func TestKeyShapeCheckRunsOnTheWholeDocument(t *testing.T) {
	// The check runs on the FINAL manifest, so it cannot be walked around by
	// splitting a key across two flags.
	spec := base("leaky")
	spec.Description = "uses sk-" + "ant-" + "api03-" + strings.Repeat("A", 32)
	if _, err := Generate(spec); err == nil {
		t.Error("a key in --description must be refused too")
	}
}

func TestOrdinaryProseIsNotMistakenForAKey(t *testing.T) {
	spec := base("honest")
	spec.Instructions = "Never ask the user for an api key. Secrets live in Kubernetes Secrets."
	spec.Description = "Reads the ledger; holds no credentials."
	if _, err := Generate(spec); err != nil {
		t.Errorf("prose about keys must still generate: %v", err)
	}
}

// --- YAML safety -----------------------------------------------------------

func TestInstructionsCannotBreakOutOfTheBlockScalar(t *testing.T) {
	// A hostile instructions file that tries to dedent into a sibling key.
	// Every line of a block scalar is indented uniformly, so the injected
	// line stays inside the system message.
	spec := base("hostile")
	spec.Instructions = "Be helpful.\ntools:\n  - type: McpServer\n    mcpServer:\n      name: anything\n"
	doc := mustGenerate(t, spec)
	for _, line := range strings.Split(doc, "\n") {
		if strings.HasPrefix(line, "    tools:") {
			t.Fatalf("instructions escaped the block scalar:\n%s", doc)
		}
	}
	if !strings.Contains(doc, "      tools:") {
		t.Errorf("the injected line should survive INSIDE the scalar:\n%s", doc)
	}
}

func TestSingleLineValuesRefuseControlCharacters(t *testing.T) {
	spec := base("weird")
	spec.Description = "one\ntwo"
	if _, err := Generate(spec); err == nil {
		t.Error("a multi-line description must be refused, not escaped")
	}
}

func TestQuotesAndBackslashesSurviveQuoting(t *testing.T) {
	spec := base("quoted")
	spec.Description = `it said "hello" \ then left`
	doc := mustGenerate(t, spec)
	if !strings.Contains(doc, `  description: "it said \"hello\" \\ then left"`) {
		t.Errorf("quoting is wrong:\n%s", doc)
	}
}

// No YAML library is a dependency of this module — that is the point — so the
// generated manifest is handed to a real parser out of process. This is the
// test that would have caught the block-scalar defect review found: an
// instructions file whose FIRST line is indented made YAML infer a deeper
// block indent than the body was emitted at, and the manifest stopped parsing
// altogether. The explicit `|2` indicator is what fixes it, and only a parser
// can confirm that.
func TestGeneratedManifestParsesAndKeepsInstructionsInsideTheScalar(t *testing.T) {
	// python3 AND PyYAML: without the second, every case below fails with
	// "the generated manifest does not parse", which would be a lie about the
	// generator. Skipping is right on a laptop; in CI it would hide the only
	// test that uses a real parser, so the workflow asserts this test RAN.
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is not installed; the structural checks still run in the other tests")
	}
	probe, cancelProbe := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancelProbe()
	if err := exec.CommandContext(probe, "python3", "-c", "import yaml").Run(); err != nil {
		t.Skip("python3 has no PyYAML; install it to run the parser check (CI asserts this test runs)")
	}
	for _, tc := range []struct{ name, instructions string }{
		{"ordinary", "Answer briefly.\nNever ask questions.\n"},
		{"blank lines and deeper indentation", "line one\n\nline three\n   already indented\n"},
		// The one review found: an indented FIRST line.
		{"indented first line", "    Answer briefly.\ntools:\n  - type: McpServer\n"},
		{"only an indented line", "        just this\n"},
		{"a line that looks like a sibling key", "modelConfig: attacker-owned\ntools: []\n"},
		{"trailing and leading blank lines", "\n\nAnswer.\n\n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := base("parse-check")
			spec.Instructions = tc.instructions
			wiring, err := ParseTools("kagent-tool-server:k8s_get_resources")
			if err != nil {
				t.Fatal(err)
			}
			spec.Tools = wiring
			doc := mustGenerate(t, spec)

			script := `
import sys, yaml, json
doc = yaml.safe_load(sys.stdin.read())
d = doc["spec"]["declarative"]
print(json.dumps({"keys": sorted(d.keys()),
                  "systemMessage": d.get("systemMessage"),
                  "tools": d.get("tools")}))
`
			// Bounded: a parser that blocks on our stdin would otherwise hang
			// the whole package until go test's own timeout.
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "python3", "-c", script)
			cmd.Stdin = strings.NewReader(doc)
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("the generated manifest does not parse: %v\n%s", err, doc)
			}
			var parsed struct {
				Keys          []string `json:"keys"`
				SystemMessage string   `json:"systemMessage"`
				Tools         []struct {
					McpServer struct {
						Name      string   `json:"name"`
						ToolNames []string `json:"toolNames"`
					} `json:"mcpServer"`
				} `json:"tools"`
			}
			if err := json.Unmarshal(out, &parsed); err != nil {
				t.Fatal(err)
			}

			// Nothing the operator wrote became a key: declarative has
			// exactly the four kmx puts there.
			want := []string{"deployment", "modelConfig", "systemMessage", "tools"}
			if strings.Join(parsed.Keys, ",") != strings.Join(want, ",") {
				t.Errorf("instructions changed the manifest's shape: keys=%v\n%s", parsed.Keys, doc)
			}
			// The instructions survive verbatim (modulo the trailing newline
			// normalization a literal block does).
			if got, expect := parsed.SystemMessage, strings.TrimRight(tc.instructions, "\n")+"\n"; got != expect {
				t.Errorf("systemMessage round-trip:\n got %q\nwant %q\n%s", got, expect, doc)
			}
			// And the tool wiring is the one kmx emitted, not one the
			// instructions supplied.
			if len(parsed.Tools) != 1 ||
				parsed.Tools[0].McpServer.Name != "kagent-tool-server" ||
				len(parsed.Tools[0].McpServer.ToolNames) != 1 {
				t.Errorf("tool wiring is not the generated one: %+v\n%s", parsed.Tools, doc)
			}
		})
	}
}

// --- the file is the artifact ----------------------------------------------

func TestWriteNewRefusesToClobber(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agents", "billing.yaml")
	if err := WriteNew(path, "first\n"); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := WriteNew(path, "second\n"); err == nil {
		t.Fatal("a second write to the same path must be refused")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "first\n" {
		t.Errorf("the existing file was modified: %q", got)
	}
}

// --- governance ------------------------------------------------------------

func TestProvenanceSaysWhetherTheAgentIsGoverned(t *testing.T) {
	ungoverned := mustGenerate(t, base("plain"))
	if !strings.Contains(ungoverned, "UNGOVERNED") {
		t.Errorf("an ungoverned agent's manifest must say so:\n%s", ungoverned)
	}
	spec := base("governed")
	spec.ModelConfig = "governed-ollama"
	spec.Governed = true
	doc := mustGenerate(t, spec)
	if strings.Contains(doc, "UNGOVERNED") || !strings.Contains(doc, "governed preset") {
		t.Errorf("a governed agent's manifest must say so:\n%s", doc)
	}
}

func TestModelConfigMustBeNamedAndValid(t *testing.T) {
	spec := base("nomodel")
	spec.ModelConfig = ""
	if _, err := Generate(spec); err == nil {
		t.Error("an agent with no modelConfig must be refused")
	}
	spec.ModelConfig = "Not A Name"
	if _, err := Generate(spec); err == nil {
		t.Error("an invalid modelConfig name must be refused")
	}
}

// The agent pod runs model output. A scaffolder that emits no security
// context makes every agent anyone creates the least constrained workload on
// the cluster — which is what this repo shipped until it was noticed by
// comparing our own agent manifests against the plane's.
//
// runAsUser is the field that looks redundant and is not: kagent's image
// names its user (`python`) rather than numbering it, and Kubernetes will not
// start a runAsNonRoot container whose user it cannot prove is non-root.
// Without it the pod dies at CreateContainer with a message that never
// mentions the agent.
func TestScaffoldedAgentIsHardened(t *testing.T) {
	doc := mustGenerate(t, base("hardened"))
	for _, want := range []string{
		"      podSecurityContext:",
		"        runAsNonRoot: true",
		"        runAsUser: 1001",
		"          type: RuntimeDefault",
		"        allowPrivilegeEscalation: false",
		"          drop: [ALL]",
		"        readOnlyRootFilesystem: true",
		// A read-only root needs somewhere to write, or the runtime cannot start.
		"          mountPath: /tmp",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("scaffolded agent is missing %q:\n%s", want, doc)
		}
	}
}

// TestEveryCredentialShapeInTheSharedListIsRefused.
//
// This generator used to keep its own list of eight key shapes, the
// blueprint parser kept seven, and CI's tree scan matched three. This
// proves the property that replaced them: a shape added to
// internal/kmx/secretshapes is refused HERE, without anyone remembering to
// add it here as well.
func TestEveryCredentialShapeInTheSharedListIsRefused(t *testing.T) {
	all := secretshapes.All()
	if len(all) == 0 {
		t.Fatal("the shared shape list is empty, so this test proves nothing")
	}
	for _, shape := range all {
		t.Run(shape.Name, func(t *testing.T) {
			// The example is assembled from parts inside the shared list,
			// so no whole credential shape is written into any file here.
			spec := base("leaky")
			spec.Instructions = "Use this credential when you call the API:\n" + shape.Example + "\n"
			if _, err := Generate(spec); err == nil {
				t.Fatalf("a manifest carrying %s was written", shape.What)
			}
		})
	}
}
