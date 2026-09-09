package app

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/lift"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

// A kubectl stub that refuses what real kubectl refuses.
//
// kubectl has no `networkpolicy` COMMAND. Handed one where a verb belongs, it
// concludes the word must name a plugin binary (kubectl-networkpolicy) and,
// because flags precede it, exits 1 with "flags cannot be placed before plugin
// name". A stub that answered every invocation cheerfully would have passed
// while the real thing failed on every lift, so this one carries the single
// rule that was actually broken: after the flags, the first bare word must be
// a verb.
//
// It also records what it was asked, because a test that only checks the exit
// status cannot tell a fixed call from a call that never happened.
func stubKubectlThatChecksTheVerb(t *testing.T, dir string) string {
	t.Helper()
	log := filepath.Join(dir, "kubectl.log")
	script := `#!/bin/sh
printf '%s\n' "$*" >> ` + log + `
while [ $# -gt 0 ]; do
  case "$1" in
    --context|-n|--namespace|-o|--output) shift 2 ;;
    -*) shift ;;
    *) break ;;
  esac
done
case "$1" in
  get|apply|delete|create|describe|patch|exec|logs|rollout|wait|version|config) ;;
  *)
    echo "Error: flags cannot be placed before plugin name: --context" >&2
    exit 1 ;;
esac
echo "networkpolicy.networking.k8s.io/whatever"
`
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return log
}

// The defect this pins: `recordPreExistingState` asked kubectl whether two
// objects existed and never said `get`. It is upstream of the only line that
// sets Before.Recorded, so no run could get past it and no resumed run could
// skip it — the observability phase of EVERY lift failed, on a bring-your-own
// cluster and on one the lift had just created alike. It failed closed, which
// is why it destroyed nothing and why nothing but running it noticed.
func TestRecordingWhatWasHereBeforeAsksKubectlSomethingKubectlAccepts(t *testing.T) {
	dir := t.TempDir()
	// az answers the one call this path makes: the cluster's current
	// monitoring configuration. Nothing is enabled, which is the branch that
	// carries on into the two existence reads.
	if err := os.WriteFile(filepath.Join(dir, "az"), []byte("#!/bin/sh\necho '{}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	log := stubKubectlThatChecksTheVerb(t, dir)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var errOut bytes.Buffer
	a := &App{
		Cfg: &config.Config{KubeContext: "kind-test"},
		Run: &run.Runner{Stdout: io.Discard, Stderr: io.Discard},
		Out: io.Discard, Err: &errOut,
	}
	record := &lift.Record{}
	if err := a.recordPreExistingState(lift.Options{Cluster: "c", ResourceGroup: "rg"}, record, func() error { return nil }); err != nil {
		t.Fatalf("recording the cluster's prior state failed, so the observability phase cannot start: %v", err)
	}
	if !record.Before.Recorded {
		t.Fatal("the prior state was not recorded, so teardown would have nothing to consult")
	}

	// Without this the test is vacuous: a path that made no kubectl call at
	// all would satisfy every assertion above.
	asked, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("the stub kubectl was never called: %v", err)
	}
	for _, want := range []string{"networkpolicy " + scraperPolicy, scrapeMonitorResource + " " + scrapeMonitor} {
		if !strings.Contains(string(asked), "get "+want) {
			t.Errorf("no `get %s` in what kubectl was asked:\n%s", want, asked)
		}
	}
}

// The prior-state read runs BEFORE the metrics add-on is enabled, and the
// add-on is what installs the PodMonitor CRD. So on every fresh cluster the
// question "is there already a PodMonitor of ours" is asked of a cluster that
// has no such kind, and kubectl answers "the server doesn't have a resource
// type" — which is not a NotFound and which this package deliberately refuses
// to read as absence everywhere else. Read as a refusal here it would block
// the phase exactly the way the missing verb did.
func TestAClusterWithNoPodMonitorCRDIsNotARefusal(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "az"), []byte("#!/bin/sh\necho '{}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// kubectl's real words for a kind the cluster does not have.
	script := `#!/bin/sh
case "$*" in
  *podmonitors*)
    echo "error: the server doesn't have a resource type \"podmonitors\"" >&2
    exit 1 ;;
esac
echo "networkpolicy.networking.k8s.io/whatever"
`
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	a := &App{
		Cfg: &config.Config{KubeContext: "kind-test"},
		Run: &run.Runner{Stdout: io.Discard, Stderr: io.Discard},
		Out: io.Discard, Err: io.Discard,
	}
	record := &lift.Record{}
	if err := a.recordPreExistingState(lift.Options{Cluster: "c", ResourceGroup: "rg"}, record, func() error { return nil }); err != nil {
		t.Fatalf("a cluster with no PodMonitor CRD blocked the phase: %v", err)
	}
	if record.Before.ScrapeMonitorExisted {
		t.Fatal("a kind the cluster does not have was recorded as an object that was already there")
	}
	if !record.Before.WeCreatedScrapeMonitor() {
		t.Fatal("the run may not remove the PodMonitor it is about to create")
	}
}

// And the general form, because the specific fix above only closes one call
// site.
//
// Every kubectl command kmx builds is assembled by App.kubectl, which prepends
// `--context <ctx>`. So a call whose arguments omit the verb produces
// flags-then-a-noun, which kubectl reads as a plugin name and rejects. A
// compiler cannot see it, a reviewer skims past it, and the only other way to
// find out is to run the command against a cluster.
//
// So the source is parsed instead, and two rules are enforced over both shapes
// the package uses: the three kubectlCapture/Run/Quiet helpers, and the direct
// `Run.Run("kubectl", a.kubectl(...)...)` form that exec, port-forward and the
// manifest applies use. A regular expression could not do this — it cannot
// tell `"-n", someNamespaceVariable` from `"-n"` followed by nothing, and gets
// the position of the verb wrong the moment a flag's value is a variable,
// which is most of them.
//
// Rule one: the first argument that is neither a flag nor a flag's value must
// be a kubectl verb.
//
// Rule two, and this is the one that covers the defect above rather than
// merely its neighbours: no call may hand kubectl an argument slice assembled
// somewhere else. `objectExists` did exactly that — it took `args ...string`
// and spread them, so the verb was the CALLER'S business and no reader of
// either end could see whether one had been supplied. Rule one cannot judge
// such a call, which is precisely the problem with it. One pass-through
// survives, App.Capture in app.go, named explicitly rather than
// pattern-matched: it exists to satisfy the admin.Kube interface, whose
// callers live in another package.
func TestEveryKubectlCallInThisPackageNamesAVerb(t *testing.T) {
	verbs := map[string]bool{
		"get": true, "apply": true, "create": true, "delete": true, "describe": true,
		"patch": true, "replace": true, "edit": true, "label": true, "annotate": true,
		"exec": true, "logs": true, "cp": true, "port-forward": true, "attach": true,
		"rollout": true, "scale": true, "wait": true, "run": true, "expose": true,
		"set": true, "config": true, "cluster-info": true, "version": true, "api-resources": true,
		"api-versions": true, "auth": true, "top": true, "explain": true, "diff": true,
		"kustomize": true, "drain": true, "cordon": true, "uncordon": true, "taint": true,
	}
	// Flags that take a value as the NEXT argument, so the value is not
	// mistaken for the verb. A flag missing from here is not guessed at: the
	// scan says so and fails, because guessing is how a check quietly starts
	// answering a weaker question than it advertises.
	takesValue := map[string]bool{
		"-n": true, "--namespace": true, "-o": true, "--output": true, "-l": true,
		"--selector": true, "-f": true, "--filename": true, "--context": true,
		"--timeout": true, "--for": true, "--type": true, "-c": true, "--container": true,
		"--field-selector": true, "--tail": true, "--patch": true, "-p": true,
		"--patch-file": true, "--address": true, "--kubeconfig": true,
	}

	fset := token.NewFileSet()
	// Every non-test file in the package, parsed one at a time.
	// `parser.ParseDir` would say this in a line, and is deprecated because it
	// ignores build tags when deciding which files belong to a package — which
	// for a scan whose whole value is that it misses nothing would be the
	// wrong trade.
	sources, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]*ast.File{}
	for _, path := range sources {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files[path] = parsed
	}
	if len(files) < 10 {
		t.Fatalf("only %d source files found in this package — the scan is not seeing them", len(files))
	}

	checked := 0
	// judge walks one kubectl argument list the way kubectl's own flag parser
	// does, stopping at the first bare word.
	judge := func(where token.Pos, args []ast.Expr) {
		for i := 0; i < len(args); i++ {
			word, isLiteral := stringLiteral(args[i])
			if !isLiteral {
				return // a variable here; this scan cannot judge the call
			}
			if strings.HasPrefix(word, "-") {
				switch {
				case takesValue[word]:
					i++ // the value, whatever it is
				case strings.Contains(word, "="):
					// --timeout=300s and friends carry their own value
				default:
					t.Errorf("%s: %q is not in this test's flag table, so where the verb "+
						"begins in this call is a guess — add it, saying whether it takes a value",
						fset.Position(where), word)
					return
				}
				continue
			}
			checked++
			if !verbs[word] {
				t.Errorf("%s: this kubectl call reaches %q where a verb belongs — kubectl "+
					"will treat it as a plugin name and refuse to run at all",
					fset.Position(where), word)
			}
			return
		}
	}

	for path, file := range files {
		{
			enclosing := ""
			ast.Inspect(file, func(n ast.Node) bool {
				if fn, ok := n.(*ast.FuncDecl); ok {
					enclosing = fn.Name.Name
					return true
				}
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				switch sel.Sel.Name {
				case "kubectlCapture", "kubectlRun", "kubectlQuiet":
					if call.Ellipsis.IsValid() {
						if enclosing == "Capture" && filepath.Base(path) == "app.go" {
							return true // the admin.Kube adapter; see above
						}
						t.Errorf("%s: this kubectl command is assembled by whoever calls %s, so "+
							"nothing here can tell whether it names a verb — pass the parts as "+
							"named arguments and build the command line at this end",
							fset.Position(call.Pos()), enclosing)
						return true
					}
					judge(call.Pos(), call.Args)
				default:
					// The direct form: Run.Run("kubectl", a.kubectl(...)...)
					// and its Capture/Quiet/Pipe/RunStdin/Command siblings.
					// The command line is inside the inner a.kubectl call.
					for _, arg := range call.Args {
						inner, ok := arg.(*ast.CallExpr)
						if !ok {
							continue
						}
						innerSel, ok := inner.Fun.(*ast.SelectorExpr)
						if !ok || innerSel.Sel.Name != "kubectl" {
							continue
						}
						judge(inner.Pos(), inner.Args)
					}
				}
				return true
			})
		}
	}
	// A scan that matches no call passes every assertion in the loop it never
	// enters. This package makes dozens.
	if checked < 40 {
		t.Fatalf("only %d kubectl calls were examined in this package — the scan is not seeing them, "+
			"so it is passing vacuously", checked)
	}
}

// stringLiteral reports an argument's value when it is a plain string literal
// in the source. Anything else — a variable, a concatenation, a function call
// — is not something a source scan may draw a conclusion from.
func stringLiteral(expr ast.Expr) (string, bool) {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	value, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return value, true
}

// An unreadable cluster is not "you have no scrape jobs".
//
// Teardown warns, before it disables the metrics add-on, that doing so removes
// the PodMonitor KIND and takes every PodMonitor on the cluster with it. If the
// read behind that warning fails — an unreachable API server, an RBAC denial —
// treating the failure as an empty inventory produces silence at exactly the
// moment the sentence matters, and the add-on goes anyway. Only kubectl saying
// the cluster has no such kind is an answer, because then there is genuinely
// nothing of anyone's to lose.
func TestAnUnreadableClusterStillWarnsAboutScrapeJobsTheAddonWouldTake(t *testing.T) {
	for _, tc := range []struct {
		name, stderr string
		wantSilence  bool
	}{
		{
			name:        "the kind is not on this cluster",
			stderr:      `error: the server doesn't have a resource type "podmonitors"`,
			wantSilence: true,
		},
		{
			name:   "the API server cannot be reached",
			stderr: "Unable to connect to the server: dial tcp: i/o timeout",
		},
		{
			name:   "the read is forbidden",
			stderr: `Error from server (Forbidden): podmonitors.azmonitoring.coreos.com is forbidden`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			// A heredoc, because kubectl's own wording contains an
			// apostrophe ("doesn't have a resource type") and a shell-quoted
			// echo would mangle the one string this test most needs verbatim.
			script := "#!/bin/sh\ncat >&2 <<'KUBECTL_STDERR'\n" + tc.stderr + "\nKUBECTL_STDERR\nexit 1\n"
			if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

			var errOut bytes.Buffer
			a := &App{
				Cfg: &config.Config{KubeContext: "kind-test"},
				Run: &run.Runner{Stdout: io.Discard, Stderr: io.Discard},
				Out: io.Discard, Err: &errOut,
			}
			a.warnAboutScrapeJobsTheAddonOwns()

			said := errOut.String()
			if tc.wantSilence {
				if said != "" {
					t.Errorf("a cluster with no such kind has nothing to warn about, but it said:\n%s", said)
				}
				return
			}
			if said == "" {
				t.Fatal("the read failed and teardown said nothing — an operator loses their scrape " +
					"jobs to the next line of this command with no sentence about it")
			}
			if !strings.Contains(said, "could not read the PodMonitors") {
				t.Errorf("the warning does not say the read failed:\n%s", said)
			}
		})
	}
}
