// Package scaffold generates reviewable Kubernetes YAML and provides shared
// name, reference and credential-shape checks. Orka authoring is in orka.go;
// the helpers here are shared by every scaffolder.
package scaffold

import (
	"fmt"
	"regexp"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
)

// RFC 1123 label: also used by the other Kubernetes scaffolders.
var nameRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// identifierRE bounds an Orka tool or skill reference to something that can
// only name a resource: no whitespace, no quoting, nothing a YAML emitter
// would have to escape. orka.go is its only caller.
var identifierRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// No name is reserved. `hello-world` and `hello-tools` were, because
// `kmx up` created agents under those names from k8s/hello-world.yaml and
// k8s/tools-agent.yaml. Those manifests and the runtime that applied them
// are gone, so nothing occupies the names — and refusing a name kmx would
// not collide with is a rule with nothing behind it.

func ValidateName(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("an agent needs a name: kmx agent create <name>")
	case len(name) > 63:
		return fmt.Errorf("agent name %q is %d characters; Kubernetes allows 63", name, len(name))
	case !nameRE.MatchString(name):
		return fmt.Errorf("agent name %q is not a valid Kubernetes name — lowercase letters, digits and dashes, starting and ending with a letter or digit (e.g. billing-investigator)", name)
	}
	return nil
}

// RefuseKeyShapes is shared by all scaffolders. Scan both raw input and final
// output: escaping can hide an input shape, while joining can create a new one.
func RefuseKeyShapes(document string) error {
	if shape := secretshapes.Match(document); shape != nil {
		return fmt.Errorf("refusing to write this manifest: it contains something shaped like %s.\n"+
			"  kmx never handles keys. An agent references a Secret; it does not carry one:\n"+
			"    kubectl -n <namespace> create secret generic <name> --from-file=api-key=/dev/stdin", shape.What)
	}
	return nil
}
