package app

import (
	"bytes"
	"encoding/json"
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

// Monitoring ownership is established only from Azure add-on state. No
// Kubernetes-plane read is needed, and an unreadable Azure state must not
// create deletion authority or persist a guessed snapshot.
func TestLiftPriorMonitoringStateIsRecordedWithoutPlaneReads(t *testing.T) {
	for _, reply := range []string{"off", "on", "unreadable", "invalid"} {
		t.Run(reply, func(t *testing.T) {
			dir := t.TempDir()
			body := "#!/bin/sh\necho '{}'\n"
			switch reply {
			case "on":
				body = "#!/bin/sh\necho '{\"azureMonitorProfile\":{\"metrics\":{\"enabled\":true}},\"addonProfiles\":{\"omsagent\":{\"enabled\":true}}}'\n"
			case "unreadable":
				body = "#!/bin/sh\necho 'permission denied' >&2\nexit 1\n"
			case "invalid":
				body = "#!/bin/sh\necho 'not-json'\n"
			}
			if err := os.WriteFile(filepath.Join(dir, "az"), []byte(body), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir) // no kubectl: this read must not require it
			a := &App{Cfg: &config.Config{KubeContext: "cluster"}, Run: &run.Runner{Stdout: io.Discard, Stderr: io.Discard}}
			record, err := lift.NewRecord("abcd1234", lift.BringYourOwn, lift.PayloadOrka, "sub", "rg", "cluster")
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "record.json")
			saved := 0
			err = a.recordPreExistingState(lift.Options{Cluster: "cluster", ResourceGroup: "rg"}, record, func() error {
				saved++
				var data bytes.Buffer
				if err := record.Write(&data); err != nil {
					return err
				}
				return os.WriteFile(path, data.Bytes(), 0o600)
			})
			if reply == "unreadable" || reply == "invalid" {
				if err == nil || !strings.Contains(err.Error(), "cannot read the cluster") || saved != 0 || record.Before.Recorded {
					t.Fatalf("unreadable state authorized cleanup: record=%+v saves=%d err=%v", record, saved, err)
				}
				return
			}
			if err != nil || saved != 1 || !record.Before.Recorded || !record.PlaneMonitoringUnmanaged {
				t.Fatalf("prior state: record=%+v saves=%d err=%v", record, saved, err)
			}
			if record.Before.MetricsAddonEnabled != (reply == "on") || record.Before.LogsAddonEnabled != (reply == "on") {
				t.Fatalf("prior add-on state lost: %+v", record.Before)
			}
			if record.MayRemoveScraperPolicy() || record.MayRemoveScrapeMonitor() {
				t.Fatal("recording add-on state authorized plane-object deletion")
			}
			// Decode the persisted JSON as an older CLI does: the unmanaged
			// marker is unknown, so only the legacy fields deny ownership.
			var old struct {
				Before struct {
					Recorded             bool `json:"recorded"`
					MetricsAddonEnabled  bool `json:"metrics_addon_enabled"`
					LogsAddonEnabled     bool `json:"logs_addon_enabled"`
					ScraperPolicyExisted bool `json:"scraper_policy_existed"`
					ScrapeMonitorExisted bool `json:"scrape_monitor_existed"`
				} `json:"before"`
				ScrapeMonitorApplied bool `json:"scrape_monitor_applied"`
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(data, &old); err != nil {
				t.Fatal(err)
			}
			if !old.Before.Recorded {
				t.Fatal("persisted prior state was not established for the old reader")
			}
			if old.Before.Recorded && !old.Before.ScraperPolicyExisted {
				t.Error("older teardown would delete an unmanaged plane policy")
			}
			if old.Before.Recorded && !old.Before.ScrapeMonitorExisted {
				t.Error("legacy prior state claims ownership of an unmanaged PodMonitor")
			}
			if old.ScrapeMonitorApplied {
				t.Error("persisted snapshot claims to have applied a PodMonitor")
			}
			wantCleanup := reply == "off"
			if (old.Before.Recorded && !old.Before.MetricsAddonEnabled) != wantCleanup ||
				(old.Before.Recorded && !old.Before.LogsAddonEnabled) != wantCleanup ||
				record.Before.WeEnabledMetrics() != wantCleanup || record.Before.WeEnabledLogs() != wantCleanup {
				t.Fatalf("persisted add-on ownership lost: %+v", old.Before)
			}
			before := record.Before
			if err := a.recordPreExistingState(lift.Options{}, record, func() error { t.Fatal("resume rewrote ownership"); return nil }); err != nil || record.Before != before {
				t.Fatalf("resume changed prior state: %+v, %v", record.Before, err)
			}
		})
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
// such a call, which is precisely the problem with it.
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

	for _, file := range files {
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
	// enters. Native runtime and cloud orchestration make at least 25.
	if checked < 25 {
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

// A plane-named PodMonitor is not implicitly ours: a new lift never creates
// it. Owned legacy objects are removed before this warning runs, so every
// object still listed must be named before disabling the add-on's CRD.
func TestLiftScrapeWarningIncludesOwnerCreatedPlaneNamedMonitor(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte("#!/bin/sh\necho kaimahi/kaimahi-plane\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	var out bytes.Buffer
	a := &App{Cfg: &config.Config{KubeContext: "cluster"}, Run: &run.Runner{Stdout: io.Discard, Stderr: io.Discard}, Err: &out}
	a.warnAboutScrapeJobsTheAddonOwns()
	if !strings.Contains(out.String(), "kaimahi/kaimahi-plane") || !strings.Contains(out.String(), "removes the PodMonitor KIND") {
		t.Fatalf("teardown silently removes an owner-created monitor with the old name: %s", out.String())
	}
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
