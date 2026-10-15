package app

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceLiftCachedThresholdDoesNotInventPassingRuns(t *testing.T) {
	a, opt, state, _, f, _ := workspaceLiftFixture(t, func(root string) {
		path := filepath.Join(root, "eval/example.yaml")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, path, string(raw)+"\nruns: 2\nminPasses: 1\n")
	})
	a.Out, a.Err = io.Discard, io.Discard
	tasks := 0
	state.afterCreate = func(key string) {
		if strings.HasPrefix(key, "Task/") {
			tasks++
			f.answers[nativeCanary+" first"] = nativeCanary + " answer"
			if tasks == 1 {
				f.answers[nativeCanary+" first"] = "wrong"
			}
		}
	}
	for run := 0; run < 2; run++ {
		p, err := PrepareWorkspaceLift(t.Context(), opt)
		if err != nil {
			t.Fatal(err)
		}
		result, err := a.LiftWorkspace(t.Context(), p)
		if err != nil {
			p.Close()
			t.Fatal(err)
		}
		if err := p.Close(); err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(result.Plan)
		if err != nil {
			t.Fatal(err)
		}
		var plan map[string]any
		if err := json.Unmarshal(raw, &plan); err != nil {
			t.Fatal(err)
		}
		if plan["caseRuns"] != float64(3) || plan["cases"] != nil {
			t.Fatalf("ambiguous execution count unit: %s", raw)
		}
		if result.Eval.Passed != 2 || result.Eval.Total != 3 {
			t.Fatalf("run %d invented passed runs: %+v", run, result.Eval)
		}
	}
	if tasks != 3 {
		t.Fatalf("cached threshold re-executed: %d tasks", tasks)
	}
}
