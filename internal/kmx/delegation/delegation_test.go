// Package delegation holds the test that make and kmx are ONE
// implementation of the journey: every delegating Makefile target is a thin
// alias that calls `kmx`, so the CI job that runs `make` is exercising the
// same code a developer runs, and neither can drift into being the real one.
//
// The claim is not "the Makefile mentions kmx somewhere". It is that each
// delegating target hands kmx the right work with the right arguments — and
// the only way to know that is to ask make itself, with the same variable
// expansion a developer's invocation gets. `make -n` runs nothing, so this
// stays a unit test: no cluster, no network, no Go toolchain beyond the one
// already running it.
package delegation

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
)

func dryRun(t *testing.T, args ...string) string {
	t.Helper()
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make is not installed")
	}
	cmd := exec.Command("make", append([]string{"-n"}, args...)...)
	cmd.Dir = "../../.."
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make -n %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

type delegationCase struct {
	target string
	// vars are make variables the recipe expands — set where the
	// delegation only becomes meaningful with an operator's argument
	// in it (a preset, a request id, a file). An `$(if ...)` that
	// collapses the wrong way produces a flag with no value, and that
	// failure has to land here rather than on an operator.
	vars []string
	want string
}

// Every target the Makefile delegates, and the kmx command it must produce.
// TestTheTableCoversEveryDelegatingTarget below holds this list to the
// Makefile, so a target added or removed there cannot pass unnoticed.
func delegationCases() []delegationCase {
	return []delegationCase{
		{target: "up", want: "bin/kmx up"},
		{target: "cluster", want: "bin/kmx up --step cluster"},
		{target: "ollama", want: "bin/kmx up --step ollama"},
		{target: "model", want: "bin/kmx up --step model"},
		{target: "kagent", want: "bin/kmx up --step kagent"},
		{target: "agent", want: "bin/kmx up --step agent"},
		{target: "tools-agent", want: "bin/kmx up --step tools-agent"},
		{target: "status", want: "bin/kmx status"},
		{target: "down", want: "bin/kmx down"},
		{target: "chat", want: `bin/kmx agent chat "$KMX_CHAT_AGENT" "$KMX_CHAT_TASK"`},
		// The governance half: the plane, and an agent governed onto it.
		{target: "plane", want: "bin/kmx plane --source ."},
		{target: "plane-image", want: "bin/kmx plane --step image --source ."},
		{target: "plane-secrets", want: "bin/kmx plane --step secrets"},
		{target: "govern", want: "bin/kmx govern hello-world --agent hello-world --preset governed-ollama"},
		{target: "ledger", want: "bin/kmx ledger hello-world"},
		{target: "grants", want: "bin/kmx grants"},
		{target: "tool-audit", want: "bin/kmx audit tool hello-tools"},
		{target: "approval-audit", want: "bin/kmx audit approval"},
		// The operator verbs. The exact argument
		// STRING is the contract, because these are what the delegating
		// recipe hands kmx after make's own expansion — an `$(if ...)`
		// that collapses the wrong way is a flag with no value, and the
		// failure lands on an operator, not here.
		{target: "use", want: "bin/kmx use"},
		{target: "use", vars: []string{"PRESET=anthropic"}, want: "bin/kmx use anthropic"},
		{target: "use-ollama", want: "bin/kmx use ollama"},
		{target: "budget", want: `bin/kmx budget hello-world --cents "-" --tokens "-"`},
		{target: "budget", vars: []string{"CAP_TOKENS=1"},
			want: `bin/kmx budget hello-world --cents "-" --tokens "1"`},
		{target: "approvals", want: "bin/kmx approvals"},
		{target: "approve", vars: []string{"ID=abc", "TTL=10m", "USES=1"},
			want: `bin/kmx approve "abc" --ttl "10m" --uses "1" --amount "-"`},
		{target: "deny", vars: []string{"ID=abc"}, want: `bin/kmx deny "abc"`},
		{target: "request", vars: []string{"KIND=tool", "SUBJECT=k8s_get_events"},
			want: `bin/kmx request tool k8s_get_events --credential "hello-tools"`},
		// A tool request names the CALL it is about, and the quoting
		// has to survive make, the shell and kmx's flag parsing intact.
		{target: "request", vars: []string{"KIND=tool", "SUBJECT=k8s_get_events", `ARGS={"namespace": "default"}`},
			want: `--args '{"namespace": "default"}'`},
		{target: "request", vars: []string{"KIND=budget", "SUBJECT=tokens"},
			want: `bin/kmx request budget tokens --credential "hello-world"`},
		{target: "govern-tools", want: `bin/kmx tools govern --credential hello-tools --tools "k8s_get_resources"`},
		{target: "ungovern-tools", want: "bin/kmx tools ungovern"},
		{target: "tool-allow", want: `bin/kmx tools allow "k8s_get_resources" --credential hello-tools`},
		{target: "tool-allowlist", want: "bin/kmx tools allowlist hello-tools"},
		{target: "backup", want: "bin/kmx backup"},
		{target: "backup", vars: []string{"FILE=ci-backup.sql"}, want: "bin/kmx backup ci-backup.sql"},
		{target: "restore", vars: []string{"FILE=ci-backup.sql"}, want: "bin/kmx restore ci-backup.sql"},
		{target: "plane-metrics", want: "bin/kmx metrics"},
		{target: "plane-metrics", vars: []string{"POD=kaimahi-proxy-1"}, want: "bin/kmx metrics --pod kaimahi-proxy-1"},
		// Credentials that expire: the view, and the one verb that moves a
		// deadline. NAME is required by the recipe, so it is set here; TTL
		// left out must collapse to the "unchanged" placeholder rather than
		// to a flag with no value.
		{target: "credentials", want: "bin/kmx credentials"},
		{target: "credential-renew", vars: []string{"NAME=hello-world", "TTL=1h"},
			want: `bin/kmx credential renew hello-world --ttl "1h"`},
		{target: "credential-renew", vars: []string{"NAME=hello-world"},
			want: `bin/kmx credential renew hello-world --ttl "-"`},
		// Capturing the credential an upstream needs. The repository or
		// organization the credential is scoped to has to reach kmx, or the
		// prompt would capture a token nobody bounded. The values below are
		// obviously-fake placeholders, not real accounts.
		{target: "github-secret", vars: []string{"GITHUB_REPO=owner/repo"},
			want: "bin/kmx credential capture github owner/repo"},
		{target: "release-secret", vars: []string{"GITHUB_REPO=owner/repo"},
			want: "bin/kmx credential capture github-release owner/repo"},
		{target: "ado-secret", vars: []string{"ADO_ORG=example-org"},
			want: "bin/kmx credential capture ado example-org"},
	}
}

func TestMakeTargetsDelegateToKmx(t *testing.T) {
	for _, tc := range delegationCases() {
		t.Run(tc.target, func(t *testing.T) {
			out := dryRun(t, append([]string{tc.target}, tc.vars...)...)
			if !strings.Contains(out, tc.want) {
				t.Errorf("`make %s` does not invoke `%s`:\n%s", tc.target, tc.want, out)
			}
		})
	}
}

// $(KMX) in COMMAND position in an unexpanded recipe line: at the start of the
// line (after make's `@`, `-` and `+` prefixes and any environment prefix such
// as `$(KMX_ENV)` or `FOO=bar`), or after a shell operator. Command position
// is the point — `build` echoes `$(abspath $(KMX))` and the link rule writes
// `go build -o $(KMX)`, and neither of those runs the journey, so a plain
// "mentions $(KMX)" would count targets no delegation can be written for.
var kmxInvocation = regexp.MustCompile(
	`(?m)^\s*[-@+]*\s*(?:[A-Za-z_][A-Za-z0-9_]*=\S*\s+|\$\([A-Za-z_]+\)\s+)*\$\(KMX\)\s+[^)\s]`)

// The table above is a claim about the Makefile, and a claim nobody checks
// goes stale: `credentials` and `credential-renew` were delegating targets
// for weeks while a comment here said this covered every one of them. So the
// floor comes from make's own database — every kind-path target whose recipe
// invokes kmx — and the table must cover it exactly. A target that stops
// delegating, and one that starts without anyone adding a case, both land
// here.
func TestTheTableCoversEveryDelegatingTarget(t *testing.T) {
	covered := map[string]bool{}
	for _, tc := range delegationCases() {
		covered[tc.target] = true
	}

	delegating := map[string]bool{}
	for target, recipe := range recipes(t, "TARGET=kind") {
		if kmxInvocation.MatchString(recipe) {
			delegating[target] = true
		}
	}
	// An empty derivation is a broken parse, not a Makefile with nothing in
	// it — and it would make every comparison below vacuous.
	if len(delegating) == 0 {
		t.Fatalf("no Makefile target invokes kmx at all: the derivation is broken, not the tree")
	}

	for target := range delegating {
		if !covered[target] {
			t.Errorf("`make %s` delegates to kmx but no case here says what it must invoke", target)
		}
	}
	for target := range covered {
		if !delegating[target] {
			t.Errorf("this test claims `make %s` delegates to kmx, but its recipe no longer calls it", target)
		}
	}
}

func TestBareMakeOnlyBuildsKmx(t *testing.T) {
	cmd := exec.Command("make", "-n")
	cmd.Dir = "../../.."
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bare make dry-run failed: %v\n%s", err, out)
	}
	text := string(out)
	if !strings.Contains(text, "kmx ready:") {
		t.Fatalf("bare make did not report the kmx path:\n%s", text)
	}
	for _, forbidden := range []string{"bin/kmx up", "kind create", "kubectl ", "helm "} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("bare make would perform runtime work (%q):\n%s", forbidden, text)
		}
	}
}

// The delegation has to carry the operator's settings, or `KIND_CLUSTER=mine
// make up` would build one cluster and `KIND_CLUSTER=mine kmx up` another.
func TestDelegationPassesTheOperatorsSettings(t *testing.T) {
	out := dryRun(t, "up", "KIND_CLUSTER=mine", "CONTAINER_ENGINE=podman", "MODEL=llama3", "CHAT_PORT=9999")
	for _, want := range []string{
		`KIND_CLUSTER="$KMX_KIND_CLUSTER"`,
		`KUBE_CTX="$KMX_KUBE_CTX"`,
		`CONTAINER_ENGINE="$KMX_CONTAINER_ENGINE"`,
		`MODEL="$KMX_MODEL"`,
		`CHAT_PORT="$KMX_CHAT_PORT"`,
		`KAGENT_VERSION="$KMX_KAGENT_VERSION"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("delegation does not pass %s:\n%s", want, out)
		}
	}
	cmd := exec.Command("make", "-pn", "KIND_CLUSTER=mine", "CONTAINER_ENGINE=podman", "MODEL=llama3", "CHAT_PORT=9999")
	cmd.Dir = "../../.."
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make database failed: %v\n%s", err, raw)
	}
	db := string(raw)
	for _, want := range []string{"KMX_KIND_CLUSTER := mine", "KMX_KUBE_CTX := kind-mine", "KMX_CONTAINER_ENGINE := podman", "KMX_MODEL := llama3", "KMX_CHAT_PORT := 9999"} {
		if !strings.Contains(db, want) {
			t.Errorf("make did not resolve %s", want)
		}
	}
}

func TestChatPortIsPassedOnlyWhenExplicit(t *testing.T) {
	if out := dryRun(t, "chat"); strings.Contains(out, "CHAT_PORT=") {
		t.Fatalf("implicit chat port was passed to kmx:\n%s", out)
	}
	if out := dryRun(t, "chat", "CHAT_PORT=8183"); !strings.Contains(out, `CHAT_PORT="$KMX_CHAT_PORT"`) {
		t.Fatalf("explicit chat port was not passed to kmx:\n%s", out)
	}
}

func TestChatInputsAreNotInterpolatedIntoShellSource(t *testing.T) {
	out := dryRun(t, "chat", "AGENT=hello-world; touch /tmp/agent-pwn", `TASK="; touch /tmp/task-pwn; :`)
	for _, injected := range []string{"; touch /tmp/agent-pwn", "; touch /tmp/task-pwn"} {
		if strings.Contains(out, injected) {
			t.Fatalf("chat input was interpolated into shell source (%q):\n%s", injected, out)
		}
	}
	for _, want := range []string{`"$KMX_CHAT_AGENT"`, `"$KMX_CHAT_TASK"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("chat does not read %s from the environment:\n%s", want, out)
		}
	}
}

func TestExportedContextFollowsTargetDefaults(t *testing.T) {
	cmd := exec.Command("make", "-pn", "TARGET=aks", "AKS_CLUSTER=aks-demo")
	cmd.Dir = "../../.."
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make database failed: %v\n%s", err, out)
	}
	if !regexp.MustCompile(`(?m)^KMX_KUBE_CTX := aks-demo$`).Match(out) {
		t.Fatalf("AKS context was exported before target defaults resolved:\n%s", out)
	}
}

func TestStatusPassesOutputFormat(t *testing.T) {
	out := dryRun(t, "status", "STATUS_OUTPUT=yaml")
	if !strings.Contains(out, `bin/kmx status -o "$KMX_STATUS_OUTPUT"`) {
		t.Fatalf("status output format was not delegated:\n%s", out)
	}
}

func TestStatusOutputIsNotInterpolatedIntoShellSource(t *testing.T) {
	out := dryRun(t, "status", "STATUS_OUTPUT=yaml && touch /tmp/pwn")
	if strings.Contains(out, "&& touch /tmp/pwn") {
		t.Fatalf("status output was interpolated into shell source:\n%s", out)
	}
	if !strings.Contains(out, `-o "$KMX_STATUS_OUTPUT"`) {
		t.Fatalf("status does not read the format from the exported environment:\n%s", out)
	}
}

// A confirmation given to make must not be asked for again by kmx.
func TestConfirmationRidesThrough(t *testing.T) {
	out := dryRun(t, "down", "KIND_CLUSTER=mine", "KAIMAHI_CONFIRM=kind-mine")
	if !strings.Contains(out, `KAIMAHI_CONFIRM="$KMX_CONFIRM"`) {
		t.Errorf("KAIMAHI_CONFIRM is not passed to kmx:\n%s", out)
	}
}

// By default kmx owns the verified kagent download. An explicit override is
// passed through, but make must not use curl before kmx can run its checks.
func TestChatLetsKmxAcquireKagentUnlessOverridden(t *testing.T) {
	out := dryRun(t, "chat")
	if strings.Contains(out, "curl ") || strings.Contains(out, "KAGENT=") {
		t.Errorf("chat acquired kagent before kmx:\n%s", out)
	}
	out = dryRun(t, "chat", "KAGENT=/tmp/kagent")
	if !strings.Contains(out, `KAGENT="$KMX_KAGENT"`) {
		t.Errorf("chat dropped the explicit KAGENT override:\n%s", out)
	}
}

func TestChatPassesInteractiveSettings(t *testing.T) {
	out := dryRun(t, "chat", "INTERACTIVE=1", "SESSION=session-1", "AGENT=hello-tools")
	for _, want := range []string{"agent chat --interactive", `--session "$KMX_CHAT_SESSION"`, `"$KMX_CHAT_AGENT"`} {
		if !strings.Contains(out, want) {
			t.Errorf("interactive chat does not pass %s:\n%s", want, out)
		}
	}
}

func TestChatSessionIsNotInterpolatedIntoShellSource(t *testing.T) {
	out := dryRun(t, "chat", "INTERACTIVE=1", "SESSION=x' ; touch /tmp/pwn ; : '")
	if strings.Contains(out, "touch /tmp/pwn") {
		t.Fatalf("session value was interpolated into shell source:\n%s", out)
	}
	if !strings.Contains(out, `--session "$KMX_CHAT_SESSION"`) {
		t.Fatalf("session is not read from the exported environment:\n%s", out)
	}
}

func TestInteractiveChatDoesNotSendTheDefaultTask(t *testing.T) {
	out := dryRun(t, "chat", "INTERACTIVE=1")
	if strings.Contains(out, config.DefaultTask) {
		t.Fatalf("interactive chat sent the default one-shot task:\n%s", out)
	}
	out = dryRun(t, "chat", "INTERACTIVE=1", "TASK=explicit first turn")
	if !strings.Contains(out, `"$KMX_CHAT_TASK"`) {
		t.Fatalf("interactive chat dropped an explicit task:\n%s", out)
	}
}

// The managed path is NOT kmx's: kmx covers the kind path only. Its
// bring-up must still be the Makefile's own
// recipes, and so must its plane and its governance: kmx side-loads a local
// image and applies the manifest unrendered, which on a registry-backed
// cluster would mean ErrImageNeverPull, forever.
func TestTheManagedPathDoesNotDelegate(t *testing.T) {
	out := dryRun(t, "cluster", "TARGET=aks", "AKS_RESOURCE_GROUP=rg", "ACR_NAME=acr")
	if strings.Contains(out, "bin/kmx up") {
		t.Errorf("TARGET=aks must not route cluster bring-up through kmx:\n%s", out)
	}
	if !strings.Contains(out, "aks-up.sh") {
		t.Errorf("TARGET=aks cluster should still run scripts/aks-up.sh:\n%s", out)
	}

	// The recipes themselves, read out of make's own database. `make -n`
	// cannot be used for this: a recipe line containing $(MAKE) is executed
	// even under -n (that is make's recursion rule), and the managed
	// `govern` has one — it would go looking for a real AKS context.
	// Question mode prints the database and runs nothing.
	db := recipes(t, "TARGET=aks", "AKS_RESOURCE_GROUP=rg", "ACR_NAME=acr")
	for target, want := range map[string]string{
		"plane":          "plane-deploy.sh",
		"plane-image":    "az acr build",
		"plane-secrets":  "plane-secrets.sh",
		"govern":         "plane-admin.sh issue",
		"ledger":         "plane-admin.sh ledger",
		"grants":         "plane-admin.sh grants",
		"tool-audit":     "plane-admin.sh tool-audit",
		"approval-audit": "plane-admin.sh approval-audit",
	} {
		recipe, ok := db[target]
		if !ok {
			t.Errorf("TARGET=aks has no %s recipe at all", target)
			continue
		}
		if strings.Contains(recipe, "$(KMX)") || strings.Contains(recipe, "bin/kmx") {
			t.Errorf("TARGET=aks %s must stay on the scripts, not kmx:\n%s", target, recipe)
		}
		if !strings.Contains(recipe, want) {
			t.Errorf("TARGET=aks %s no longer runs %q:\n%s", target, want, recipe)
		}
	}
}

// recipes returns each target's recipe as make itself records it, without
// running anything (`make -qp`: question mode, print database).
func recipes(t *testing.T, args ...string) map[string]string {
	t.Helper()
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make is not installed")
	}
	cmd := exec.Command("make", append([]string{"-qp"}, args...)...)
	cmd.Dir = "../../.."
	// -q exits 1 when a target is out of date; the database is still
	// printed, so the exit status is not the signal here.
	out, _ := cmd.Output()

	db := map[string]string{}
	target, body := "", []string{}
	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "\t"):
			if target != "" {
				body = append(body, line)
			}
		case strings.HasPrefix(line, "#"), line == "":
			// Comments and blanks separate entries but do not end a recipe
			// (make interleaves them), so only a new target line does.
		default:
			if target != "" {
				db[target] = strings.Join(body, "\n")
			}
			target, body = "", nil
			if name, _, ok := strings.Cut(line, ":"); ok && !strings.ContainsAny(name, " =$") {
				target = name
			}
		}
	}
	if target != "" {
		db[target] = strings.Join(body, "\n")
	}
	return db
}

// Everything embed.go carries is inside the binary, so editing any of it has
// to RELINK. Without that the Makefile happily reuses a bin/kmx built before
// the edit and applies the previous file — the same class of staleness the
// unconditional proxy restart exists for.
//
// The list is DERIVED from embed.go's //go:embed directives rather than
// restated here. The version that restated it named two files, and while it
// sat there green KMX_ASSETS drifted twelve files behind embed.go: the WASM
// runtime, the release blueprint, and every script and observability
// manifest the managed path ships. A guard that repeats what it guards
// cannot catch drift, because the drift happens in the half it does not
// read.
func TestEveryEmbeddedFileRelinksKmx(t *testing.T) {
	embedded := embeddedFiles(t)
	assets := makeVariable(t, "KMX_ASSETS")
	for _, file := range embedded {
		if !strings.Contains(assets, file) {
			t.Errorf("embed.go carries %s but KMX_ASSETS does not list it, so editing it would not relink bin/kmx", file)
		}
	}
}

// embeddedFiles expands embed.go's //go:embed patterns to the files on disk.
//
// The vacuity guards here are the point of the exercise and are derived too:
// a directive syntax that stops matching, or a pattern that expands to
// nothing, would leave this test iterating an empty list and passing — which
// is exactly the failure it replaced.
func embeddedFiles(t *testing.T) []string {
	t.Helper()
	root := filepath.Join("..", "..", "..")
	source, err := os.ReadFile(filepath.Join(root, "embed.go"))
	if err != nil {
		t.Fatal(err)
	}
	directive := regexp.MustCompile(`(?m)^//go:embed (.+)$`)
	matches := directive.FindAllStringSubmatch(string(source), -1)
	if len(matches) == 0 {
		t.Fatal("no //go:embed directive found in embed.go — this scan is reading for a syntax that is " +
			"no longer there, so it would pass while checking nothing")
	}
	var files []string
	for _, match := range matches {
		for _, pattern := range strings.Fields(match[1]) {
			found := expandEmbedPattern(t, root, pattern)
			if len(found) == 0 {
				t.Errorf("//go:embed %s matches nothing on disk", pattern)
			}
			files = append(files, found...)
		}
	}
	if len(files) == 0 {
		t.Fatal("embed.go's directives expanded to no files at all")
	}
	return files
}

// expandEmbedPattern resolves one pattern the way go:embed does: a directory
// contributes every file under it except those whose name begins with "." or
// "_", and anything else is a path or a glob.
func expandEmbedPattern(t *testing.T, root, pattern string) []string {
	t.Helper()
	info, err := os.Stat(filepath.Join(root, pattern))
	if err == nil && info.IsDir() {
		var found []string
		err = filepath.WalkDir(filepath.Join(root, pattern), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || strings.HasPrefix(d.Name(), ".") || strings.HasPrefix(d.Name(), "_") {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			found = append(found, filepath.ToSlash(rel))
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return found
	}
	globbed, err := filepath.Glob(filepath.Join(root, pattern))
	if err != nil {
		t.Fatalf("%s is not a usable pattern: %v", pattern, err)
	}
	var found []string
	for _, path := range globbed {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			t.Fatal(err)
		}
		found = append(found, filepath.ToSlash(rel))
	}
	return found
}

// makeVariable asks make what a variable expands to, so the test reads the
// same value a recipe would.
func makeVariable(t *testing.T, name string) string {
	t.Helper()
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make is not installed")
	}
	cmd := exec.Command("make", "-f", "Makefile", "-f", "/dev/stdin", "print-"+name)
	cmd.Stdin = strings.NewReader("print-%:\n\t@echo $($*)\n")
	cmd.Dir = "../../.."
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("cannot read %s: %v", name, err)
	}
	return string(out)
}
