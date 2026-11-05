package main

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
)

func TestVersionJSONDiscoveryTarget(t *testing.T) {
	var out, diagnostics bytes.Buffer
	deps := versionTestDependencies(&out, &diagnostics)
	deps.goos = "linux"
	if e := execute([]string{"version", "-ojson"}, deps); e != nil {
		t.Fatal(e)
	}
	r := decodeMachineRecord(t, out.Bytes())
	found := false
	for _, v := range r["capabilities"].([]any) {
		m := v.(map[string]any)
		if m["operation"] == "cluster.discover" && m["scope"] == "target" {
			found = true
			if len(m) != 4 || m["output"] != "jsonl" || m["schemaVersion"] != float64(1) {
				t.Fatal(m)
			}
		}
	}
	if !found {
		t.Fatal("target not advertised")
	}
}
func TestDiscoveryTargetCLIContract(t *testing.T) {
	discoveryCLIEnv(t, `case "$*" in
 */api/v1/namespaces/kube-system) printf '%s' '{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"kube-system","uid":"cluster-uid"}}';;
 */api/v1/namespaces/dev) printf '%s' '{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"dev","uid":"namespace-uid"}}';;
 *) printf '%s' 'Error from server (Forbidden): private' >&2; exit 1;;
 esac`)
	for _, output := range []string{"jsonl", "text"} {
		var out, diagnostics bytes.Buffer
		deps, _ := testDependencies(&out, &diagnostics)
		deps.loadConfig = func(string, string) (*config.Config, error) { t.Fatal("config loaded"); return nil, nil }
		deps.newApp = func(*config.Config) *app.App { t.Fatal("configured app"); return nil }
		e := execute([]string{"cluster", "discover", "--context=chosen", "--namespace=dev", "-o", output}, deps)
		if e == nil {
			t.Fatal("false complete")
		}
		if output == "text" {
			if !strings.Contains(out.String(), "namespace-uid") {
				t.Fatal(out.String())
			}
			continue
		}
		d := json.NewDecoder(&out)
		var r map[string]any
		if e := d.Decode(&r); e != nil {
			t.Fatal(e)
		}
		if r["seq"] != float64(1) || r["exitCode"] != float64(1) || r["operation"] != "cluster.discover" {
			t.Fatal(r)
		}
		report := r["discovery"].(map[string]any)
		target := report["target"].(map[string]any)
		if report["scope"] != "target" || target["namespaceUid"] != "namespace-uid" || target["providers"] == nil || report["rows"] == nil || report["checks"] == nil {
			t.Fatal(r)
		}
		if e := d.Decode(&r); e != io.EOF {
			t.Fatal(e)
		}
	}
}
func TestDiscoveryTargetOutputFailure(t *testing.T) {
	discoveryCLIEnv(t, `printf '%s' 'Error from server (Forbidden): private' >&2; exit 1`)
	c := newClusterDiscoveryCommand(&commandState{contextFlag: "chosen"})
	c.SilenceUsage, c.SilenceErrors = true, true
	c.SetOut(&discoveryShortWriter{})
	c.SetArgs([]string{"--namespace=dev", "-ojsonl"})
	e := c.Execute()
	if e == nil || !strings.Contains(e.Error(), "output") {
		t.Fatal(e)
	}
}
