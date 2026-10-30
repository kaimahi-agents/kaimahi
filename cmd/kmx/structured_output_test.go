package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestYAMLReportsMatchJSON(t *testing.T) {
	t.Setenv("KMX_HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	source, _ := filepath.Abs("../../.github/agents/hello.agent.md")
	for _, args := range [][]string{{"list", "--all"}, {"info", "--directory", t.TempDir()}, {"validate", source}, {"inspect", source}, {"cluster", "targets"}, {"list", "--definitions", "--type", "md", "--directory", "../.."}, {"settings"}} {
		t.Run(args[0]+args[len(args)-1], func(t *testing.T) {
			values := []any{}
			for _, format := range []string{"json", "yaml"} {
				var out, diag bytes.Buffer
				deps, _ := testDependencies(&out, &diag)
				err := execute(append(append([]string{}, args...), "-o", format), deps)
				if err != nil {
					t.Fatal(err)
				}
				var value any
				if format == "json" {
					err = json.Unmarshal(out.Bytes(), &value)
				} else {
					var v any
					err = yaml.Unmarshal(out.Bytes(), &v)
					if err == nil {
						raw, e := json.Marshal(v)
						if e != nil {
							t.Fatal(e)
						}
						err = json.Unmarshal(raw, &value)
					}
				}
				if err != nil {
					t.Fatalf("%s: %s: %v", format, &out, err)
				}
				values = append(values, value)
			}
			if !reflect.DeepEqual(values[0], values[1]) {
				t.Fatalf("different contracts: %+v / %+v", values[0], values[1])
			}
		})
	}
}
func TestYAMLHelpIsInert(t *testing.T) {
	var out, diag bytes.Buffer
	deps, loads := testDependencies(&out, &diag)
	if err := execute([]string{"list", "-o", "yaml", "--help"}, deps); err != nil {
		t.Fatal(err)
	}
	if *loads != 0 || !bytes.Contains(out.Bytes(), []byte("Usage:")) {
		t.Fatal(out.String())
	}
}
