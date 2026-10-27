package main

import (
	"bytes"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func helpEntries(output string) []string {
	_, rest, _ := strings.Cut(output, "Available Commands:\n")
	list, _, _ := strings.Cut(rest, "\n\n")
	var names []string
	for _, match := range regexp.MustCompile(`(?m)^  ([a-z][a-z-]*) +\S`).FindAllStringSubmatch(list, -1) {
		names = append(names, match[1])
	}
	return names
}

func TestShortHelpRootDiscovery(t *testing.T) {
	want := []string{"create", "run", "test", "chat", "model", "package", "cluster", "version"}
	sorting := cobra.EnableCommandSorting
	for _, args := range [][]string{nil, {"--help"}, {"help"}} {
		var out, diag bytes.Buffer
		deps, loads := testDependencies(&out, &diag)
		if err := execute(args, deps); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(out.String(), "Write, run and test agent files.\n") || !reflect.DeepEqual(helpEntries(out.String()), want) {
			t.Fatalf("%v: wrong root help:\n%s", args, out.String())
		}
		if *loads != 0 || diag.Len() != 0 {
			t.Fatalf("help effects: %d / %s", *loads, &diag)
		}
	}
	if cobra.EnableCommandSorting != sorting {
		t.Fatal("help changed global Cobra sorting")
	}
}

func TestShortHelpInnerLoopBudgets(t *testing.T) {
	for _, path := range [][]string{{"create"}, {"run"}, {"test"}, {"test", "explain"}, {"chat"}, {"model"}} {
		t.Run(strings.Join(path, "/"), func(t *testing.T) {
			var out, diag bytes.Buffer
			deps, loads := testDependencies(&out, &diag)
			root := newRootCommand(&commandState{deps: deps})
			cmd, remaining, err := root.Find(path)
			if err != nil || len(remaining) != 0 {
				t.Fatalf("find %v: %v", path, err)
			}
			if cmd.Short == "" || strings.Contains(cmd.Short, "\n") {
				t.Fatal("summary must be one line")
			}
			if n := len(strings.Split(strings.TrimSpace(cmd.Long), "\n")); n > 5 {
				t.Errorf("description has %d lines", n)
			}
			examples := strings.Split(strings.TrimSpace(cmd.Example), "\n")
			if len(examples) < 2 || len(examples) > 3 {
				t.Errorf("examples=%d", len(examples))
			}
			for _, args := range [][]string{append(append([]string{}, path...), "--help"), append([]string{"help"}, path...)} {
				out.Reset()
				if err := execute(args, deps); err != nil {
					t.Fatal(err)
				}
				output := out.String()
				for _, line := range strings.Split(output, "\n") {
					if len([]rune(line)) > 80 {
						t.Errorf("line wider than 80: %s", line)
					}
				}
				start := strings.Index(output, "\nFlags")
				if start < 0 {
					t.Fatal("missing flag section")
				}
				if count := len(strings.Split(strings.TrimSpace(output[start:]), "\n")); count > 24 {
					t.Errorf("flags occupy %d lines", count)
				}
				if *loads != 0 || diag.Len() != 0 {
					t.Fatalf("help effects: %d / %s", *loads, &diag)
				}
			}
		})
	}
}

func TestShortHelpFlagScopesAndCaveats(t *testing.T) {
	for _, path := range [][]string{{"create"}, {"test"}, {"test", "explain"}, {"model"}} {
		var out, diag bytes.Buffer
		deps, _ := testDependencies(&out, &diag)
		if err := execute(append(append([]string{}, path...), "--help"), deps); err != nil {
			t.Fatal(err)
		}
		for _, flag := range []string{"--context", "--container-engine"} {
			if strings.Contains(out.String(), flag) {
				t.Errorf("%v advertises cluster selector %s", path, flag)
			}
		}
		if len(path) == 2 && strings.Contains(out.String(), "--workspace") {
			t.Error("file Explain advertises workspace mode")
		}
	}
	for _, verb := range []string{"run", "test"} {
		var out, diag bytes.Buffer
		deps, _ := testDependencies(&out, &diag)
		if err := execute([]string{verb, "--help"}, deps); err != nil {
			t.Fatal(err)
		}
		output := out.String()
		if strings.Count(output, "https://github.com/kaimahi-agents/kaimahi/blob/main/docs/local-preview.md") != 1 || !strings.Contains(strings.ToLower(output), "retention") {
			t.Errorf("%s missing single retention/privacy reference: %s", verb, output)
		}
		if verb == "run" {
			_, fileFlags, _ := strings.Cut(output, "Flags (file preview):")
			fileFlags, rest, ok := strings.Cut(fileFlags, "Flags (shared input):")
			sharedFlags, liveFlags, liveOK := strings.Cut(rest, "Flags (live Orka):")
			if !ok || !liveOK || strings.Contains(fileFlags, "--context") || !strings.Contains(liveFlags, "--agent") || !strings.Contains(liveFlags, "--context") {
				t.Fatal("file/shared/live flag scopes confused")
			}
			for _, flag := range []string{"--prompt ", "--prompt-file "} {
				if !strings.Contains(sharedFlags, flag) || strings.Contains(fileFlags, flag) || strings.Contains(liveFlags, flag) {
					t.Errorf("shared input flag misplaced: %s", flag)
				}
			}
			for _, usage := range []string{"Input text (FILE or live Agent)", "Input file, or - for stdin (FILE/live Agent)"} {
				if !strings.Contains(sharedFlags, usage) {
					t.Errorf("shared usage missing: %s", usage)
				}
			}
		}
	}
}

func TestShortHelpOperationalTopicsKeepDefaultRendering(t *testing.T) {
	for _, path := range [][]string{{"model", "login"}, {"model", "list"}, {"package"}, {"package", "test"}, {"cluster"}} {
		var out, diag bytes.Buffer
		deps, loads := testDependencies(&out, &diag)
		if err := execute(append(append([]string{}, path...), "--help"), deps); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "Global Flags:") || *loads != 0 {
			t.Fatalf("%v changed renderer or loaded config: %s", path, &out)
		}
	}
}

func TestShortHelpTechnicalCallsStayInert(t *testing.T) {
	for _, path := range [][]string{{"mcp"}, {"completion"}, {"help"}, {"package", "test"}, {"eval"}, {"lift"}} {
		var out, diag bytes.Buffer
		deps, loads := testDependencies(&out, &diag)
		if err := execute(append(append([]string{}, path...), "--help"), deps); err != nil {
			t.Fatalf("%v: %v", path, err)
		}
		if out.Len() == 0 || *loads != 0 {
			t.Fatalf("%v: empty help or config loaded", path)
		}
	}
}

func TestShortHelpFlagErrorsOfferSafelyQuotedHelp(t *testing.T) {
	for _, executable := range []string{"kmx", "./tools with space/kmx'custom"} {
		for _, path := range [][]string{nil, {"test"}, {"package", "test"}, {"help"}} {
			var out, diag bytes.Buffer
			deps, loads := testDependencies(&out, &diag)
			deps.executable = executable
			deps.goos = "linux"
			args := append(append([]string{}, path...), "--not-a-flag")
			err := execute(args, deps)
			hint := quoteShell(executable)
			if len(path) > 0 {
				hint += " " + strings.Join(path, " ")
			}
			hint += " --help"
			if err == nil || !strings.HasPrefix(err.Error(), "unknown flag: --not-a-flag") || !strings.Contains(err.Error(), "Run "+hint+" for available flags") {
				t.Fatalf("%v: %v", args, err)
			}
			if *loads != 0 || out.Len() != 0 || diag.Len() != 0 {
				t.Fatal("parse error performed effects")
			}
		}
	}
	for _, verb := range []string{"eval", "lift"} {
		for _, prefix := range [][]string{{verb}, {"help", verb}} {
			var out, diag bytes.Buffer
			deps, loads := testDependencies(&out, &diag)
			err := execute(append(prefix, "--not-a-flag"), deps)
			if err == nil || err.Error() != "unknown flag: --not-a-flag" || *loads != 0 || out.Len() != 0 {
				t.Fatalf("legacy changed: %v / %s / %s", err, &out, &diag)
			}
			notice := evalDeprecationNotice
			if verb == "lift" {
				notice = "kmx lift is deprecated; use kmx package deploy (same arguments)."
			}
			expected := notice + "\n"
			if verb == "lift" && len(prefix) == 2 {
				expected = ""
			} // Existing help parser refusal has no lift notice.
			if diag.String() != expected {
				t.Fatalf("legacy stderr changed: %q", diag.String())
			}
		}
	}
}

func TestShortHelpWindowsFlagErrorUsesPowerShell(t *testing.T) {
	for _, tc := range []struct{ executable, quoted string }{
		{"kmx.exe", "'kmx.exe'"},
		{`C:\tools\kmx.exe`, `'C:\tools\kmx.exe'`},
		{`C:\Program Files\Kaimahi\kmx.exe`, `'C:\Program Files\Kaimahi\kmx.exe'`},
		{`C:\O'Brien & $literal\kmx.exe`, `'C:\O''Brien & $literal\kmx.exe'`},
		{`C:\O‘Brien\kmx.exe`, `'C:\O‘‘Brien\kmx.exe'`},
		{`C:\O’Brien\kmx.exe`, `'C:\O’’Brien\kmx.exe'`},
		{`C:\O‚Brien\kmx.exe`, `'C:\O‚‚Brien\kmx.exe'`},
		{`C:\O‛Brien\kmx.exe`, `'C:\O‛‛Brien\kmx.exe'`},
	} {
		var out, diag bytes.Buffer
		deps, loads := testDependencies(&out, &diag)
		deps.goos = "windows"
		deps.executable = tc.executable
		err := execute([]string{"test", "--not-a-flag"}, deps)
		want := "unknown flag: --not-a-flag\nPowerShell: & " + tc.quoted + " test --help"
		if err == nil || err.Error() != want || *loads != 0 || out.Len() != 0 || diag.Len() != 0 {
			t.Errorf("%s: %v; loads=%d stdout=%q stderr=%q", tc.executable, err, *loads, &out, &diag)
		}
	}
}

func TestShortHelpTestExitCodesMatchRuntime(t *testing.T) {
	var out, diag bytes.Buffer
	deps, loads := testDependencies(&out, &diag)
	if err := execute([]string{"test", "--help"}, deps); err != nil {
		t.Fatal(err)
	}
	for _, contract := range []string{"0 pass", "1 assertion fail", "2 unknown/execution error", "130 interrupted"} {
		if !strings.Contains(out.String(), contract) {
			t.Errorf("file Test help omits runtime exit contract %q", contract)
		}
	}
	if *loads != 0 || diag.Len() != 0 {
		t.Fatal("help performed effects")
	}
}
