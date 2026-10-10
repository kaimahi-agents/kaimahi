package main

import (
	"bytes"
	"errors"
	"reflect"
	"regexp"
	"runtime/debug"
	"sort"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
)

var removedPlaneCommands = []string{"plane", "migrate", "credential", "credentials", "ledger", "budget", "flow", "watch", "backup", "restore", "metrics", "models", "govern", "use"}

// Removed commands must fail before resolving environment or touching a cluster,
// including former nested commands, legacy aliases and invocations with flags.
func TestRemovedPlaneCommandsAreUnknownBeforeConfiguration(t *testing.T) {
	var invocations [][]string
	for _, name := range removedPlaneCommands {
		invocations = append(invocations, []string{name}, []string{name, "--help"})
	}
	invocations = append(invocations,
		[]string{"models", "add", "demo", "--url", "http://localhost:11434", "--classification", "public"},
		[]string{"models", "credential", "copilot"},
		[]string{"credential", "issue", "demo", "--discard"},
		[]string{"credential", "renew", "demo", "--ttl", "1d"},
		[]string{"govern", "hello-world", "--model", "governed-ollama"},
		[]string{"use", "ollama", "--agent", "hello-world"},
	)
	for _, invocation := range invocations {
		for _, withContext := range []bool{false, true} {
			args := append([]string(nil), invocation...)
			if withContext {
				args = append([]string{"--context", "kind-stale"}, args...)
			}
			t.Run(strings.Join(args, " "), func(t *testing.T) {
				var out, diagnostics bytes.Buffer
				deps, loads := testDependencies(&out, &diagnostics)
				apps := 0
				deps.loadConfig = func(string, string) (*config.Config, error) {
					*loads++
					return nil, errors.New("poisoned operational config")
				}
				deps.newApp = func(*config.Config) *app.App { apps++; panic("unexpected App construction") }
				err := execute(args, deps)
				if err == nil || !strings.Contains(err.Error(), "unknown command \""+invocation[0]+"\"") {
					t.Errorf("removed command did not fail as unknown: %v", err)
				}
				if *loads != 0 || apps != 0 {
					t.Errorf("removed command loaded config %d times and constructed %d Apps", *loads, apps)
				}
				if out.Len() != 0 || diagnostics.Len() != 0 {
					t.Errorf("unknown command printed help or runtime output: stdout=%q stderr=%q", out.String(), diagnostics.String())
				}
			})
		}
	}
}

func TestPlaneCommandsAreNotRegisteredOrDiscoverable(t *testing.T) {
	var out, diagnostics bytes.Buffer
	deps, loads := testDependencies(&out, &diagnostics)
	deps.loadConfig = func(string, string) (*config.Config, error) { *loads++; return nil, errors.New("poisoned config") }
	root := newRootCommand(&commandState{deps: deps})
	for _, cmd := range root.Commands() {
		for _, name := range removedPlaneCommands {
			if cmd.Name() == name || cmd.HasAlias(name) {
				t.Errorf("removed command %q is still registered", name)
			}
		}
	}
	want := []string{"agent", "completion", "console", "context", "help", "local", "orka", "quickstart", "status", "suite", "targets", "task", "version"}
	sort.Strings(want)
	for _, args := range [][]string{{}, {"--help"}, {"help"}, {"__complete", ""}} {
		out.Reset()
		diagnostics.Reset()
		if err := execute(args, deps); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		var got []string
		if len(args) > 0 && args[0] == "__complete" {
			for _, line := range strings.Split(out.String(), "\n") {
				if line != "" && !strings.HasPrefix(line, ":") && !strings.HasPrefix(line, "--") {
					got = append(got, strings.SplitN(line, "\t", 2)[0])
				}
			}
		} else {
			_, commands, found := strings.Cut(out.String(), "Available Commands:\n")
			if !found {
				t.Fatalf("%v lost command help: %s", args, out.String())
			}
			commands, _, _ = strings.Cut(commands, "\n\n")
			entries := regexp.MustCompile(`(?m)^  ([a-z][a-z-]*) +\S`).FindAllStringSubmatch(commands, -1)
			for _, entry := range entries {
				got = append(got, entry[1])
			}
		}
		sort.Strings(got)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%v command entries=%v, want %v", args, got, want)
		}
	}
	// Shell-specific static command entries must not resurrect removed roots.
	for _, shell := range []string{"bash", "zsh", "fish"} {
		out.Reset()
		if err := execute([]string{"completion", shell}, deps); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "__complete") {
			t.Fatalf("%s lost completion protocol", shell)
		}
		for _, name := range removedPlaneCommands {
			pattern := `(?m)(?:^\s*['"]` + name + `:| -a ['"]?` + name + `(?:['"]|\s|$)|^\s*` + name + `\))`
			if regexp.MustCompile(pattern).MatchString(out.String()) {
				t.Errorf("%s completion advertises %q", shell, name)
			}
		}
	}
	for _, name := range removedPlaneCommands {
		out.Reset()
		if err := execute([]string{"__complete", name}, deps); err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(out.String(), "\n") {
			if strings.SplitN(line, "\t", 2)[0] == name {
				t.Errorf("dynamic completion advertises %q", name)
			}
		}
	}
	if *loads != 0 {
		t.Fatalf("help/completion loaded config %d times", *loads)
	}
}

func TestVersionReportsNativeTargetsWithoutPlane(t *testing.T) {
	for _, tc := range []struct {
		name string
		info *debug.BuildInfo
		ok   bool
		want string
	}{
		{"unknown", nil, false, "kmx unknown"},
		{"tagged", &debug.BuildInfo{Main: debug.Module{Version: "v0.3.0"}}, true, "kmx v0.3.0"},
		{"dirty", &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}, Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "0123456789abcdef"}, {Key: "vcs.modified", Value: "true"}}}, true, "kmx v0.0.0-dev+0123456789ab.dirty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			deps, loads := testDependencies(&out, &diagnostics)
			deps.buildInfo = func() (*debug.BuildInfo, bool) { return tc.info, tc.ok }
			if err := execute([]string{"version"}, deps); err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{tc.want, "orka", app.OrkaVersion, "model", config.DefaultModel} {
				if !strings.Contains(out.String(), want) {
					t.Errorf("version missing %q: %s", want, out.String())
				}
			}
			if strings.Contains(strings.ToLower(out.String()), "plane") {
				t.Errorf("version still reports the plane: %s", out.String())
			}
			if *loads != 0 {
				t.Fatal("version loaded operational config")
			}
		})
	}
}
