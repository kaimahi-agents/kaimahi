package blueprint_test

// MILESTONE 1's acceptance test, and it is not "it works".
//
// The claim is EQUIVALENCE: the release workflow's governance, expressed
// as a blueprint, produces the same three artifacts as the make targets it
// replaces —
//
//	make release-allow   the credential's tool allowlist
//	make release-bind    the standing constraints, as an overlay fragment
//
// and it proves that by RUNNING THOSE TARGETS, not by restating what they
// were believed to do. `scripts/release-bind.sh` is executed unmodified
// against a stub kubectl that serves the committed table and captures the
// patch, and the allowlist reference is read out of `make -n`, which is
// the same expansion a developer's invocation gets (the delegation
// package established that technique). A change to either target that
// this blueprint does not follow fails here.
//
// What Normalize takes out of the comparison is key ORDER and whitespace,
// and nothing else — never a value, never a JSON type, never a
// present-versus-absent key. The plane parses JSON, so key order is not
// something either side gets to be right or wrong about; everything that
// decides an admission is compared exactly.

import (
	"encoding/json"
	"os"
	"os/exec"
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

func TestTheBlueprintProducesTheSameAllowlistAsMakeReleaseAllow(t *testing.T) {
	want := allowlistFromMake(t)
	got := renderRelease(t, map[string]string{"repo": "Contoso/widget"}).Allowlist

	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("the blueprint's allowlist is not the one `make release-allow` sets.\n"+
			" make:      %s\n blueprint: %s", strings.Join(want, ", "), strings.Join(got, ", "))
	}
	// strings.Split never returns a zero-length slice, so "len == 0" was
	// a guard that could not fire: an EMPTY allowlist on both sides would
	// have passed this test while proving nothing. Check the content.
	if len(want) < 5 || want[0] == "" {
		t.Fatalf("read no usable allowlist out of the Makefile (%v); the test is not proving anything", want)
	}
	for _, tool := range want {
		if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`).MatchString(tool) {
			t.Fatalf("%q is not a tool name; the Makefile was parsed wrongly", tool)
		}
	}
}

// TestTheBlueprintProducesTheSameConstraintsAsMakeReleaseBind runs the
// real script, in both the shapes an operator uses it in.
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
			want := runReleaseBind(t, tc.env)
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
				t.Fatalf("release-bind.sh wrote something that is not JSON: %v\n%s", err, want)
			}
			if string(gotJSON) != string(wantJSON) {
				t.Fatalf("the blueprint's standing constraints are not the ones release-bind.sh writes.\n"+
					"--- scripts/release-bind.sh\n%s\n--- blueprints/release.yaml\n%s", wantJSON, gotJSON)
			}
			// The comparison is worthless if either side is empty.
			var doc struct {
				StandingConstraints map[string]map[string]json.RawMessage `json:"standing_constraints"`
			}
			if err := json.Unmarshal(wantJSON, &doc); err != nil {
				t.Fatal(err)
			}
			if len(doc.StandingConstraints["release-agent"]) == 0 {
				t.Fatal("release-bind.sh produced no constraints; the comparison proves nothing")
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

// allowlistFromMake asks make what `release-allow` would run, and reads
// the tool list out of the command line — the same expansion a developer
// gets, which is how internal/kmx/delegation proves its claims too.
func allowlistFromMake(t *testing.T) []string {
	t.Helper()
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make is not installed")
	}
	cmd := exec.Command("make", "-n", "release-allow")
	cmd.Dir = repoRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make -n release-allow: %v\n%s", err, out)
	}
	re := regexp.MustCompile(`tool-allow\s+\S+\s+"([^"]*)"`)
	m := re.FindStringSubmatch(string(out))
	if m == nil {
		t.Fatalf("could not read the allowlist out of:\n%s", out)
	}
	tools := strings.Split(m[1], ",")
	for i := range tools {
		tools[i] = strings.TrimSpace(tools[i])
	}
	sortStrings(tools)
	return tools
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// runReleaseBind executes scripts/release-bind.sh unmodified against a
// stub cluster and returns the fragment it patched into the overlay.
//
// A stub rather than a re-implementation, because a re-implementation is
// a second opinion about what the script does and this test exists to
// have exactly one. The stub answers the four things the script asks a
// cluster: which context it is on (a kind-shaped one, so kube-guard.sh
// proceeds), the committed upstream table, whether the overlay ConfigMap
// exists, and a rollout that succeeds.
func runReleaseBind(t *testing.T, env map[string]string) []byte {
	t.Helper()
	for _, bin := range []string{"bash", "python3"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s is not installed", bin)
		}
	}
	dir := t.TempDir()
	table, err := committedTable(t)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "upstreams.json"), table, 0o600); err != nil {
		t.Fatal(err)
	}
	stub := `#!/usr/bin/env bash
# A stub kubectl for the equivalence test. It answers only what
# scripts/release-bind.sh asks, and fails loudly on anything else so that
# a script that started doing something new cannot pass silently.
set -euo pipefail
args="$*"
case "$args" in
  "config view -o json")
    printf '%s' '{"contexts":[{"name":"kind-equivalence","context":{"cluster":"kind-equivalence"}}],"clusters":[{"name":"kind-equivalence","cluster":{"server":"https://127.0.0.1:6443"}}]}' ;;
  "config view --minify -o jsonpath="*)
    printf 'kind-equivalence' ;;
  *"get configmap kaimahi-upstreams"*)
    cat "$STUB_DIR/upstreams.json" ;;
  *"get configmap kaimahi-upstreams-extra"*)
    printf 'configmap/kaimahi-upstreams-extra' ;;
  *"patch configmap kaimahi-upstreams-extra"*)
    for a in "$@"; do
      case "$a" in --patch-file=*) cp "${a#--patch-file=}" "$STUB_DIR/patch.json" ;; esac
    done
    prev=""
    for a in "$@"; do
      if [ "$prev" = "--patch-file" ]; then cp "$a" "$STUB_DIR/patch.json"; fi
      prev="$a"
    done ;;
  *"rollout restart"*|*"rollout status"*) : ;;
  *) echo "stub kubectl: unexpected call: $args" >&2; exit 64 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(stub), 0o700); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("bash", "scripts/release-bind.sh")
	cmd.Dir = repoRoot
	// AN ALLOWLIST, not a denylist, and that is the whole point of it.
	//
	// This test's claim is that it runs scripts/release-bind.sh
	// UNMODIFIED, so what the script sees has to be what this test set
	// and nothing else. Two ways the caller's own environment could
	// change the answer: the script reads its inputs from it (an
	// exported ADO_PIPELINES would silently turn the "one repository"
	// case into the bounded-builds one), and it runs `bash` and
	// `python3 -`, both of which take instructions from their
	// environment — BASH_ENV is SOURCED before a non-interactive script,
	// PYTHONPATH can put a different `json` module in front of the real
	// one, PYTHONHOME can point the interpreter somewhere else entirely.
	//
	// Listing those to exclude would be a list nobody ever finishes.
	// Naming what the script may KEEP is finite, and a name missing from
	// it fails loudly here rather than quietly somewhere else.
	keep := map[string]bool{
		"HOME": true, "TMPDIR": true, "LANG": true, "LC_ALL": true, "LC_CTYPE": true,
	}
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); keep[name] {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env,
		"PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"STUB_DIR="+dir,
		"KUBECTL=kubectl",
	)
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("scripts/release-bind.sh: %v\n%s", err, out)
	}
	patch, err := os.ReadFile(filepath.Join(dir, "patch.json"))
	if err != nil {
		t.Fatalf("release-bind.sh patched nothing: %v", err)
	}
	var p struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(patch, &p); err != nil {
		t.Fatal(err)
	}
	fragment, ok := p.Data["release-bind.json"]
	if !ok {
		t.Fatalf("release-bind.sh did not write release-bind.json; it wrote %v", p.Data)
	}
	return []byte(fragment)
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
