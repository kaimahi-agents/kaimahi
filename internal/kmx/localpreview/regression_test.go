package localpreview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func TestResponseModelMustMatchSelectedModel(t *testing.T) {
	for name, model := range map[string]string{
		"different": `"other-model"`,
		"missing":   "",
		"empty":     `""`,
		"null":      `null`,
	} {
		t.Run(name, func(t *testing.T) {
			server := newCompatible(t, false, func(w http.ResponseWriter, _ *http.Request, _ chatRequest) {
				body := strings.Replace(reply("Hello, Ada"), `"model":"m",`, "", 1)
				if model != "" {
					body = `{"model":` + model + `,` + body[1:]
				}
				fmt.Fprint(w, body)
			})
			dir := journalDir(t)
			result, err := Test(testContext(t), modelAgent(t, "selected-model"), Options{JournalDir: dir, Endpoint: &Endpoint{BaseURL: server.URL}})
			if err != nil || len(result.Examples) != 1 {
				t.Fatalf("result = %+v, err = %v", result, err)
			}
			row := result.Examples[0]
			if result.Verdict != agentruntime.EvaluationUnknown || row.Answer != "" || row.Refusal == nil || row.Refusal.Kind != RefusedMalformed {
				t.Fatalf("unattributable response accepted: %+v", result)
			}
			inspection, err := Inspect(testContext(t), dir, result.RunID)
			if err != nil || len(inspection.Sessions) != 1 || inspection.Sessions[0].Outcome != "incomplete" || inspection.Sessions[0].Answer != "" || server.hits.Load() != 1 {
				t.Fatalf("unattributable answer retained or retried: %+v, err = %v", inspection, err)
			}
		})
	}
}

func TestAssertionMetadataCredentialRefusedBeforeEffects(t *testing.T) {
	for _, source := range []string{"agent", "judge"} {
		for _, field := range []string{"id", "type", "judgeModel", "judgeVersion"} {
			t.Run(source+"/"+field, func(t *testing.T) {
				agent := modelAgent(t, "m")
				minScore := 50
				a := agentruntime.EvaluationAssertion{ID: "opaque-assertion", Type: "llmJudge", Rubric: "greets", MinScore: &minScore, JudgeModel: "opaque-judge-model", JudgeVersion: "opaque-version"}
				values := map[string]string{"id": a.ID, "type": a.Type, "judgeModel": a.JudgeModel, "judgeVersion": a.JudgeVersion}
				key := values[field]
				agent.Examples[0].Assertions = []agentruntime.EvaluationAssertion{a}
				t.Setenv("KMX_TEST_METADATA_KEY", key)
				server := newCompatible(t, true, func(w http.ResponseWriter, _ *http.Request, _ chatRequest) { fmt.Fprint(w, reply("Hello, Ada")) })
				options := Options{JournalDir: journalDir(t), Endpoint: &Endpoint{BaseURL: server.URL}, JudgeEndpoint: &Endpoint{BaseURL: server.URL}, Judge: stubJudge, Transport: server.Client().Transport}
				if source == "agent" {
					options.Endpoint.KeyEnv = "KMX_TEST_METADATA_KEY"
				} else {
					options.JudgeEndpoint.KeyEnv = "KMX_TEST_METADATA_KEY"
				}
				result, err := Test(context.Background(), agent, options)
				var refusal *Refusal
				if !errors.As(err, &refusal) || refusal.Kind != RefusedCredentialContent || strings.Contains(err.Error(), key) {
					t.Fatalf("metadata credential not refused safely: result = %+v, err = %v", result, err)
				}
				encoded, marshalErr := json.Marshal(result)
				if marshalErr != nil || strings.Contains(string(encoded), key) || result.RunID != "" {
					t.Fatal("credential returned in result")
				}
				if _, statErr := os.Lstat(options.JournalDir); !errors.Is(statErr, os.ErrNotExist) || server.hits.Load() != 0 {
					t.Fatalf("effects before refusal: journal stat = %v, sends = %d", statErr, server.hits.Load())
				}
			})
		}
	}
}
