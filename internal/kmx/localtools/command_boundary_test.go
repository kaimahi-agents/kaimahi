package localtools

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestCommandPatternsMatchTheWholeValue(t *testing.T) {
	cfg := CommandConfig{ArgumentPlan: []CommandArgument{{Literal: "--mode"}, {Parameter: "mode"}}, Parameters: map[string]CommandParameter{"mode": {Type: "string", Pattern: `^foo|bar$`, MaxLength: 64}}}
	if err := validateCommandContract(cfg); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"foo", "bar"} {
		if _, err := commandArguments(cfg, map[string]any{"mode": value}); err != nil {
			t.Fatal(err)
		}
	}
	for _, value := range []string{"foo; rm -rf x", "prefixbar", "foo\nsuffix", "foobar"} {
		if _, err := commandArguments(cfg, map[string]any{"mode": value}); err == nil {
			t.Fatalf("partial match accepted: %q", value)
		}
	}
	check := CommandCheck{ExitCode: 0, StdoutPattern: `^ready|ok$`}
	for _, value := range []string{"ready-extra", "prefixok", "ready\nextra"} {
		if checkCommandObservation(check, map[string]any{"exitCode": 0, "stdout": value}) == nil {
			t.Fatalf("partial probe match: %q", value)
		}
	}
	if err := checkCommandObservation(check, map[string]any{"exitCode": 0, "stdout": "ready\n"}); err != nil {
		t.Fatal(err)
	}
}

func TestCommandResultWithholdsNormalizedSplitCredential(t *testing.T) {
	dir := t.TempDir()
	check, err := validateCommandConfig(CommandConfig{Name: "check", Command: filepath.Join(dir, "tool"), Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	out := []byte("ghp_\x1b[0m" + strings.Repeat("a", 18))
	diagnostic := []byte(strings.Repeat("a", 18))
	if result, err := commandResult(0, out, diagnostic, check); err == nil || result != nil {
		t.Fatal("normalized cross-stream credential escaped")
	}
}

func TestCommandPlanDigestBindsResolvedArgumentsAndChecks(t *testing.T) {
	p := CommandPlan{ConfigRevision: "source", Commands: []CommandConfig{{Name: "test", Command: "/usr/bin/tool", Args: []string{"--safe"}}}}
	before, err := p.Digest()
	if err != nil {
		t.Fatal(err)
	}
	p.Commands[0].Args[0] = "--different"
	after, _ := p.Digest()
	if before == after {
		t.Fatal("resolved arguments not bound")
	}
	p.Commands[0].Checks = []CommandCheck{{Name: "readiness", TimeoutMilliseconds: 1000, ExitCode: 0}}
	checked, _ := p.Digest()
	if after == checked {
		t.Fatal("probe not bound")
	}
}
