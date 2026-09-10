package blueprint_test

// MILESTONE 1's acceptance test, and it is not "it works".
//
// The release workflow's governance is authoritative here: these tests pin
// its allowlist and standing constraints directly, without consulting a
// superseded shell or Make implementation.
//
// What Normalize takes out of the comparison is key ORDER and whitespace,
// and nothing else — never a value, never a JSON type, never a
// present-versus-absent key. The plane parses JSON, so key order is not
// something either side gets to be right or wrong about; everything that
// decides an admission is compared exactly.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	kaimahi "github.com/kaimahi-agents/kaimahi"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/blueprint"
)

// repoRoot is this package's path back to the tree the make targets and
// scripts live in.
const repoRoot = "../../.."

func TestTheReleaseBlueprintHasTheReadOnlyAllowlist(t *testing.T) {
	want := []string{"actions_get", "actions_list", "core_list_projects", "get_latest_release", "get_release_by_tag", "list_commits", "list_pull_requests", "list_releases", "list_tags", "pipelines_build", "pipelines_build_log", "pipelines_definition"}
	got := renderRelease(t, map[string]string{"repo": "Contoso/widget"}).Allowlist

	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("release blueprint allowlist = [%s], want [%s]", strings.Join(got, ", "), strings.Join(want, ", "))
	}
	// strings.Split never returns a zero-length slice, so "len == 0" was
	// a guard that could not fire: an EMPTY allowlist on both sides would
	// have passed this test while proving nothing. Check the content.
	if len(want) < 5 || want[0] == "" {
		t.Fatalf("the pinned release allowlist is unusable (%v); the test is not proving anything", want)
	}
	for _, tool := range want {
		if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`).MatchString(tool) {
			t.Fatalf("%q is not a valid tool name", tool)
		}
	}
}

// TestTheBlueprintProducesTheSameConstraintsAsMakeReleaseBind compares the
// blueprint with the old binding's frozen output in both supported shapes.
func TestTheBlueprintProducesTheSameConstraintsAsMakeReleaseBind(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
		set  map[string]string
	}{
		{
			name: "one repository",
			env:  map[string]string{"GITHUB_REPO": "Contoso/widget"},
			set:  map[string]string{"repo": "Contoso/widget"},
		},
		{
			// docs/release-agent.md: "builds are bounded, not approved".
			name: "one repository, and the builds bounded",
			env: map[string]string{
				"GITHUB_REPO": "Contoso/widget", "ADO_ORG": "contoso",
				"ADO_PROJECT": "widget-ci", "ADO_PIPELINES": "41,42",
			},
			set: map[string]string{
				"repo": "Contoso/widget", "ado_org": "contoso",
				"ado_project": "widget-ci", "ado_pipelines": "41,42",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := expectedReleaseConstraints(t, tc.env)
			fragment, err := renderRelease(t, tc.set).Fragment()
			if err != nil {
				t.Fatal(err)
			}
			gotJSON, err := blueprint.Normalize([]byte(fragment))
			if err != nil {
				t.Fatal(err)
			}
			wantJSON, err := blueprint.Normalize(want)
			if err != nil {
				t.Fatalf("frozen release constraints are not JSON: %v\n%s", err, want)
			}
			if string(gotJSON) != string(wantJSON) {
				t.Fatalf("the blueprint's standing constraints changed from the frozen release binding.\n"+
					"--- frozen expected binding\n%s\n--- blueprints/release.yaml\n%s", wantJSON, gotJSON)
			}
			// The comparison is worthless if either side is empty.
			var doc struct {
				StandingConstraints map[string]map[string]json.RawMessage `json:"standing_constraints"`
			}
			if err := json.Unmarshal(wantJSON, &doc); err != nil {
				t.Fatal(err)
			}
			if len(doc.StandingConstraints["release-agent"]) == 0 {
				t.Fatal("the frozen binding has no constraints; the comparison proves nothing")
			}
		})
	}
}

// TestTheBlueprintDeclaresThePolicyFieldsTheCommittedTableDeclares is the
// half of the governance a blueprint may NOT write and can only assert:
// `github-release` and `ado` are hosted, keyed seams, and
// plane/internal/config/overlay.go refuses an overlay entry that sets
// their custody fields. So the blueprint states what it depends on, and
// this proves the statement is true of what ships.
func TestTheBlueprintDeclaresThePolicyFieldsTheCommittedTableDeclares(t *testing.T) {
	declared := committedPolicyFields(t)
	b := loadRelease(t)
	for _, seam := range []string{"github-release", "ado"} {
		s, ok := b.Seams[seam]
		if !ok {
			t.Fatalf("the release blueprint no longer names the seam %q", seam)
		}
		for tool, fields := range s.Requires {
			got, ok := declared[tool]
			if !ok {
				// release_publish is the driver's own action and no
				// server offers it — but the committed table DOES
				// declare it, deliberately, so its approval is bound and
				// legible like every tool call's.
				t.Fatalf("blueprint requires policy fields for %s, and the committed table declares none", tool)
			}
			if strings.Join(got, ",") != strings.Join(fields, ",") {
				t.Fatalf("%s: the blueprint requires [%s] and k8s/plane/upstreams.yaml declares [%s]. "+
					"A blueprint asserts what it depends on; one of the two is wrong",
					tool, strings.Join(fields, ", "), strings.Join(got, ", "))
			}
		}
	}
}

// TestTheConstraintVocabularyMatchesThePlanes keeps the mirrored operator
// list honest. kmx cannot import the plane (a separate Go module),
// so the list is copied — and a copy that drifts would let a blueprint
// emit a constraint the plane refuses at boot, taking a rollout down.
func TestTheConstraintVocabularyMatchesThePlanes(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot, "plane/internal/config/policy.go"))
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`(?m)^\tOp[A-Za-z]+\s*=\s*"([a-z_]+)"`)
	var want []string
	for _, m := range re.FindAllStringSubmatch(string(raw), -1) {
		want = append(want, m[1])
	}
	if len(want) == 0 {
		t.Fatal("read no operators out of plane/internal/config/policy.go")
	}
	if strings.Join(want, ",") != strings.Join(blueprint.ConstraintOps, ",") {
		t.Fatalf("the plane's constraint operators are [%s]; kmx mirrors [%s]",
			strings.Join(want, ", "), strings.Join(blueprint.ConstraintOps, ", "))
	}
}

// --- helpers ---------------------------------------------------------

func loadRelease(t *testing.T) *blueprint.Bundle {
	t.Helper()
	b, err := blueprint.Load(kaimahi.Blueprints, "release")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func renderRelease(t *testing.T, set map[string]string) *blueprint.Rendered {
	t.Helper()
	b := loadRelease(t)
	v, err := b.Bind(set, nil)
	if err != nil {
		t.Fatal(err)
	}
	r, err := b.Render(v, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func expectedReleaseConstraints(t *testing.T, env map[string]string) []byte {
	t.Helper()
	owner, repo, ok := strings.Cut(env["GITHUB_REPO"], "/")
	if !ok {
		t.Fatal("fixture repository is not owner/name")
	}
	tools := []string{"list_pull_requests", "list_commits", "list_tags", "list_releases", "get_latest_release", "get_release_by_tag", "actions_list", "actions_get"}
	constraints := map[string][]map[string]any{}
	for _, tool := range tools {
		constraints[tool] = []map[string]any{{"field": "owner", "op": "eq", "value": owner}, {"field": "repo", "op": "eq", "value": repo}}
	}
	if env["ADO_PIPELINES"] != "" {
		constraints["pipelines_write"] = []map[string]any{
			{"field": "action", "op": "eq", "value": "run_pipeline"},
			{"field": "orgName", "op": "eq", "value": env["ADO_ORG"]},
			{"field": "project", "op": "eq", "value": env["ADO_PROJECT"]},
			{"field": "pipelineId", "op": "in", "values": []int{41, 42}},
		}
	}
	out, err := json.Marshal(map[string]any{"standing_constraints": map[string]any{"release-agent": constraints}})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// committedTable extracts the upstreams.json literal block out of the
// committed ConfigMap. The plane's own test does the same thing for the
// same reason: the root module has a YAML parser now, but reading the
// literal block keeps this test comparing the exact bytes the plane
// would boot with.
func committedTable(t *testing.T) ([]byte, error) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot, "k8s/plane/upstreams.yaml"))
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(raw), "\n")
	var out []string
	in := false
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "upstreams.json: |") {
			in = true
			continue
		}
		if !in {
			continue
		}
		if strings.TrimSpace(line) != "" && !strings.HasPrefix(line, "    ") {
			break
		}
		out = append(out, strings.TrimPrefix(line, "    "))
	}
	joined := strings.Join(out, "\n")
	if !json.Valid([]byte(joined)) {
		t.Fatalf("could not extract upstreams.json from k8s/plane/upstreams.yaml")
	}
	return []byte(joined), nil
}

func committedPolicyFields(t *testing.T) map[string][]string {
	t.Helper()
	table, err := committedTable(t)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		ToolUpstreams map[string]struct {
			Tools map[string]struct {
				PolicyFields []string `json:"policy_fields"`
			} `json:"tools"`
		} `json:"tool_upstreams"`
	}
	if err := json.Unmarshal(table, &doc); err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	for _, up := range doc.ToolUpstreams {
		for tool, policy := range up.Tools {
			out[tool] = policy.PolicyFields
		}
	}
	return out
}
