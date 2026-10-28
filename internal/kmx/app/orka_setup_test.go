package app

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestOrkaSetupFreezesInstallerAndEnrollmentBeforeEffects(t *testing.T) {
	f := newOrkaFixture(t, nil)
	chart := enrollmentOrkaChart(t, nil)
	opt := candidateOrkaOptions(t, chart)
	enabled := true
	t.Setenv("KMX_TEST_NAMESPACE_JSON", `{"kind":"Namespace","metadata":{"name":"qualification-tasks","uid":"tasks-1"}}`)
	if err := f.app.prepareOrkaSetup(OrkaSetupOptions{
		Chart: opt.Chart, ChartSHA256: opt.ChartSHA256, Enforcement: &enabled,
	}); err != nil {
		t.Fatal(err)
	}
	if f.writes(t) != "" {
		t.Fatal("preparing setup mutated the cluster")
	}
	enabled = false
	if err := os.WriteFile(opt.Chart, []byte("changed after selection"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := f.app.prepareOrkaRuntime(); err != nil {
		t.Fatal(err)
	}
	want, _, err := orkaChartCRDs(chart)
	if err != nil {
		t.Fatal(err)
	}
	applied, err := os.ReadFile(f.stdin)
	if err != nil || !bytes.Equal(applied, want) {
		t.Fatalf("runtime setup did not consume the frozen verified CRDs: %v", err)
	}
	if !strings.Contains(f.calls(t), "enforcement.enabled=true") ||
		!strings.Contains(f.calls(t), "kmxInstallerSHA256="+opt.ChartSHA256) {
		t.Fatal("runtime setup lost exact installer identity or explicit enrollment")
	}
	if f.app.orkaNativeCandidate != &f.app.orkaSetup.spec {
		t.Fatal("native evaluation did not retain the same selected installation")
	}
}

func TestOrkaSetupOmissionDoesNotFetchOrEnroll(t *testing.T) {
	f := newOrkaFixture(t, nil)
	f.app.orkaInstaller = "http://127.0.0.1:1/must-not-fetch"
	if err := f.app.prepareOrkaSetup(OrkaSetupOptions{}); err != nil {
		t.Fatal(err)
	}
	if f.app.orkaSetup != nil || f.app.orkaNativeCandidate != nil ||
		f.calls(t) != "" || f.writes(t) != "" {
		t.Fatal("omitted options changed ordinary setup")
	}
}

func TestOrkaSetupCarriesTaskNamespaceWithoutChangingGovernance(t *testing.T) {
	f := newOrkaFixture(t, nil)
	opt := candidateOrkaOptions(t, splitCandidateChart(t, "candidate-tasks"))
	if err := f.app.prepareOrkaSetup(OrkaSetupOptions{Chart: opt.Chart, ChartSHA256: opt.ChartSHA256}); err != nil {
		t.Fatal(err)
	}
	if f.app.quickstartCreateOptions().Namespace != "candidate-tasks" ||
		!strings.Contains(f.app.orkaSetupNamespaces(), "candidate-tasks") {
		t.Fatal("quickstart target and guard lost the selected Task namespace")
	}
	result := f.app.quickstartResult("question", "answer", f.app.timeNow())
	if result.Governed || !strings.Contains(result.Manifest, "candidate-tasks") {
		t.Fatal("candidate setup changed governance semantics or reported the wrong namespace")
	}
}

func TestOrkaSetupFirstAnswerRequirementIsFrozenAndIndependent(t *testing.T) {
	for _, enrollment := range []string{"omitted", "true", "false"} {
		t.Run(enrollment, func(t *testing.T) {
			f := newOrkaFixture(t, nil)
			opt := candidateOrkaOptions(t, enrollmentOrkaChart(t, nil))
			setup := OrkaSetupOptions{Chart: opt.Chart, ChartSHA256: opt.ChartSHA256}
			if enrollment != "omitted" {
				enabled := enrollment == "true"
				setup.Enforcement = &enabled
			}
			requirement := testTaskEnforcement()
			path := writeTaskEnforcement(t, requirement)
			if err := f.app.prepareQuickstartEnforcement(setup, path); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("changed after selection"), 0600); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(f.app.taskEnforcement, requirement) {
				t.Fatal("first-answer requirement lost its complete original bytes")
			}
			if (f.app.orkaSetup.spec.enrollment == nil) != (setup.Enforcement == nil) {
				t.Fatal("Task requirement implicitly changed provider enrollment")
			}
			if !strings.Contains(f.app.quickstartFollowups()[0], "--enforcement-file") {
				t.Fatal("protected first-answer follow-up silently suggested unprotected chat")
			}
			if f.writes(t) != "" {
				t.Fatal("preparing first-answer input mutated the cluster")
			}
		})
	}
}

func TestOrkaSetupRejectsIrrelevantStepBeforeEffects(t *testing.T) {
	f := newOrkaFixture(t, nil)
	enabled := true
	if err := f.app.UpWithOptions("model", OrkaSetupOptions{Enforcement: &enabled}); err == nil {
		t.Fatal("model-only setup silently ignored provider enrollment")
	}

	if f.calls(t) != "" || f.writes(t) != "" {
		t.Fatal("irrelevant setup option performed external work")
	}
}

func TestOrkaSetupNativeStagesReverifyFrozenInstalledSelection(t *testing.T) {
	for _, outcome := range []string{"matching", "changed installed archive", "wrong namespace", "missing installation"} {
		t.Run(outcome, func(t *testing.T) {
			f := newOrkaFixture(t, nil)
			chart := candidateOrkaArchive(t, nil)
			opt := candidateOrkaOptions(t, chart)
			if err := f.app.prepareOrkaSetup(OrkaSetupOptions{Chart: opt.Chart, ChartSHA256: opt.ChartSHA256}); err != nil {
				t.Fatal(err)
			}
			spec := f.app.orkaSetup.spec
			values := candidateOrkaValues(t)
			values["kmxInstallerSHA256"] = spec.archiveDigest
			if outcome == "changed installed archive" {
				values["kmxInstallerSHA256"] = strings.Repeat("b", 64)
			}
			raw, err := json.Marshal(values)
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("KMX_TEST_HELM_VALUES", string(raw))
			t.Setenv("KMX_TEST_HELM_LIST", `[{"name":"orka","chart":"orka-0.3.0-local.1","app_version":"v0.3.0-local.1","status":"deployed"}]`)
			t.Setenv("KMX_TEST_DEPLOY_JSON", orkaDeployJSON(t, candidateOrkaController(spec.controllerImage(), spec.taskNamespace)))
			namespace := spec.taskNamespace
			if outcome == "wrong namespace" {
				namespace = "other"
			} else if outcome == "missing installation" {
				t.Setenv("KMX_TEST_HELM_LIST", "[]")
			}
			if err := os.Remove(opt.Chart); err != nil {
				t.Fatal(err)
			}
			err = f.app.selectOrkaNativeCandidate(t.Context(), "", "", namespace)
			if outcome == "matching" {
				if err != nil || f.app.orkaNativeCandidate == nil ||
					f.app.orkaNativeCandidate.archiveDigest != spec.archiveDigest {
					t.Fatalf("native stage lost its frozen setup selection: %v", err)
				}
			} else if err == nil || f.app.orkaNativeCandidate != nil {
				t.Fatal("native stage accepted changed, absent or wrong-namespace installation")
			}
			if f.writes(t) != "" {
				t.Fatal("native selection repaired or installed the runtime")
			}
		})
	}
}

func TestOrkaSetupCompletePathRejectsChangedChartBeforeEffects(t *testing.T) {
	f := newOrkaFixture(t, nil)
	opt := candidateOrkaOptions(t, candidateOrkaArchive(t, nil))
	if err := os.WriteFile(opt.Chart, []byte("not the selected archive"), 0600); err != nil {
		t.Fatal(err)
	}
	err := f.app.UpWithOptions("", OrkaSetupOptions{Chart: opt.Chart, ChartSHA256: opt.ChartSHA256})
	if err == nil {
		t.Fatal("complete local up ignored the selected chart")
	}
	if f.calls(t) != "" || f.writes(t) != "" {
		t.Fatal("invalid setup performed cluster, model or runtime work")
	}
}
