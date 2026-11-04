package localtools

import "testing"

func TestCommandConfigSelectsFixedDeclarations(t *testing.T) {
	raw := []byte("commandTools:\n  check:\n    command: /usr/bin/true\n    args: [one, '']\n    workingDirectory: subdir\n    env: {TOKEN: HOST_TOKEN}\n  unused:\n    command: /usr/bin/false\n")
	defs, err := parseCommandConfig(raw, []string{"check"})
	if err != nil || len(defs) != 1 || defs[0].name != "check" || defs[0].command != "/usr/bin/true" || defs[0].directory != "subdir" || len(defs[0].args) != 2 || defs[0].env["TOKEN"] != "HOST_TOKEN" {
		t.Fatalf("definitions=%+v err=%v", defs, err)
	}
}

func TestCommandConfigRefusesDynamicOrAmbiguousLaunches(t *testing.T) {
	for _, raw := range []string{
		"commandTools: {check: {command: go}}\n",
		"commandTools: {check: {command: /usr/bin/true, workingDirectory: /tmp}}\n",
		"commandTools: {check: {command: /usr/bin/true, unknown: value}}\n",
		"commandTools: {check: {command: /usr/bin/true, env: {TOKEN: bad-name}}}\n",
		"commandTools: {check: {command: /usr/bin/true}, check: {command: /usr/bin/false}}\n",
	} {
		if _, err := parseCommandConfig([]byte(raw), []string{"check"}); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
}

func TestCommandConfigIgnoresUnselectedCommandSemantics(t *testing.T) {
	raw := []byte("commandTools:\n  selected:\n    command: /usr/bin/true\n  unused:\n    command: relative\n    unknown: [invalid]\n")
	defs, err := parseCommandConfig(raw, []string{"selected"})
	if err != nil || len(defs) != 1 || defs[0].name != "selected" {
		t.Fatalf("selected definition blocked by unused semantics: %+v %v", defs, err)
	}
	if _, err := parseCommandConfig(raw, []string{"unused"}); err == nil {
		t.Fatal("invalid selected definition accepted")
	}
}
