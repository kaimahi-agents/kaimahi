package app

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

// appWithKubectl puts a kubectl on PATH that does whatever the script says,
// so cluster reads and writes can be driven without a real cluster.
func appWithKubectl(t *testing.T, script string) *App {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake kubectl is a shell script")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	r := run.Default()
	r.Stdout, r.Stderr, r.Echo = out, errOut, false
	return &App{
		Cfg: &config.Config{KubeContext: "kind-kaimahi-p1", ContextSource: config.SourceKubeCtx},
		Run: r, Out: out, Err: errOut,
	}
}

// Capture the build environment before fixtures restrict PATH or change the
// target environment. Only the helper build uses it; kubectl still inherits the
// active fixture's environment exactly as it did when re-executing App tests.
var kubectlFixtureBuildEnv = os.Environ()
var kubectlFixtureGoTool, kubectlFixtureGoErr = exec.LookPath("go")

var kubectlFixtureBuild struct {
	sync.Once
	dir, binary string
	err         error
}

func TestMain(m *testing.M) {
	status := m.Run()
	if kubectlFixtureBuild.dir != "" {
		if err := os.RemoveAll(kubectlFixtureBuild.dir); err != nil {
			fmt.Fprintln(os.Stderr, "remove kubectl fixture build:", err)
			status = 1
		}
	}
	os.Exit(status)
}

// kubectlFixture keeps the fake at the real executable boundary without paying
// for unrelated UI and application initialization on every kubectl invocation.
func kubectlFixture(t *testing.T, helper string) string {
	t.Helper()
	return "exec " + shellArg(kubectlFixtureBinary(t)) + " -test.run=^" + helper + "$ -- \"$@\""
}

func kubectlFixtureBinary(t *testing.T) string {
	t.Helper()
	kubectlFixtureBuild.Do(func() {
		_, source, _, ok := runtime.Caller(0)
		if !ok {
			kubectlFixtureBuild.err = fmt.Errorf("locate kubectl fixture source")
			return
		}
		dir, err := os.MkdirTemp("", "kmx-kubectl-fixture-")
		if err != nil {
			kubectlFixtureBuild.err = err
			return
		}
		kubectlFixtureBuild.dir = dir
		binary := filepath.Join(dir, "kubectl.test")
		if runtime.GOOS == "windows" {
			binary += ".exe"
		}
		args := []string{"test", "-c", "-o", binary}
		if kubectlFixtureRace {
			args = append(args, "-race")
		}
		args = append(args, "./testdata/kubectl")
		if kubectlFixtureGoErr != nil {
			kubectlFixtureBuild.err = fmt.Errorf("locate Go for kubectl fixture build: %w", kubectlFixtureGoErr)
			return
		}
		cmd := exec.CommandContext(t.Context(), kubectlFixtureGoTool, args...)
		cmd.Dir = filepath.Dir(source)
		cmd.Env = append(append([]string(nil), kubectlFixtureBuildEnv...), "GOOS="+runtime.GOOS, "GOARCH="+runtime.GOARCH)
		if out, err := cmd.CombinedOutput(); err != nil {
			kubectlFixtureBuild.err = fmt.Errorf("build kubectl fixture: %w\n%s", err, out)
			return
		}
		kubectlFixtureBuild.binary = binary
	})
	if kubectlFixtureBuild.err != nil {
		t.Fatal(kubectlFixtureBuild.err)
	}
	return kubectlFixtureBuild.binary
}
