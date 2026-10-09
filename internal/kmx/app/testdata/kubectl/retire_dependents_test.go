package kubectl

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"text/template"
)

func TestRetireDependentsKubectl(t *testing.T) {
	if os.Getenv("KMX_DEPENDENTS_DIR") == "" {
		return
	}
	args := os.Args[slices.Index(os.Args, "--")+1:]
	if len(args) != 7 || args[0] != "--context" || args[1] != "kind-test" || args[2] != "--request-timeout=10s" || args[3] != "get" || args[5] != "--all-namespaces" || !strings.HasPrefix(args[6], "-o=go-template=") {
		os.Exit(2)
	}
	if args[4] == os.Getenv("KMX_DEPENDENTS_DENIED") {
		fmt.Fprint(os.Stderr, os.Getenv("KMX_DEPENDENTS_ERROR"))
		os.Exit(1)
	}
	if args[4] == os.Getenv("KMX_DEPENDENTS_OVERSIZE") {
		_, _ = os.Stdout.Write([]byte(strings.Repeat("X", 5<<20)))
		os.Exit(0)
	}
	raw, err := os.ReadFile(filepath.Join(os.Getenv("KMX_DEPENDENTS_DIR"), args[4]+".json"))
	if err != nil {
		os.Exit(2)
	}
	var data map[string]any
	if json.Unmarshal(raw, &data) != nil {
		os.Exit(2)
	}
	project, err := template.New("projection").Parse(strings.TrimPrefix(args[6], "-o=go-template="))
	if err != nil || project.Execute(os.Stdout, data) != nil {
		os.Exit(2)
	}
	os.Exit(0)
}
