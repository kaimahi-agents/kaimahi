package agentsuite

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestDiffValuesTreatEquivalentJSONNumbersAsEqual(t *testing.T) {
	before := map[string]any{
		"number": json.Number("1"),
		"nested": []any{json.Number("1e0")},
	}
	after := map[string]any{
		"number": json.Number("1.0"),
		"nested": []any{json.Number("1.00")},
	}

	same, err := sameDiffValue(before, after)
	if err != nil {
		t.Fatalf("sameDiffValue() error = %v", err)
	}
	if !same {
		t.Fatal("sameDiffValue() reported equivalent JSON numbers as different")
	}
	fields, err := diffFields("", before, true, after, true)
	if err != nil {
		t.Fatalf("diffFields() error = %v", err)
	}
	if len(fields) != 0 {
		t.Fatalf("diffFields() = %+v, want no changes", fields)
	}
}

func TestDiffPathsReportsFieldChangesAndAffectedAgents(t *testing.T) {
	before := writeMinimalSuite(t)
	after := writeMinimalSuite(t)

	var agent Agent
	readStrictJSON(t, filepath.Join(after, "agents", "writer.json"), &agent)
	agent.Description = "Writes release notes"
	agentDigest := mustWriteJSON(t, after, "agents/writer.json", MediaTypeAgent, agent)

	var profile BuildProfile
	readStrictJSON(t, filepath.Join(after, "build-profiles", "default.json"), &profile)
	profile.SourceEpoch++
	profileDigest := mustWriteJSON(t, after, "build-profiles/default.json", MediaTypeBuildProfile, profile)

	var suite Suite
	readStrictJSON(t, filepath.Join(after, "agentsuite.json"), &suite)
	suite.Agents[0].Descriptor = agentDigest
	suite.BuildProfiles[0].Descriptor = profileDigest
	mustWriteJSON(t, after, "agentsuite.json", MediaTypeSuite, suite)

	report, err := DiffPaths(before, after)
	if err != nil {
		t.Fatalf("DiffPaths() error = %v", err)
	}
	if report.Summary.Added != 0 || report.Summary.Removed != 0 || report.Summary.Modified != 2 {
		t.Fatalf("summary = %+v", report.Summary)
	}
	assertDiffField(t, report, "agent", "writer", "description", nil, `"Writes release notes"`)
	assertDiffField(t, report, "buildProfile", "default", "sourceEpoch", "1", "2")
	if len(report.AffectedAgents) != 1 || report.AffectedAgents[0].ID != "writer" {
		t.Fatalf("affected agents = %+v", report.AffectedAgents)
	}
	reasons := strings.Join(report.AffectedAgents[0].Reasons, "\n")
	for _, want := range []string{"agent definition modified", "build profile default modified"} {
		if !strings.Contains(reasons, want) {
			t.Errorf("affected-agent reasons %q do not contain %q", reasons, want)
		}
	}
}

func TestDiffPathsReportsAddedAndRemovedLogicalRecords(t *testing.T) {
	before := filepath.Join("testdata", "minimal")
	after := filepath.Join("testdata", "coordinator-workers")
	report, err := DiffPaths(before, after)
	if err != nil {
		t.Fatalf("DiffPaths() error = %v", err)
	}
	if !hasRecordChange(report, DiffAdded, "agent", "reviewer") {
		t.Fatalf("changes do not include added reviewer: %+v", report.Changes)
	}
	if !hasRecordChange(report, DiffAdded, "composition", "coordinator@linux/amd64") {
		t.Fatalf("changes do not include added coordinator composition: %+v", report.Changes)
	}
	if report.Summary.AgentsAffected < 2 {
		t.Fatalf("affected agents = %+v", report.AffectedAgents)
	}

	reverse, err := DiffPaths(after, before)
	if err != nil {
		t.Fatalf("reverse DiffPaths() error = %v", err)
	}
	if !hasRecordChange(reverse, DiffRemoved, "agent", "reviewer") {
		t.Fatalf("reverse changes do not include removed reviewer: %+v", reverse.Changes)
	}
}

func TestDiffPathsPropagatesChangedWorkerToInvokingAgent(t *testing.T) {
	before := copyAgentSuiteDirectory(t, filepath.Join("testdata", "coordinator-workers"))
	after := copyAgentSuiteDirectory(t, filepath.Join("testdata", "coordinator-workers"))

	var writer Agent
	readStrictJSON(t, filepath.Join(after, "agents", "writer.json"), &writer)
	writer.Description = "Writes revised content"
	writerDigest := mustWriteJSON(t, after, "agents/writer.json", MediaTypeAgent, writer)

	var suite Suite
	readStrictJSON(t, filepath.Join(after, "agentsuite.json"), &suite)
	for index := range suite.Agents {
		if suite.Agents[index].ID == "writer" {
			suite.Agents[index].Descriptor = writerDigest
		}
	}
	mustWriteJSON(t, after, "agentsuite.json", MediaTypeSuite, suite)

	report, err := DiffPaths(before, after)
	if err != nil {
		t.Fatalf("DiffPaths() error = %v", err)
	}
	assertAffectedAgentReason(t, report, "writer", "agent definition modified")
	assertAffectedAgentReason(t, report, "coordinator", "invoked agent writer modified")
}

func TestAffectedAgentsPropagatesInvocationImpactsTransitivelyAcrossBothVersions(t *testing.T) {
	changed := map[diffRecordKey]string{{kind: "agent", id: "worker"}: DiffModified}
	tests := []struct {
		name   string
		before map[string]Agent
		after  map[string]Agent
	}{
		{
			name: "unchanged invocation graph",
			before: map[string]Agent{
				"worker":      {ID: "worker"},
				"coordinator": {ID: "coordinator", Invokes: []AgentInvoke{{Agent: "worker"}}},
				"director":    {ID: "director", Invokes: []AgentInvoke{{Agent: "coordinator"}}},
			},
			after: map[string]Agent{
				"worker":      {ID: "worker"},
				"coordinator": {ID: "coordinator", Invokes: []AgentInvoke{{Agent: "worker"}}},
				"director":    {ID: "director", Invokes: []AgentInvoke{{Agent: "coordinator"}}},
			},
		},
		{
			name: "added invocation edge",
			before: map[string]Agent{
				"worker": {ID: "worker"},
				"caller": {ID: "caller"},
			},
			after: map[string]Agent{
				"worker": {ID: "worker"},
				"caller": {ID: "caller", Invokes: []AgentInvoke{{Agent: "worker"}}},
			},
		},
		{
			name: "removed invocation edge",
			before: map[string]Agent{
				"worker": {ID: "worker"},
				"caller": {ID: "caller", Invokes: []AgentInvoke{{Agent: "worker"}}},
			},
			after: map[string]Agent{
				"worker": {ID: "worker"},
				"caller": {ID: "caller"},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			report := affectedAgents(
				diffSnapshot{agents: test.before},
				diffSnapshot{agents: test.after},
				changed,
			)
			assertAffectedReason(t, report, "worker", "agent definition modified")
			if _, ok := test.before["coordinator"]; ok {
				assertAffectedReason(t, report, "coordinator", "invoked agent worker modified")
				assertAffectedReason(t, report, "director", "invoked agent coordinator affected")
			} else {
				assertAffectedReason(t, report, "caller", "invoked agent worker modified")
			}
		})
	}
}

func TestDiffPathsReportsIdenticalSuites(t *testing.T) {
	root := writeMinimalSuite(t)
	report, err := DiffPaths(root, root)
	if err != nil {
		t.Fatalf("DiffPaths() error = %v", err)
	}
	if report.HasChanges() || report.Summary != (DiffSummary{}) || len(report.AffectedAgents) != 0 {
		t.Fatalf("identical diff = %+v", report)
	}
}

func TestDiffPathsTreatsExtractedSuiteAndOCILayoutAsEquivalent(t *testing.T) {
	extracted := writeMinimalSuite(t)
	layout := writeTestOCILayout(t, extracted)
	report, err := DiffPaths(extracted, layout)
	if err != nil {
		t.Fatalf("DiffPaths() error = %v", err)
	}
	if report.HasChanges() || report.Summary != (DiffSummary{}) {
		t.Fatalf("cross-representation diff = %+v", report)
	}
}

func TestDiffPathsCanonicalizesOmittedDirectoriesAndHardlinks(t *testing.T) {
	extracted := writeMinimalSuite(t)
	original := "supplement/a-original.txt"
	hardlink := "supplement/z-hardlink.txt"
	data := []byte("shared supplemental content")
	mustWrite(t, extracted, original, data)
	mustWrite(t, extracted, hardlink, data)

	layout := writeTestOCILayoutTar(t, tarDirectoryWithHardlink(t, extracted, hardlink, original))
	report, err := DiffPaths(extracted, layout)
	if err != nil {
		t.Fatalf("DiffPaths() error = %v", err)
	}
	if report.HasChanges() || report.Before.ContentDigest != report.After.ContentDigest {
		t.Fatalf("cross-representation hardlink diff = %+v", report)
	}
}

func TestDiffPathsReportsSupplementalContentAcrossOCILayouts(t *testing.T) {
	before := writeMinimalSuite(t)
	after := writeMinimalSuite(t)
	for root, value := range map[string]string{
		before: "id: example\ninput: before\nexpectContains: [before]\n",
		after:  "id: example\ninput: after\nexpectContains: [after]\n",
	} {
		path := filepath.Join(root, "metadata", "kmx", "eval")
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "example.yaml"), []byte(value), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	report, err := DiffPaths(writeTestOCILayout(t, before), writeTestOCILayout(t, after))
	if err != nil {
		t.Fatalf("DiffPaths() error = %v", err)
	}
	if !hasRecordChange(report, DiffModified, "content", "metadata/kmx/eval/example.yaml") {
		t.Fatalf("supplemental content change is absent: %+v", report.Changes)
	}
	if report.Before.ArtifactDigest == report.After.ArtifactDigest ||
		report.Before.ContentDigest == report.After.ContentDigest {
		t.Fatalf("artifact identities did not change: %+v -> %+v", report.Before, report.After)
	}
}

func TestDiffPathsReportsSupplementalMetadataAlongsideOtherChanges(t *testing.T) {
	before := writeMinimalSuite(t)
	after := writeMinimalSuite(t)
	for _, root := range []string{before, after} {
		if err := os.WriteFile(filepath.Join(root, "same.txt"), []byte("same"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "other.txt"), []byte("before"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(filepath.Join(after, "same.txt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(after, "other.txt"), []byte("after!"), 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := DiffPaths(before, after)
	if err != nil {
		t.Fatalf("DiffPaths() error = %v", err)
	}
	assertDiffField(t, report, "content", "same.txt", "mode", "420", "493")
	if !hasRecordChange(report, DiffModified, "content", "other.txt") {
		t.Fatalf("other supplemental content change is absent: %+v", report.Changes)
	}
}

func TestDiffPathsNormalizesSupplementalContentNumbers(t *testing.T) {
	before := writeMinimalSuite(t)
	after := writeMinimalSuite(t)
	if err := os.WriteFile(filepath.Join(before, "supplement.txt"), []byte("before"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(after, "supplement.txt"), []byte("after!"), 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := DiffPaths(before, after)
	if err != nil {
		t.Fatalf("DiffPaths() error = %v", err)
	}
	for _, change := range report.Changes {
		if change.Kind == "content" && change.ID == "supplement.txt" {
			if len(change.Fields) != 1 || change.Fields[0].Path != "digest" {
				t.Fatalf("supplement fields = %+v, want only digest", change.Fields)
			}
			return
		}
	}
	t.Fatalf("supplemental content change is absent: %+v", report.Changes)
}

func TestDiffPathsReportsOCIManifestOnlyChanges(t *testing.T) {
	content := writeMinimalSuite(t)
	before := writeTestOCILayout(t, content, map[string]string{"example": "before"})
	after := writeTestOCILayout(t, content, map[string]string{"example": "after"})

	report, err := DiffPaths(before, after)
	if err != nil {
		t.Fatalf("DiffPaths() error = %v", err)
	}
	if len(report.Changes) != 1 || report.Changes[0].Kind != "artifact" {
		t.Fatalf("manifest-only changes = %+v, want one artifact change", report.Changes)
	}
	assertDiffField(t, report, "artifact", "content", "artifactDigest",
		fmt.Sprintf("%q", report.Before.ArtifactDigest), fmt.Sprintf("%q", report.After.ArtifactDigest))
	if report.Before.ContentDigest != report.After.ContentDigest {
		t.Fatalf("content identity changed: %s -> %s", report.Before.ContentDigest, report.After.ContentDigest)
	}
}

func TestDiffPathsTreatsInvocationOrderAsSemanticSet(t *testing.T) {
	before := copyAgentSuiteDirectory(t, filepath.Join("testdata", "coordinator-workers"))
	after := copyAgentSuiteDirectory(t, filepath.Join("testdata", "coordinator-workers"))
	var coordinator Agent
	readStrictJSON(t, filepath.Join(after, "agents", "coordinator.json"), &coordinator)
	slices.Reverse(coordinator.Invokes)
	coordinatorDigest := mustWriteJSON(t, after, "agents/coordinator.json", MediaTypeAgent, coordinator)
	var suite Suite
	readStrictJSON(t, filepath.Join(after, "agentsuite.json"), &suite)
	for index := range suite.Agents {
		if suite.Agents[index].ID == coordinator.ID {
			suite.Agents[index].Descriptor = coordinatorDigest
		}
	}
	mustWriteJSON(t, after, "agentsuite.json", MediaTypeSuite, suite)

	report, err := DiffPaths(before, after)
	if err != nil {
		t.Fatalf("DiffPaths() error = %v", err)
	}
	if hasRecordChange(report, DiffModified, "agent", "coordinator") {
		t.Fatalf("invocation order changed the agent semantics: %+v", report.Changes)
	}
	if len(report.AffectedAgents) != 0 {
		t.Fatalf("invocation order affected agents: %+v", report.AffectedAgents)
	}
	if len(report.Changes) != 1 || report.Changes[0].Kind != "artifact" {
		t.Fatalf("repacked artifact changes = %+v, want only artifact identity", report.Changes)
	}
}

func TestAffectedAgentsIgnoresStandaloneToolProviderBuildInputs(t *testing.T) {
	snapshot := diffSnapshot{
		compositions: map[string]Composition{
			"writer@linux/amd64": {
				Agent: "writer", Platform: Platform{OS: "linux", Architecture: "amd64"},
				BuildProfile:  "agent-build",
				ToolProviders: []ResolvedToolProvider{{ID: "search", Version: "1.0.0"}},
			},
		},
		agents: map[string]Agent{"writer": {ID: "writer"}},
		providerCompositions: map[string]ToolProviderComposition{
			"search@1.0.0@linux/amd64": {
				ID: "search", Version: "1.0.0",
				Platform:     Platform{OS: "linux", Architecture: "amd64"},
				BuildProfile: "provider-build",
			},
		},
	}
	changed := map[diffRecordKey]string{
		{kind: "toolProviderComposition", id: "search@1.0.0@linux/amd64"}: DiffModified,
		{kind: "buildProfile", id: "provider-build"}:                      DiffModified,
	}
	if affected := affectedAgents(snapshot, snapshot, changed); len(affected) != 0 {
		t.Fatalf("standalone ToolProvider build inputs affected agents: %+v", affected)
	}
}

func TestDiffReportJSONContractUsesVersionedArrays(t *testing.T) {
	report := DiffReport{
		SchemaVersion:  DiffReportSchemaVersion,
		Before:         DiffIdentity{Suite: "same", SourceType: "directory", ContentDigest: "sha256:before"},
		After:          DiffIdentity{Suite: "same", SourceType: "directory", ContentDigest: "sha256:after"},
		Changes:        []RecordChange{},
		AffectedAgents: []AffectedAgent{},
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	const want = `{
  "schemaVersion": "kmx.agentsuite.diff/v1",
  "before": {
    "suite": "same",
    "sourceType": "directory",
    "contentDigest": "sha256:before"
  },
  "after": {
    "suite": "same",
    "sourceType": "directory",
    "contentDigest": "sha256:after"
  },
  "summary": {
    "added": 0,
    "removed": 0,
    "modified": 0,
    "fieldsChanged": 0,
    "agentsAffected": 0
  },
  "changes": [],
  "affectedAgents": []
}`
	if string(encoded) != want {
		t.Fatalf("JSON contract:\n%s\nwant:\n%s", encoded, want)
	}
}

func TestDiffLimitsRefuseExcessiveFieldsAndReportBytes(t *testing.T) {
	atBoundary := make([]any, maxDiffFieldsPerRecord)
	for index := range atBoundary {
		atBoundary[index] = json.Number("1")
	}
	if _, err := diffFields("", nil, false, atBoundary, true); err != nil {
		t.Fatalf("field boundary rejected: %v", err)
	}
	overBoundary := append(atBoundary, json.Number("1"))
	_, err := diffFields("", nil, false, overBoundary, true)
	var limitErr *DiffLimitError
	if !errors.As(err, &limitErr) || limitErr.Resource != "field-per-record" {
		t.Fatalf("field limit error = %v", err)
	}

	report := DiffReport{
		SchemaVersion:  DiffReportSchemaVersion,
		Before:         DiffIdentity{Suite: strings.Repeat("x", maxDiffReportBytes)},
		Changes:        []RecordChange{},
		AffectedAgents: []AffectedAgent{},
	}
	err = validateDiffReportSize(report)
	if !errors.As(err, &limitErr) || limitErr.Resource != "serialized-report-byte" {
		t.Fatalf("report limit error = %v", err)
	}
}

func TestDiffFieldsLargeToolProviderEmitsOnlyActualChanges(t *testing.T) {
	files := largeDiffInventory(1_300)
	variant := map[string]any{
		"platform": map[string]any{"os": "linux", "architecture": "amd64"},
		"files":    files,
	}
	before := map[string]any{"revision": "v1", "variants": []any{variant}}
	after := map[string]any{"revision": "v2", "variants": []any{variant}}

	fields, err := diffFields("toolProvider", before, true, after, true)
	if err != nil {
		t.Fatalf("large ToolProvider diff error = %v", err)
	}
	if len(fields) != 1 || fields[0].Path != "revision" {
		t.Fatalf("large ToolProvider fields = %+v, want revision only", fields)
	}
}

func TestDiffFieldsLargeToolProviderAddRemoveAggregatesKeyedElements(t *testing.T) {
	provider := map[string]any{
		"revision": "v1",
		"variants": []any{map[string]any{
			"platform": map[string]any{"os": "linux", "architecture": "amd64"},
			"files":    largeDiffInventory(3_501),
		}},
	}
	for _, test := range []struct {
		name                        string
		before, after               any
		beforePresent, afterPresent bool
	}{
		{name: "added", after: provider, afterPresent: true},
		{name: "removed", before: provider, beforePresent: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fields, err := diffFields(
				"toolProvider",
				test.before, test.beforePresent,
				test.after, test.afterPresent,
			)
			if err != nil {
				t.Fatalf("large ToolProvider %s error = %v", test.name, err)
			}
			if len(fields) != 3_504 || fields[0].Path != "revision" {
				t.Fatalf("large ToolProvider %s field count = %d, want 3504", test.name, len(fields))
			}
			fileFields := 0
			for _, field := range fields {
				if strings.HasPrefix(field.Path, "variants[linux/amd64].files[") {
					fileFields++
					filePath := strings.TrimPrefix(field.Path, "variants[linux/amd64].files[")
					if strings.Contains(filePath, "].") {
						t.Fatalf("large ToolProvider %s expanded keyed file path %q", test.name, field.Path)
					}
				}
			}
			if fileFields != 3_501 {
				t.Fatalf("large ToolProvider %s keyed file fields = %d, want 3501", test.name, fileFields)
			}
		})
	}
}

func TestDiffFieldsAddedRemovedContentUsesAggregateField(t *testing.T) {
	content := map[string]any{
		"path": "bin/tool", "type": "file", "mode": json.Number("493"),
		"size": json.Number("1"), "digest": fmt.Sprintf("sha256:%064x", 1),
	}
	for _, test := range []struct {
		name                        string
		before, after               any
		beforePresent, afterPresent bool
	}{
		{name: "added", after: content, afterPresent: true},
		{name: "removed", before: content, beforePresent: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fields, err := diffFields(
				"content",
				test.before, test.beforePresent,
				test.after, test.afterPresent,
			)
			if err != nil {
				t.Fatalf("content %s error = %v", test.name, err)
			}
			if len(fields) != 1 || fields[0].Path != "$" {
				t.Fatalf("content %s fields = %+v, want one aggregate field", test.name, fields)
			}
		})
	}
}

func TestDiffPathsLargeToolProviderAddRemoveReturnsReport(t *testing.T) {
	withoutProvider := writeMinimalSuite(t)
	withProvider := writeMinimalSuite(t)
	addLargeDiffToolProvider(t, withProvider, 3_501)

	for _, test := range []struct {
		name          string
		before, after string
		changeType    string
	}{
		{name: "added", before: withoutProvider, after: withProvider, changeType: DiffAdded},
		{name: "removed", before: withProvider, after: withoutProvider, changeType: DiffRemoved},
	} {
		t.Run(test.name, func(t *testing.T) {
			report, err := DiffPaths(test.before, test.after)
			if err != nil {
				t.Fatalf("DiffPaths() error = %v", err)
			}
			if !hasRecordChange(report, test.changeType, "toolProvider", "large@1.0.0") {
				t.Fatalf("large ToolProvider %s change is absent", test.name)
			}
			for _, change := range report.Changes {
				if change.Kind == "content" && (len(change.Fields) != 1 || change.Fields[0].Path != "$") {
					t.Fatalf("content change %s fields = %+v, want one aggregate field", change.ID, change.Fields)
				}
			}
		})
	}
}

func TestDiffFieldsMatchesSetArraysByLogicalKey(t *testing.T) {
	file := func(path string) any {
		return map[string]any{"path": path, "type": "file", "mode": json.Number("493")}
	}
	variant := func(files ...any) any {
		return map[string]any{
			"platform": map[string]any{"os": "linux", "architecture": "amd64"},
			"files":    files,
		}
	}
	before := map[string]any{"variants": []any{variant(file("bin/b"), file("bin/c"))}}
	after := map[string]any{"variants": []any{variant(file("bin/a"), file("bin/b"), file("bin/c"))}}

	fields, err := diffFields("toolProvider", before, true, after, true)
	if err != nil {
		t.Fatalf("keyed ToolProvider diff error = %v", err)
	}
	if len(fields) != 1 {
		t.Fatalf("keyed ToolProvider fields = %+v, want one field for one file", fields)
	}
	if fields[0].Path != "variants[linux/amd64].files[bin/a]" {
		t.Fatalf("field path = %q, want keyed bin/a path", fields[0].Path)
	}
}

func largeDiffInventory(count int) []any {
	files := make([]any, count)
	for index := range files {
		files[index] = map[string]any{
			"path":      fmt.Sprintf("bin/tool-%04d", index),
			"type":      "file",
			"mode":      json.Number("493"),
			"uid":       json.Number("0"),
			"gid":       json.Number("0"),
			"size":      json.Number("1"),
			"digest":    fmt.Sprintf("sha256:%064x", index+1),
			"component": "large",
		}
	}
	return files
}

func addLargeDiffToolProvider(t *testing.T, root string, count int) {
	t.Helper()
	schema := []byte(`{"type":"object","additionalProperties":false}`)
	mustWrite(t, root, "schemas/large.json", schema)
	payloadRoot := "tool-providers/large/1.0.0/linux-amd64"
	files := make([]InventoryEntry, count)
	for index := range files {
		name := fmt.Sprintf("bin/tool-%04d", index)
		data := []byte{byte(index)}
		mustWriteMode(t, root, filepath.Join(payloadRoot, name), data, 0o755)
		files[index] = InventoryEntry{
			Path: name, Type: "file", Mode: 0o755,
			Size: int64(len(data)), Digest: digestBytes(data), Component: "large",
		}
	}
	variant := ToolProviderVariant{
		Platform:    Platform{OS: "linux", Architecture: "amd64"},
		InstallRoot: "/opt/agentsuite/tool-providers/large",
		PayloadRoot: payloadRoot,
		Entrypoint:  "/opt/agentsuite/tool-providers/large/bin/tool-0000",
		Runtime:     RuntimeRequirement{ABI: "static", CPUBaseline: "x86-64-v1"},
		Files:       files,
	}
	variant.VariantDigest = mustVariantDigest(t, variant)
	retained := true
	provider := ToolProvider{
		SchemaVersion: SpecVersion,
		MediaType:     MediaTypeToolProvider,
		ID:            "large",
		Version:       "1.0.0",
		Retained:      &retained,
		Protocol:      "mcp",
		Revision:      "2025-06-18",
		Tools: []Tool{{
			Name: "large", InputSchema: FileRef{
				Path: "schemas/large.json", Descriptor: Descriptor{
					MediaType: MediaTypeJSONSchema, Digest: digestBytes(schema), Size: int64(len(schema)),
				},
			},
		}},
		Variants:   []ToolProviderVariant{variant},
		Extensions: []Extension{},
	}
	providerPath := "tool-providers/large/1.0.0/tool-provider.json"
	providerDescriptor := mustWriteJSON(t, root, providerPath, MediaTypeToolProvider, provider)

	var catalog ToolProviderCatalog
	readStrictJSON(t, filepath.Join(root, "tool-providers/catalog.json"), &catalog)
	catalog.ToolProviders = append(catalog.ToolProviders, ManifestRef{
		ID: "large", Version: "1.0.0", Path: providerPath, Descriptor: providerDescriptor,
	})
	catalogDescriptor := mustWriteJSON(t, root, "tool-providers/catalog.json", MediaTypeToolProviderCatalog, catalog)

	var suite Suite
	readStrictJSON(t, filepath.Join(root, "agentsuite.json"), &suite)
	suite.ToolProviderCatalog.Descriptor = catalogDescriptor
	suite.Capabilities = []string{"bundled-stdio-mcp"}
	mustWriteJSON(t, root, "agentsuite.json", MediaTypeSuite, suite)
}

func tarDirectoryWithHardlink(t *testing.T, root, hardlinkPath, targetPath string) []byte {
	t.Helper()
	var names []string
	if err := filepath.WalkDir(root, func(name string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if name != root && !entry.IsDir() {
			names = append(names, name)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	slices.Sort(names)
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	for _, name := range names {
		relative, err := filepath.Rel(root, name)
		if err != nil {
			t.Fatal(err)
		}
		relative = filepath.ToSlash(relative)
		info, err := os.Lstat(name)
		if err != nil {
			t.Fatal(err)
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			t.Fatal(err)
		}
		header.Name = relative
		header.Uid = 0
		header.Gid = 0
		header.ModTime = time.Unix(0, 0)
		header.AccessTime = time.Time{}
		header.ChangeTime = time.Time{}
		if relative == hardlinkPath {
			header.Typeflag = tar.TypeLink
			header.Linkname = targetPath
			header.Size = 0
		}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if header.Typeflag == tar.TypeReg {
			data, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := writer.Write(data); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestDiffPathsLabelsInvalidSide(t *testing.T) {
	valid := writeMinimalSuite(t)
	invalid := t.TempDir()
	if err := os.WriteFile(filepath.Join(invalid, "agentsuite.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := DiffPaths(invalid, valid); err == nil || !strings.Contains(err.Error(), "before AgentSuite") {
		t.Fatalf("before error = %v", err)
	}
	if _, err := DiffPaths(valid, invalid); err == nil || !strings.Contains(err.Error(), "after AgentSuite") {
		t.Fatalf("after error = %v", err)
	}
}

func readStrictJSON(t *testing.T, name string, value any) {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := decodeStrict(data, value); err != nil {
		t.Fatal(err)
	}
}

func assertDiffField(t *testing.T, report DiffReport, kind, id, path string, before any, after string) {
	t.Helper()
	for _, change := range report.Changes {
		if change.Kind != kind || change.ID != id {
			continue
		}
		for _, field := range change.Fields {
			if field.Path != path {
				continue
			}
			if before == nil {
				if field.Before != nil {
					t.Fatalf("%s/%s %s before = %s, want absent", kind, id, path, field.Before)
				}
			} else if string(field.Before) != before {
				t.Fatalf("%s/%s %s before = %s, want %s", kind, id, path, field.Before, before)
			}
			if string(field.After) != after {
				t.Fatalf("%s/%s %s after = %s, want %s", kind, id, path, field.After, after)
			}
			return
		}
	}
	t.Fatalf("missing field change %s/%s %s", kind, id, path)
}

func hasRecordChange(report DiffReport, changeType, kind, id string) bool {
	for _, change := range report.Changes {
		if change.Type == changeType && change.Kind == kind && change.ID == id {
			return true
		}
	}
	return false
}

func assertAffectedAgentReason(t *testing.T, report DiffReport, agent, reason string) {
	t.Helper()
	assertAffectedReason(t, report.AffectedAgents, agent, reason)
}

func assertAffectedReason(t *testing.T, affected []AffectedAgent, agent, reason string) {
	t.Helper()
	for _, item := range affected {
		if item.ID == agent {
			if !slices.Contains(item.Reasons, reason) {
				t.Fatalf("affected agent %s reasons = %v, want %q", agent, item.Reasons, reason)
			}
			return
		}
	}
	t.Fatalf("affected agents = %+v, want %s", affected, agent)
}

func copyAgentSuiteDirectory(t *testing.T, source string) string {
	t.Helper()
	destination := t.TempDir()
	if err := os.CopyFS(destination, os.DirFS(source)); err != nil {
		t.Fatal(err)
	}
	return destination
}
