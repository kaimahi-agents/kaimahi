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

func TestCommandConfigParsesConstrainedArgumentsAndChecks(t *testing.T) {
	raw := []byte(`commandTools:
  check:
    command: /usr/bin/true
    args:
      - status
      - {argument: target}
      - {argument: count}
    parameters:
      target: {type: path, description: Relative target, maxLength: 128}
      count: {type: integer, description: Count, minimum: 1, maximum: 3}
    checks:
      compatibility:
        args: [--version]
        timeout: 3s
        when: activation
        expect: {exitCode: 0, stdoutRegex: '^tool version 1\.[0-9]+$'}
`)
	defs, err := parseCommandConfig(raw, []string{"check"})
	if err != nil || len(defs) != 1 || len(defs[0].args) != 3 || defs[0].args[1].Parameter != "target" || defs[0].parameters["count"].Maximum == nil || *defs[0].parameters["count"].Maximum != 3 || len(defs[0].checks) != 1 || defs[0].checks[0].TimeoutMilliseconds != 3000 {
		t.Fatalf("definitions=%+v err=%v", defs, err)
	}
	cfg := commandConfigFromDefinition(defs[0], "/tmp")
	if err := validateCommandContract(cfg); err != nil {
		t.Fatal(err)
	}
}

func TestCommandConfigRefusesEscapableArgumentContractsAndChecks(t *testing.T) {
	for _, raw := range []string{
		"commandTools: {check: {command: /usr/bin/true, args: [{argument: x}], parameters: {x: {type: string, pattern: '.*', maxLength: 20}}}}\n",
		"commandTools: {check: {command: /usr/bin/true, args: [status, {argument: x}], parameters: {x: {type: string, pattern: '^.*$', enum: [both], maxLength: 20}}}}\n",
		"commandTools: {check: {command: /usr/bin/true, args: [status, {argument: missing}], parameters: {x: {type: path, maxLength: 20}}}}\n",
		"commandTools: {check: {command: /usr/bin/true, checks: {compatibility: {args: [--version], expect: {exitCode: 0, stdoutRegex: version}}}}}\n",
	} {
		defs, err := parseCommandConfig([]byte(raw), []string{"check"})
		if err == nil && len(defs) == 1 {
			if validateCommandContract(commandConfigFromDefinition(defs[0], "/tmp")) == nil {
				t.Fatalf("accepted %q", raw)
			}
		}
	}
}
