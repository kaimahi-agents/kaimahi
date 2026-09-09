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
// site. Every kubectl command kmx builds goes through the three helpers on
// App, each of which prepends `--context <ctx>` — so a call whose arguments
// omit the verb produces flags-then-a-noun, which kubectl reads as a plugin
// name and rejects. A compiler cannot see it, a reviewer skims past it, and
// the only other way to find out is to run the command against a cluster.
//
// So the source is read instead, and two rules are enforced.
//
// First, in every call whose arguments are written out, the first argument
// that is neither a flag nor a flag's value must be a kubectl verb.
//
// Second — and this is the one that covers the defect above rather than
// merely its neighbours — no call may hand kubectl an argument slice
// assembled somewhere else. `objectExists` did exactly that: it took
// `args ...string` and spread them, so the verb was the CALLER'S business
// and no reader of either end could see whether one had been supplied. The
// first rule cannot judge such a call, which is precisely the problem with
// it. One pass-through survives, App.Capture, and it is named explicitly
// rather than pattern-matched: it exists to satisfy the admin.Kube
// interface, whose callers live in another package.
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
	// mistaken for the verb.
	takesValue := map[string]bool{
		"-n": true, "--namespace": true, "-o": true, "--output": true, "-l": true,
		"--selector": true, "-f": true, "--filename": true, "--context": true,
		"--timeout": true, "--for": true, "--type": true, "-c": true, "--container": true,
		"--field-selector": true, "--tail": true, "--patch": true, "-p": true,
	}

	// Parsed rather than grepped. A regular expression over the source cannot
	// tell `"-n", someNamespaceVariable` from `"-n"` followed by nothing, and
	// gets the position of the verb wrong the moment a flag's value is a
	// variable — which is most of them.
	fset := token.NewFileSet()
	pkg, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatal(err)
	}

	checked := 0
	for _, p := range pkg {
		for _, file := range p.Files {
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
				default:
					return true
				}
				if call.Ellipsis.IsValid() {
					if enclosing == "Capture" {
						return true // the admin.Kube adapter; see above
					}
					t.Errorf("%s: this kubectl command is assembled by whoever calls %s, so "+
						"nothing here can tell whether it names a verb — pass the parts as "+
						"named arguments and build the command line at this end",
						fset.Position(call.Pos()), enclosing)
					return true
				}
				// Walk the argument list the way kubectl's own flag parser
				// does, stopping at the first bare word.
				for i := 0; i < len(call.Args); i++ {
					word, isLiteral := stringLiteral(call.Args[i])
					if !isLiteral {
						return true // a variable here; this scan cannot judge the call
					}
					if strings.HasPrefix(word, "-") {
						if takesValue[word] {
							i++ // the value, whatever it is
						}
						continue
					}
					checked++
					if !verbs[word] {
						t.Errorf("%s: this kubectl call reaches %q where a verb belongs — kubectl "+
							"will treat it as a plugin name and refuse to run at all",
							fset.Position(call.Pos()), word)
					}
					return true
				}
				return true
			})
		}
	}
	// The scan has been wrong before by finding nothing: a regexp that matches
	// no call passes every assertion in the loop it never enters.
	if checked < 20 {
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
