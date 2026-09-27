package app

import (
	"bytes"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func commandNames(commands []slashCommand) []string {
	names := make([]string, 0, len(commands))
	for _, command := range commands {
		names = append(names, command.name)
	}
	return names
}

func TestSlashTrieMatchesPrefixes(t *testing.T) {
	for _, tc := range []struct {
		prefix string
		want   []string
	}{
		{"/", []string{"/agent", "/exit", "/help", "/inference", "/inference-copilot", "/inference-foundry", "/inference-local", "/lift", "/retry", "/tools", "/verbose-off", "/verbose-on"}},
		{"/verbose", []string{"/verbose-off", "/verbose-on"}},
		{"/h", []string{"/help"}},
		{"/l", []string{"/lift"}},
		{"/inference-", []string{"/inference-copilot", "/inference-foundry", "/inference-local"}},
		{"/unknown", []string{}},
	} {
		if got := commandNames(slashMatches(tc.prefix)); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("slashMatches(%q)=%v, want %v", tc.prefix, got, tc.want)
		}
	}
}

func TestOrkaSlashCompletionOnlyOffersItsCommands(t *testing.T) {
	matches := slashMatchesFrom(orkaSlashCommands, "/")
	names := strings.Join(commandNames(matches), ",")
	for _, want := range []string{"/agent", "/tools", "/lift", "/inference-copilot", "/verbose-on"} {
		if !strings.Contains(names, want) {
			t.Fatalf("missing %s: %s", want, names)
		}
	}
	if strings.Contains(names, "/govern") || strings.Contains(names, "/session") {
		t.Fatal("retired-runtime commands offered to Orka")
	}
	if got := slashMatchesFrom(orkaSlashCommands, "/inference-"); len(got) != 3 {
		t.Fatalf("matches=%v", got)
	}
	if got := slashMatchesFrom(orkaSlashCommands, "/tools "); len(got) != 0 {
		t.Fatal("popup remained on arguments")
	}
}

func TestSlashPopupFitsAndKeepsSelectionVisible(t *testing.T) {
	selected := 0
	for i, command := range orkaSlashCommands {
		if command.name == "/verbose-off" {
			selected = i
		}
	}
	rows := slashPopup(orkaSlashCommands, selected, 32, 6)
	if len(rows) > 6 {
		t.Fatal("popup too tall")
	}
	if !strings.Contains(strings.Join(rows, "\n"), "/verbose-off") {
		t.Fatal("selection scrolled out")
	}
	for _, row := range rows {
		if lipgloss.Width(row) > 32 {
			t.Fatalf("row too wide: %q", row)
		}
	}
}

func TestSlashCompletionUsesLongestCommonPrefix(t *testing.T) {
	for _, tc := range []struct{ line, want string }{
		{"/he", "/help"},
		{"/l", "/lift"},
		{"/lift", "/lift "},
		{"/inference-c", "/inference-copilot"},
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
	if got := slashHint(slashMatches("/lif")); got != "/lift — deploy to another cluster" {
		t.Fatalf("unexpected hint %q", got)
	}
}

func TestSlashHintFitsTerminalWidth(t *testing.T) {
	hint := fitSlashHint(slashHint(slashMatches("/")), 40)
	if lipgloss.Width(hint) > 38 || hint[len(hint)-3:] != "..." {
		t.Fatalf("hint was not bounded to terminal width: %q", hint)
	}
	if got := fitSlashHint("/history", 40); got != "/history" {
		t.Fatalf("short hint changed: %q", got)
	}
	for _, input := range []string{"/界界界界界界", "/e\u0301e\u0301e\u0301e\u0301e\u0301"} {
		if got := fitSlashHint(input, 10); lipgloss.Width(got) > 8 {
			t.Errorf("display-width hint overflowed: %q (%d cells)", got, lipgloss.Width(got))
		}
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
	// The shared driver keys its switch on a normalised command, so the
	// names it owns appear as case labels. Everything else is a RUNTIME
	// command: the driver routes it to the session by name, and the session
	// decides. Both halves are collected, or the registry check below would
	// call every Orka command undispatched.
	pattern := regexp.MustCompile(`case "(/[a-z-]+)"(?:, "(/[a-z-]+)")*:`)
	dispatched := map[string]bool{}
	for _, match := range pattern.FindAllStringSubmatch(string(source), -1) {
		for _, name := range match[1:] {
			if name != "" {
				dispatched[name] = true
			}
		}
	}
	// /quit shares its case label with /exit, and Go's multi-value case is
	// not captured repeatedly by one regexp group.
	if bytes.Contains(source, []byte(`case "/exit", "/quit":`)) {
		dispatched["/quit"] = true
	}
	for _, command := range (&orkaRuntimeSession{}).Commands() {
		dispatched[command.Name] = true
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
