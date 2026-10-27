package app

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Exercise the installed command boundary with K31's executable protocol, not
// only App injection. This is fixture qualification, never live cluster proof.
func TestSessionsAgentExecutableCLI(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "kmx")
	build := exec.CommandContext(t.Context(), kubectlFixtureGoTool, "build", "-o", binary, "./cmd/kmx")
	build.Dir = "../../.."
	build.Env = kubectlFixtureBuildEnv
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, out)
	}
	_, opt, fixtureDir := sessionsAgentFixture(t)
	tools := t.TempDir()
	fakeTool(t, tools, "kubectl", kubectlFixture(t, "TestSessionsAgentKubectlHelper"))
	root := t.TempDir()
	for name, body := range map[string]string{"workspace.yaml": "schemaVersion: 1\nname: demo\nmember: agents/demo/member.yaml\nbuildProfile: build-profiles/default.json\n", "kmx.yaml": "version: 1\ntargets:\n  sessions: {context: kind-test, namespace: workloads, runtime: agentsessions}\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	base := []string{"package", "deploy", opt.Reference, "--from-layout", opt.FromLayout, "--to", "sessions", "--evidence", opt.Evidence}
	for _, step := range []struct {
		name string
		args []string
		want string
	}{
		{"plan", append(append([]string{}, base...), "--plan"), "Agent/"},
		{"create", base, "Agent: registered"},
		{"no-op", base, "unchanged"},
		{"status", []string{"package", "status", "--to", "sessions"}, "registered: last controller-observed admission"},
	} {
		cmd := exec.CommandContext(t.Context(), binary, step.args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "PATH="+tools+string(os.PathListSeparator)+os.Getenv("PATH"), "KAIMAHI_CONFIRM=")
		if step.name == "create" {
			cmd.Env = append(cmd.Env, "KAIMAHI_CONFIRM=kind-test")
		}
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil || !strings.Contains(stdout.String(), step.want) {
			t.Fatalf("%s: %v\nstdout:%s\nstderr:%s", step.name, err, &stdout, &stderr)
		}
		t.Logf("%s (hermetic kubectl fixture, no live cluster):\n%s%s", step.name, &stderr, &stdout)
	}
	calls, err := os.ReadFile(filepath.Join(fixtureDir, "calls"))
	if err != nil || strings.Count(string(calls), `"create"`) != 1 {
		t.Fatalf("expected one conditional create: %v %s", err, calls)
	}
}
