// Package scaffold generates reviewable Kubernetes YAML and provides shared
// name, tool-reference and credential-shape checks. Orka authoring is in orka.go;
// legacy kagent editor and MCP callers still use the helpers in this file.
package scaffold

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
)

// ToolWiring is one MCP server and the tools the agent may call on it.
type ToolWiring struct {
	Server string
	Tools  []string
}

// RFC 1123 label: also used by the other Kubernetes scaffolders.
var nameRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
var identifierRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

var reserved = map[string]string{
	"hello-world": "the agent `kmx up` creates from k8s/hello-world.yaml",
	"hello-tools": "the agent `kmx up` creates from k8s/tools-agent.yaml",
}

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

// ParseTools retains the legacy kagent MCP server:allowlist contract for editor
// and tool callers. Orka create deliberately does not use this syntax.
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

// RefuseKeyShapes is shared by all scaffolders. Scan both raw input and final
// output: escaping can hide an input shape, while joining can create a new one.
func RefuseKeyShapes(document string) error {
	if shape := secretshapes.Match(document); shape != nil {
		return fmt.Errorf("refusing to write this manifest: it contains something shaped like %s.\n"+
			"  kmx never handles keys. An agent references a Secret; it does not carry one:\n"+
			"    kubectl -n kagent create secret generic <name> --from-file=api-key=/dev/stdin", shape.What)
	}
	return nil
}
