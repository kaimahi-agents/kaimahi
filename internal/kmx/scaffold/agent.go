// Package scaffold generates the reviewable YAML that IS the agent.
//
// The artifact is the point: kagent's Agent CRD is the agent-as-code
// topology document this project has shipped from its first agent onward,
// so `kmx agent create` writes a file you own and could have written by
// hand, and only then applies it. Nothing here is a runtime.
//
// The safety properties are #16's, carried over in full:
//
//	Never accepts a credential      the generator emits Secret REFERENCES.
//	                                A scaffolder that can take a key is a
//	                                scaffolder that can leak one into a file
//	                                you are about to commit.
//	Refuses key-shaped output       fail closed: if anything matching a known
//	                                key shape reaches the manifest, writing
//	                                stops.
//	Allowlist mandatory             `--tools server` is refused; you name the
//	                                tools. Names are identifiers and are
//	                                quoted on emission (CWE-74).
//	Won't overwrite                 --out uses an exclusive create, so an
//	                                edited file is never clobbered.
//	Blast radius is the guard       applying goes through the same
//	                                context guard as every other mutation.
//	Preflight on ModelConfig        a missing ModelConfig is admitted by the
//	                                API server and then fails to reconcile in
//	                                silence.
package scaffold

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
)

// Spec is everything the generator needs. There is deliberately no field
// here that could hold a credential.
type Spec struct {
	Name        string
	Namespace   string
	Description string
	ModelConfig string
	// Instructions becomes spec.declarative.systemMessage.
	Instructions string
	// Tools is the parsed --tools value: a server and its mandatory
	// allowlist.
	Tools *ToolWiring
	// Governed records whether ModelConfig is one the plane fronts. It only
	// affects the provenance comment and the warnings the caller prints.
	Governed bool

	// Image, when set, makes this a BYO agent: kagent deploys the image and
	// expects it to serve A2A on :8080.
	//
	// BYO is a different artifact, not a variation on the declarative one.
	// spec.byo has exactly ONE property, deployment — no modelConfig, no
	// tools. So the governance that a declarative agent gets by REFERENCE
	// has to be carried across as environment, and it stops being visible
	// in the Agent YAML. That is the trade, and Governance below is what
	// makes it explicit rather than implied.
	Image string
	// Placement is the resolved --isolation profile, or nil.
	Placement *Placement
	// Governance is the seam configuration injected into a BYO image's env:
	// the proxy base URL, the gateway endpoint, and the credential Secret.
	// Empty for a declarative agent, which needs none of it.
	Governance []EnvVar
	// Identity is the image-dependent half of a BYO agent's security
	// context: the UID it runs as, when the operator stated one.
	Identity Identity
}

// EnvVar is a container environment variable, either literal or from a
// Secret key.
type EnvVar struct {
	Name      string
	Value     string
	SecretRef string // Secret name; when set, Value is the key
}

// ToolWiring is one MCP server and the tools the agent may call on it.
type ToolWiring struct {
	Server string
	Tools  []string
}

// RFC 1123 label: what the API server will accept for a resource name, and
// what kagent derives the Deployment and Service names from.
var nameRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// Tool and server names are identifiers. Anything else is either a typo or
// an attempt to smuggle YAML through a list item.
var identifierRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// reserved names: the two agents `kmx up` owns. Scaffolding over one of them
// would silently replace their committed manifests with a generated file
// — and then `kmx up` would apply the committed version back over it.
var reserved = map[string]string{
	"hello-world": "the agent `kmx up` creates from k8s/hello-world.yaml",
	"hello-tools": "the agent `kmx up` creates from k8s/tools-agent.yaml",
}

// ValidateName enforces the RFC 1123 label rules with messages that say what
// to do, not just what is wrong.
func ValidateName(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("an agent needs a name: kmx agent create <name>")
	case len(name) > 63:
		return fmt.Errorf("agent name %q is %d characters; Kubernetes allows 63", name, len(name))
	case !nameRE.MatchString(name):
		return fmt.Errorf("agent name %q is not a valid Kubernetes name — lowercase letters, digits and dashes, starting and ending with a letter or digit (e.g. billing-investigator)", name)
	}
	if why, taken := reserved[name]; taken {
		return fmt.Errorf("%q is %s — pick another name", name, why)
	}
	return nil
}

// ParseTools parses `--tools server:tool1,tool2`.
//
// The allowlist is mandatory. `--tools server` — every tool the server
// offers, whatever it offers tomorrow — is exactly the wiring this project
// exists to refuse, so it is a hard error rather than a warning.
func ParseTools(value string) (*ToolWiring, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	server, list, found := strings.Cut(value, ":")
	server = strings.TrimSpace(server)
	if !found || strings.TrimSpace(list) == "" {
		return nil, fmt.Errorf("--tools needs an allowlist: --tools %s:<tool>[,<tool>...]\n"+
			"  Naming a server alone would grant every tool it offers, today and after its next release.\n"+
			"  List the tools the agent may call: kubectl -n kagent get remotemcpserver %s -o yaml", server, server)
	}
	if !identifierRE.MatchString(server) {
		return nil, fmt.Errorf("MCP server name %q is not an identifier", server)
	}
	wiring := &ToolWiring{Server: server}
	seen := map[string]bool{}
	for _, tool := range strings.Split(list, ",") {
		tool = strings.TrimSpace(tool)
		if tool == "" {
			return nil, fmt.Errorf("--tools has an empty tool name in %q", value)
		}
		if !identifierRE.MatchString(tool) {
			return nil, fmt.Errorf("tool name %q is not an identifier — a tool allowlist may only name tools", tool)
		}
		if seen[tool] {
			continue
		}
		seen[tool] = true
		wiring.Tools = append(wiring.Tools, tool)
	}
	return wiring, nil
}

// Key shapes the generator refuses to write. The first three are the CI
// secret scanner's; kmh_ is this project's own agent credential, which
// belongs in a Secret the Agent REFERENCES and never in the Agent itself;
// the rest are the provider prefixes most likely to be pasted into an
// instructions file by accident.
//
// The first two are written with a one-character class — `sk-[a]nt-` rather
// than the literal — so this file does not itself trip the repository's
// "No secrets in tree" scan, which greps for exactly those prefixes.
var keyShapes = []struct {
	what string
	re   *regexp.Regexp
}{
	{"an Anthropic API key", regexp.MustCompile(`sk-[a]nt-`)},
	{"an OpenAI project key", regexp.MustCompile(`sk-[p]roj-`)},
	// The `\\?` is not decoration: on the way into a double-quoted scalar a
	// quote becomes \", so on the emitted document the separator and the
	// quote are no longer adjacent. Without it this shape could never fire
	// on the field it was written for — found in review, reproduced, fixed.
	{"an assigned API key", regexp.MustCompile(`(?i)api[_-]?key\s*[:=]\s*\\?["'][A-Za-z0-9_-]{20,}`)},
	{"a Kaimahi agent credential", regexp.MustCompile(`kmh_[A-Za-z0-9]{8,}`)},
	{"a GitHub token", regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{20,}`)},
	{"a GitHub fine-grained token", regexp.MustCompile(`github_pat_[A-Za-z0-9_]{20,}`)},
	{"a Slack token", regexp.MustCompile(`xox[baprs]-[A-Za-z0-9-]{10,}`)},
	{"a private key", regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)},
}

// RefuseKeyShapes fails closed when anything key-shaped is present.
//
// It is run TWICE: once over every raw input, and once over the finished
// document. Both are necessary and neither is sufficient. The document scan
// catches a key assembled across two flags, which no per-input check would
// see. The input scan catches what the document scan cannot: emission
// escapes, so a value on its way into a quoted scalar has its quotes turned
// into \" — which moved the text out from under the api-key shape and let a
// key through. (Found in review, reproduced, fixed.) Checking the value the
// operator actually typed removes that whole class.
func RefuseKeyShapes(document string) error {
	for _, shape := range keyShapes {
		if shape.re.MatchString(document) {
			return fmt.Errorf("refusing to write this manifest: it contains something shaped like %s.\n"+
				"  kmx never handles keys. An agent references a Secret; it does not carry one:\n"+
				"    kubectl -n kagent create secret generic <name> --from-file=api-key=/dev/stdin", shape.what)
		}
	}
	return nil
}

const defaultInstructions = `You are %s, a declarative kagent agent defined entirely in YAML and
running on Kubernetes. Answer briefly and in plain text, and say so
plainly when you do not know something.`

// Generate renders the Agent manifest.
func Generate(spec Spec) (string, error) {
	if err := ValidateName(spec.Name); err != nil {
		return "", err
	}
	// Every operator-supplied value, as typed — before any escaping can move
	// it out from under a key shape.
	inputs := []string{spec.Name, spec.Namespace, spec.Description, spec.ModelConfig, spec.Instructions, spec.Image}
	for _, e := range spec.Governance {
		inputs = append(inputs, e.Name, e.Value, e.SecretRef)
	}
	if spec.Tools != nil {
		inputs = append(inputs, spec.Tools.Server)
		inputs = append(inputs, spec.Tools.Tools...)
	}
	for _, input := range inputs {
		if err := RefuseKeyShapes(input); err != nil {
			return "", err
		}
	}
	if spec.Namespace == "" {
		spec.Namespace = "kagent"
	}
	if !nameRE.MatchString(spec.Namespace) {
		return "", fmt.Errorf("namespace %q is not a valid Kubernetes name", spec.Namespace)
	}
	if spec.Image == "" {
		// Declarative agents think through a ModelConfig by reference. BYO
		// agents have nowhere to put one — see Spec.Image.
		if spec.ModelConfig == "" {
			return "", fmt.Errorf("an agent needs a modelConfig")
		}
		if !nameRE.MatchString(spec.ModelConfig) {
			return "", fmt.Errorf("modelConfig %q is not a valid Kubernetes name", spec.ModelConfig)
		}
	}
	if spec.Placement != nil && spec.Image == "" {
		// Placement only reaches a workload we are deploying deliberately.
		// Silently emitting nodeSelector on a declarative agent would look
		// like isolation and buy none.
		return "", fmt.Errorf("--isolation needs --image: placement applies to a BYO agent's pod")
	}
	if spec.Description == "" {
		spec.Description = fmt.Sprintf("Agent %s, scaffolded by kmx.", spec.Name)
	}
	instructions := spec.Instructions
	if strings.TrimSpace(instructions) == "" {
		instructions = fmt.Sprintf(defaultInstructions, spec.Name)
	}

	name, err := quote(spec.Name)
	if err != nil {
		return "", err
	}
	namespace, err := quote(spec.Namespace)
	if err != nil {
		return "", err
	}
	description, err := quote(spec.Description)
	if err != nil {
		return "", err
	}
	modelConfig, err := quote(spec.ModelConfig)
	if err != nil {
		return "", err
	}
	systemMessage, err := literalBlock("systemMessage", instructions, 4, 6)
	if err != nil {
		return "", err
	}

	governance := "# This agent's model calls are UNGOVERNED: no budget, no ledger, no audit\n" +
		"# sits in front of them. `make plane` then `make govern` puts a governed\n" +
		"# preset in front of an agent (milestone 2 of kmx will own that seam).\n"
	if spec.Governed {
		governance = "# This agent thinks through a governed preset: its model calls are metered,\n" +
			"# budgeted and ledgered by the Kaimahi plane.\n"
	}

	var b strings.Builder
	b.WriteString("# Generated by `kmx agent create " + spec.Name + "`.\n" +
		"# This file IS the agent: edit it, review it, commit it, re-apply it.\n" +
		"# kmx adds no runtime — the kagent controller reconciles this document.\n" +
		governance)
	b.WriteString("apiVersion: kagent.dev/v1alpha2\nkind: Agent\nmetadata:\n")
	b.WriteString("  name: " + name + "\n")
	b.WriteString("  namespace: " + namespace + "\n")
	b.WriteString("spec:\n")
	b.WriteString("  description: " + description + "\n")

	if spec.Image != "" {
		byo, err := renderBYO(spec)
		if err != nil {
			return "", err
		}
		b.WriteString(byo)
		// The same final scan the declarative path ends with, and for the
		// same reason: the individual inputs were checked above, but the
		// guarantee this makes is about the DOCUMENT. A BYO manifest is the
		// one that carries an env block, so it is the last one that should
		// be allowed to skip the check on its way out.
		document := b.String()
		if err := RefuseKeyShapes(document); err != nil {
			return "", err
		}
		return document, nil
	}

	b.WriteString("  type: Declarative\n")
	b.WriteString("  declarative:\n")
	b.WriteString("    modelConfig: " + modelConfig + "\n")
	// The same modest requests k8s/tools-agent.yaml carries: a 2-CPU CI
	// runner already sits near its allocatable ceiling with kagent, Ollama
	// and two agents on it, and the default 100m request is what tips a
	// third agent into Pending.
	// The agent pod runs model output: it is the least trusted workload on
	// the cluster, and a scaffolder that emits no security context makes
	// every agent anyone creates the least constrained thing there. Same
	// posture as k8s/plane/proxy.yaml and the committed agents.
	//
	// runAsUser is not decoration. kagent's image declares its user by NAME
	// (`python`), and Kubernetes refuses to start a runAsNonRoot container
	// it cannot prove is non-root — "image has non-numeric user (python)",
	// at CreateContainer time, with no mention of the version. 1001 is what
	// that image runs as; CI pins it so a kagent bump is caught here rather
	// than in someone's cluster.
	//
	// A read-only root filesystem needs somewhere to write, so /tmp is an
	// emptyDir. Verified on a live agent: it starts, and it answers.
	b.WriteString("    deployment:\n" +
		"      resources:\n" +
		"        requests:\n" +
		"          cpu: 50m\n" +
		"          memory: 320Mi\n" +
		"        limits:\n" +
		"          memory: 1Gi\n" +
		"      podSecurityContext:\n" +
		"        runAsNonRoot: true\n" +
		"        runAsUser: 1001\n" +
		"        seccompProfile:\n" +
		"          type: RuntimeDefault\n" +
		"      securityContext:\n" +
		"        allowPrivilegeEscalation: false\n" +
		"        capabilities:\n" +
		"          drop: [ALL]\n" +
		"        readOnlyRootFilesystem: true\n" +
		"      volumes:\n" +
		"        - name: tmp\n" +
		"          emptyDir: {}\n" +
		"      volumeMounts:\n" +
		"        - name: tmp\n" +
		"          mountPath: /tmp\n")
	b.WriteString(systemMessage)

	if spec.Tools != nil {
		server, err := quote(spec.Tools.Server)
		if err != nil {
			return "", err
		}
		b.WriteString("    tools:\n" +
			"      - type: McpServer\n" +
			"        mcpServer:\n" +
			"          apiGroup: kagent.dev\n" +
			"          kind: RemoteMCPServer\n" +
			"          name: " + server + "\n" +
			"          # The allowlist is the authority: the agent may call these\n" +
			"          # tools and no others, whatever else the server offers.\n" +
			"          toolNames:\n")
		for _, tool := range spec.Tools.Tools {
			quoted, err := quote(tool)
			if err != nil {
				return "", err
			}
			b.WriteString("            - " + quoted + "\n")
		}
	}

	document := b.String()
	if err := RefuseKeyShapes(document); err != nil {
		return "", err
	}
	return document, nil
}

// renderBYO writes the `type: BYO` half of the document.
//
// Kept separate from the declarative path rather than threaded through it
// with conditionals: they are different artifacts that happen to share a
// kind, and the reader of either should not have to skip the other.
func renderBYO(spec Spec) (string, error) {
	image, err := quote(spec.Image)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("  type: BYO\n")
	b.WriteString("  byo:\n")
	b.WriteString("    deployment:\n")
	b.WriteString("      image: " + image + "\n")
	b.WriteString(renderBYOSecurity(spec.Identity))

	if len(spec.Governance) > 0 {
		// The seams a declarative agent gets by reference, carried across as
		// environment. kmx cannot verify the image reads them — the ledger
		// is what proves governance, not the presence of these variables.
		b.WriteString("      env:\n")
		for _, e := range spec.Governance {
			name, err := quote(e.Name)
			if err != nil {
				return "", err
			}
			b.WriteString("        - name: " + name + "\n")
			value, err := quote(e.Value)
			if err != nil {
				return "", err
			}
			if e.SecretRef == "" {
				b.WriteString("          value: " + value + "\n")
				continue
			}
			ref, err := quote(e.SecretRef)
			if err != nil {
				return "", err
			}
			b.WriteString("          valueFrom:\n" +
				"            secretKeyRef:\n" +
				"              name: " + ref + "\n" +
				"              key: " + value + "\n")
		}
	}

	if p := spec.Placement; p != nil {
		if len(p.NodeSelector) > 0 {
			b.WriteString("      nodeSelector:\n")
			for _, k := range slices.Sorted(maps.Keys(p.NodeSelector)) {
				key, err := quote(k)
				if err != nil {
					return "", err
				}
				val, err := quote(p.NodeSelector[k])
				if err != nil {
					return "", err
				}
				b.WriteString("        " + key + ": " + val + "\n")
			}
		}
		if len(p.Tolerations) > 0 {
			b.WriteString("      tolerations:\n")
			for _, t := range p.Tolerations {
				key, err := quote(t.Key)
				if err != nil {
					return "", err
				}
				b.WriteString("        - key: " + key + "\n")
				if t.Operator != "" {
					b.WriteString("          operator: " + t.Operator + "\n")
				}
				if t.Effect != "" {
					b.WriteString("          effect: " + t.Effect + "\n")
				}
			}
		}
	}
	return b.String(), nil
}

// renderBYOSecurity writes the security context of a bring-your-own agent.
//
// It is split in two on one line: what holds whatever is inside the image,
// and what does not. The first half is unconditional. Dropping every
// capability, refusing privilege escalation and applying the default seccomp
// profile cannot be made wrong by an image's contents — an agent that needs
// any of them is running model output with more authority than the plane it
// is governed by, and no --image should buy that.
//
// The second half — the user and a read-only root filesystem — is a claim
// ABOUT the image, and kmx has none. It is written when the operator stated
// one and replaced by a comment when they did not, so the gap is visible in
// the artifact and not only in the terminal that scrolled past. The manifest
// is the product here; a reviewer reading the committed file must be able to
// see what was left off.
func renderBYOSecurity(id Identity) string {
	var b strings.Builder
	b.WriteString("      # Holds whatever the image contains: no capabilities, no way to\n" +
		"      # gain privileges, the default seccomp filter.\n" +
		"      securityContext:\n" +
		"        allowPrivilegeEscalation: false\n" +
		"        capabilities:\n" +
		"          drop: [ALL]\n")
	switch {
	case id.UID > 0:
		b.WriteString("        readOnlyRootFilesystem: true\n")
	case id.Root:
		b.WriteString("        # Root, on purpose (--run-as-user root). The root filesystem stays\n" +
			"        # writable because an image that needs root usually needs to write.\n")
	default:
		b.WriteString("        # readOnlyRootFilesystem is NOT set: kmx cannot tell whether this\n" +
			"        # image writes outside /tmp. Add it once you know, together with a\n" +
			"        # tmp emptyDir if it needs one.\n")
	}
	b.WriteString("      podSecurityContext:\n" +
		"        seccompProfile:\n" +
		"          type: RuntimeDefault\n")
	switch {
	case id.UID > 0:
		// Both, not just runAsNonRoot: the kubelet cannot prove a named
		// user is non-root, so runAsNonRoot alone fails at CreateContainer
		// for every image that spells its USER as a name.
		b.WriteString(fmt.Sprintf("        runAsNonRoot: true\n        runAsUser: %d\n", id.UID))
	case id.Root:
		b.WriteString("        # runAsNonRoot is deliberately NOT set: --run-as-user root.\n")
	default:
		b.WriteString("        # runAsNonRoot is NOT set, so this pod may run as root. kmx will not\n" +
			"        # guess the image's UID: a wrong one fails the pod at CreateContainer\n" +
			"        # with a message that never names the image. Pass --run-as-user <uid>\n" +
			"        # (`docker image inspect --format '{{.Config.User}}' <image>`).\n")
	}
	if id.UID > 0 {
		b.WriteString("      volumes:\n" +
			"        - name: tmp\n" +
			"          emptyDir: {}\n" +
			"      volumeMounts:\n" +
			"        - name: tmp\n" +
			"          mountPath: /tmp\n")
	}
	return b.String()
}
