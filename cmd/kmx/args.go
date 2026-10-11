package main

import (
	"fmt"
	"strings"
)

// extractContext preserves kmx's compatibility contract: --context and the
// legacy -context spelling may appear anywhere before --, including after
// positionals. Cobra owns every other flag and treats everything after -- as data.
func extractContext(argv []string) ([]string, string, error) {
	var kept []string
	value := ""
	seen := false
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		if arg == "--" {
			kept = append(kept, argv[i:]...)
			break
		}
		name, inline, hasInline := strings.Cut(arg, "=")
		if name != "--context" && name != "-context" {
			kept = append(kept, arg)
			continue
		}
		seen = true
		if hasInline {
			value = inline
			continue
		}
		if i+1 >= len(argv) {
			return nil, "", fmt.Errorf("--context needs a context name")
		}
		i++
		value = argv[i]
	}
	if seen && strings.TrimSpace(value) == "" {
		return nil, "", fmt.Errorf("--context needs a context name")
	}
	return kept, value, nil
}

func joinArgs(args []string) string { return strings.Join(args, " ") }
