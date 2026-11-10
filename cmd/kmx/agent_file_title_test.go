package main

import (
	"bytes"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/localpreview"
)

func TestFileTestTitleRoundTrip(t *testing.T) {
	for _, title := range []string{"", "Says hello é 🙂"} {
		for _, format := range []string{"text", "json", "jsonl"} {
			t.Run(format+"/"+title, func(t *testing.T) {
				t.Setenv("KMX_HOME", privateCommandHome(t))
				file, tests := cliPairFiles(t, false)
				source := cliPairTests
				if title != "" {
					source = strings.Replace(source, "id: greeting\n", "id: greeting\ntitle: "+title+"\n", 1)
				}
				if err := os.WriteFile(tests, []byte(source), 0600); err != nil {
					t.Fatal(err)
				}
				var out, diag bytes.Buffer
				deps, _ := testDependencies(&out, &diag)
				deps.fileRunner = localpreview.NewRunner(localpreview.RunnerOptions{JournalDir: func() (string, error) { return privateCommandHome(t), nil }})
				if err := execute([]string{"test", file, "-o", format}, deps); err != nil {
					t.Fatalf("%v: %s %s", err, &out, &diag)
				}
				if format == "text" {
					label := "PASS greeting"
					if title != "" {
						label += " — " + title
					}
					if !regexp.MustCompile("(?m)^" + regexp.QuoteMeta(label) + ` \([0-9]+ms\)$`).Match(out.Bytes()) {
						t.Fatalf("wrong label: %s", &out)
					}
					return
				}
				var rows []map[string]any
				if format == "json" {
					var result struct{ Examples []map[string]any }
					if err := json.Unmarshal(out.Bytes(), &result); err != nil {
						t.Fatal(err)
					}
					rows = result.Examples
				} else {
					for _, row := range fileRecords(t, out.Bytes()) {
						if row["caseId"] != nil {
							rows = append(rows, row)
						}
						if row["title"] != nil && row["caseId"] == nil {
							t.Fatalf("orphan title: %v", row)
						}
					}
				}
				if len(rows) == 0 {
					t.Fatalf("no case rows: %s", &out)
				}
				for _, row := range rows {
					id := row["id"]
					if format == "jsonl" {
						id = row["caseId"]
					}
					if id != "greeting" {
						t.Fatalf("identity changed: %v", row)
					}
					got, present := row["title"]
					if present != (title != "") || present && got != title {
						t.Fatalf("wrong optional title: %v", row)
					}
				}
			})
		}
	}
}

func TestFileInvalidTitleRefusesBeforeExecution(t *testing.T) {
	file, tests := cliPairFiles(t, false)
	if err := os.WriteFile(tests, []byte(strings.Replace(cliPairTests, "id: greeting\n", "id: greeting\ntitle: 42\n", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	var out, diag bytes.Buffer
	deps, _ := testDependencies(&out, &diag)
	runner := &machineCLIRunner{}
	deps.fileRunner = runner
	err := execute([]string{"test", file, "-ojson"}, deps)
	if err == nil || runner.fake.calls != 0 || !strings.Contains(strings.ToLower(out.String()+diag.String()), "title") {
		t.Fatalf("invalid title executed or unclear error: %v %s %s", err, &out, &diag)
	}
}
