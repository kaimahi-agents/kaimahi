package app

import (
	"bytes"
	"os"
	"reflect"
	"regexp"
	"testing"
)

func commandNames(commands []slashCommand) []string {
	names := make([]string, 0, len(commands))
	for _, command := range commands {
		names = append(names, command.name)
	}
	return names
}

func TestDiscardEscapeSequenceConsumesNavigationKeys(t *testing.T) {
	for _, sequence := range []string{"[A", "[1~", "[3~", "OH"} {
		reader := bytes.NewBufferString(sequence + "x")
		if err := discardEscapeSequence(reader); err != nil {
			t.Fatalf("sequence %q: %v", sequence, err)
		}
		if got := reader.String(); got != "x" {
			t.Fatalf("sequence %q left %q", sequence, got)
		}
	}
}

func TestSlashTrieMatchesPrefixes(t *testing.T) {
	for _, tc := range []struct {
		prefix string
		want   []string
	}{
		{"/", []string{"/exit", "/govern", "/history", "/new", "/resume", "/retry", "/session", "/sessions", "/tools", "/ungovern"}},
		{"/s", []string{"/session", "/sessions"}},
		{"/sess", []string{"/session", "/sessions"}},
		{"/hist", []string{"/history"}},
		{"/unknown", []string{}},
	} {
		if got := commandNames(slashMatches(tc.prefix)); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("slashMatches(%q)=%v, want %v", tc.prefix, got, tc.want)
		}
	}
}

func TestSlashCompletionUsesLongestCommonPrefix(t *testing.T) {
	for _, tc := range []struct{ line, want string }{
		{"/hi", "/history"},
		{"/s", "/session"},
		{"/session", "/session"},
		{"/res", "/resume"},
		{"/resume", "/resume "},
		{"/tools", "/tools "},
		{"/unknown", "/unknown"},
	} {
		if got := completeSlash(tc.line, slashMatches(tc.line)); got != tc.want {
			t.Errorf("completeSlash(%q)=%q, want %q", tc.line, got, tc.want)
		}
	}
}

func TestSlashHintsStopAtArguments(t *testing.T) {
	if got := slashMatches("/tools "); got != nil {
		t.Fatalf("argument input produced command hints: %v", got)
	}
	if got := slashHint(slashMatches("/hist")); got != "/history" {
		t.Fatalf("unexpected hint %q", got)
	}
}

func TestSlashHintFitsTerminalWidth(t *testing.T) {
	hint := fitSlashHint(slashHint(slashMatches("/")), 40)
	if len([]rune(hint)) > 38 || hint[len(hint)-3:] != "..." {
		t.Fatalf("hint was not bounded to terminal width: %q", hint)
	}
	if got := fitSlashHint("/history", 40); got != "/history" {
		t.Fatalf("short hint changed: %q", got)
	}
}

// dispatchedSlashCommands reads the chat loop's own dispatch switch and
// returns the slash commands it acts on. Reading the source is deliberate: a
// list written out here would only ever prove that the commands someone
// remembered are still dispatched, and the command this test used to miss
// (/quit) was missed for exactly that reason.
func dispatchedSlashCommands(t *testing.T) map[string]bool {
	t.Helper()
	source, err := os.ReadFile("chat_interactive.go")
	if err != nil {
		t.Fatal(err)
	}
	// Two shapes appear in the switch: an exact match on the whole line, and
	// a prefix match for the commands that take an argument.
	pattern := regexp.MustCompile(`message == "(/[a-z]+)"|strings\.HasPrefix\(message, "(/[a-z]+) "\)`)
	dispatched := map[string]bool{}
	for _, match := range pattern.FindAllStringSubmatch(string(source), -1) {
		for _, name := range match[1:] {
			if name != "" {
				dispatched[name] = true
			}
		}
	}
	// If the switch is ever rewritten into a shape this does not recognise,
	// the test must say so rather than quietly checking an empty set.
	if len(dispatched) < 8 {
		t.Fatalf("found only %d dispatched slash commands (%v); the dispatch switch no longer has the shape this test reads", len(dispatched), dispatched)
	}
	return dispatched
}

// The registry is what the chat loop completes on Tab, hints under the prompt
// and prints as its command summary. A command the loop obeys but the registry
// omits is invisible to every one of those, and a command the registry offers
// but the loop ignores does nothing when typed. Both directions are checked.
func TestTheSlashRegistryAndTheDispatchSwitchAgree(t *testing.T) {
	// The one divergence that exists today: /quit is dispatched as a synonym
	// for /exit but is offered nowhere. It is pinned here so the gap cannot
	// widen, and so that closing it — in either direction — makes this test
	// fail and be updated rather than pass silently.
	undocumented := map[string]bool{"/quit": true}

	registry := map[string]bool{}
	for _, command := range slashCommandList {
		registry[command.name] = true
	}
	dispatched := dispatchedSlashCommands(t)

	for name := range dispatched {
		if !registry[name] && !undocumented[name] {
			t.Errorf("the chat loop dispatches %s but the slash registry does not offer it", name)
		}
		if registry[name] && undocumented[name] {
			t.Errorf("%s is now in the registry; remove it from the undocumented list here", name)
		}
	}
	for name := range undocumented {
		if !dispatched[name] {
			t.Errorf("%s is listed as an undocumented dispatch but is no longer dispatched; remove it here", name)
		}
	}
	for name := range registry {
		if !dispatched[name] {
			t.Errorf("the slash registry offers %s but the chat loop does nothing with it", name)
		}
	}
}
