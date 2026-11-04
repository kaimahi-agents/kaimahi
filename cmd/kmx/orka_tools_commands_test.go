package main

import (
	"bytes"
	"errors"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOrkaToolsCommandBoundary(t *testing.T) {
	for _, args := range [][]string{{"--context", "target"}, {"--namespace", "orka-system"}, {"--context", "target", "--namespace", "../private"}, {"--context", "target", "--namespace", "orka-system", "-o", "bad"}, {"--help"}} {
		var out, diag bytes.Buffer
		deps, loads := testDependencies(&out, &diag)
		err := execute(append([]string{"cluster", "orka", "tools"}, args...), deps)
		if *loads != 0 {
			t.Fatal("configuration before validation")
		}
		if args[0] != "--help" && err == nil {
			t.Fatalf("accepted %v", args)
		}
		if strings.Contains(out.String()+diag.String(), "../private") {
			t.Fatal("reflected selector")
		}
	}
}
func TestOrkaToolsCommandJSON(t *testing.T) {
	tools := t.TempDir()
	script := `#!/bin/sh
[ "$KUBERC" = off ] && [ "$KUBECTL_KUBERC" = false ] || exit 91
[ "$1" = --context ] && [ "$2" = explicit ] && [ "$3" = --request-timeout=10s ] && [ "$4" = get ] && [ "$5" = --raw ] || exit 92
case "$6" in
 /api/v1/namespaces/kube-system) printf '{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"kube-system","uid":"cluster"}}';;
 /api/v1/namespaces/orka-system) printf '{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"orka-system","uid":"ns"}}';;
 *) printf 'Error from server (Forbidden): PRIVATE_TOOL_DIAGNOSTIC' >&2; exit 1;;
esac
`
	if err := os.WriteFile(filepath.Join(tools, "kubectl"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	var out, diag bytes.Buffer
	deps, _ := testDependencies(&out, &diag)
	err := execute([]string{"cluster", "orka", "tools", "--context", "explicit", "--namespace", "orka-system", "-o", "json"}, deps)
	if err == nil || exitCode(err) != 1 {
		t.Fatal(err)
	}
	record := decodeMachineRecord(t, out.Bytes())
	if record["operation"] != "cluster.orka.tools" || record["kind"] != "kmx.result" || record["exitCode"] != float64(1) {
		t.Fatal(record)
	}
	inventory := record["inventory"].(map[string]any)
	if inventory["complete"] != false || inventory["reason"] != "read_denied" || inventory["target"].(map[string]any)["context"] != "explicit" {
		t.Fatal(inventory)
	}
	if strings.Contains(out.String()+diag.String()+err.Error(), "PRIVATE") {
		t.Fatal("raw capture escaped")
	}
}
func TestOrkaToolsConfigAndWindows(t *testing.T) {
	for _, goos := range []string{"linux", "windows"} {
		var out, diag bytes.Buffer
		deps, loads := testDependencies(&out, &diag)
		deps.goos = goos
		deps.loadConfig = func(string, string) (*config.Config, error) { *loads++; return nil, errors.New("PRIVATE_CONFIG") }
		err := execute([]string{"cluster", "orka", "tools", "--context", "explicit", "--namespace", "orka-system", "-o", "json"}, deps)
		if err == nil || strings.Contains(out.String()+diag.String()+err.Error(), "PRIVATE") {
			t.Fatal("unsafe failure", err)
		}
		if goos == "windows" && *loads != 0 {
			t.Fatal("Windows loaded config")
		}
	}
}
