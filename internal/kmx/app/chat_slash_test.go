package app

import (
	"bytes"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/cliui"
)

func TestChatInputLayoutPlainRows(t *testing.T) {
	for _, tc := range []struct {
		name, prompt, line, hint string
		width, cursorRow         int
		want                     []string
	}{
		{"wrapped", "YOU > ", "hello", "help", 8, 1, []string{"YOU > he", "llo"}},
		{"exact width", "YOU > ", "hi", "help", 8, 1, []string{"YOU > hi", ""}},
		{"narrow editing glyph", "", "界", "", 1, 0, []string{"?", ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := layoutChatInput(chatInputLayoutOptions{prompt: tc.prompt, line: tc.line, hint: tc.hint,
				width: tc.width, height: 24, cursorRow: tc.cursorRow})
			if !reflect.DeepEqual(got.rows, tc.want) || got.cursorRow != tc.cursorRow || got.cursorColumn != 0 || got.hint != tc.hint {
				t.Fatalf("layout=%+v, want rows=%q cursorRow=%d hint=%q", got, tc.want, tc.cursorRow, tc.hint)
			}
		})
	}
}

func TestChatInputLayoutPopupOffsetsTheEditorCursor(t *testing.T) {
	got := layoutChatInput(chatInputLayoutOptions{
		ui:         cliui.WithCapabilities(cliui.Capabilities{Rich: true, Width: 40}),
		promptKind: cliui.FocusMessage, prompt: "YOU > ", line: "/", hint: "inline hint",
		width: 40, height: 24, framed: true, cursorRow: 4, selected: 1,
		matches: []slashCommand{{"/a", "/a alpha"}, {"/b", "/b beta"}, {"/c", "/c gamma"}},
	})
	if len(got.rows) != 8 || got.cursorRow != 6 || got.cursorColumn != 9 || got.hint != "" {
		t.Fatalf("popup/editor layout=%+v, want 8 rows, cursor=(6,9), no inline hint", got)
	}
	if !strings.Contains(ansi.Strip(got.rows[2]), "› /b beta") || !strings.Contains(ansi.Strip(got.rows[6]), "YOU > /") {
		t.Fatalf("selection or cursor row misplaced: %q", got.rows)
	}
}

func TestChatInputLayoutClipsOnlyTheEditingViewport(t *testing.T) {
	for _, tc := range []struct {
		name              string
		height, cursorRow int
		want              []string
	}{
		{"three rows", 4, 2, []string{strings.Repeat("x", 40), "[... input omitted ...]", ""}},
		{"two rows", 3, 1, []string{"[... input omitted ...]", ""}},
		{"one row", 2, 0, []string{"[... input omitted ...]"}},
		{"minimum height", 1, 0, []string{"[... input omitted ...]"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := layoutChatInput(chatInputLayoutOptions{line: strings.Repeat("x", 200), width: 40, height: tc.height, cursorRow: 4})
			if !reflect.DeepEqual(got.rows, tc.want) || got.cursorRow != tc.cursorRow {
				t.Fatalf("clipped layout=%+v, want rows=%q cursorRow=%d", got, tc.want, tc.cursorRow)
			}
		})
	}
}

func TestChatInputLayoutSubmissionPreservesNarrowGraphemes(t *testing.T) {
	got := layoutChatInput(chatInputLayoutOptions{line: "\x1b[31m界e\u0301👩‍💻xy\x1b[0m", hint: "editing hint",
		width: 1, height: 1, complete: true})
	if text := strings.Join(got.rows, ""); text != "界e\u0301👩‍💻xy" {
		t.Fatalf("submission was clipped or changed: %q", text)
	}
	if len(got.rows) <= 1 || got.hint != "" {
		t.Fatalf("submission retained viewport clipping or an editing hint: %+v", got)
	}
}

func TestChatInputLayoutBoxesOnlySubmittedMessages(t *testing.T) {
	for _, tc := range []struct {
		name, prompt, line string
		kind               cliui.FocusKind
		boxed              bool
		want               []string
	}{
		{"message", "YOU > ", "hello", cliui.FocusMessage, true, nil},
		{"slash command", "YOU > ", "/help", cliui.FocusMessage, false, []string{"YOU > /help"}},
		{"empty", "YOU > ", "", cliui.FocusMessage, false, []string{"YOU > "}},
		{"whitespace", "YOU > ", "   ", cliui.FocusMessage, false, []string{"YOU >    "}},
		{"non-message input", "Answer: ", "yes", cliui.FocusQuestion, false, []string{"Answer: yes"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := layoutChatInput(chatInputLayoutOptions{ui: cliui.WithCapabilities(cliui.Capabilities{Rich: true, Width: 40}),
				promptKind: tc.kind, prompt: tc.prompt, line: tc.line, hint: "editing hint",
				width: 40, height: 24, framed: true, complete: true})
			if got.hint != "" || got.cursorColumn != 0 {
				t.Fatalf("submission retained editor state: %+v", got)
			}
			if tc.boxed {
				if len(got.rows) != 3 || !strings.Contains(ansi.Strip(got.rows[0]), "─ YOU ") || !strings.Contains(ansi.Strip(got.rows[1]), "hello") {
					t.Fatalf("message is not a durable transcript box: %q", got.rows)
				}
			} else if !reflect.DeepEqual(got.rows, tc.want) {
				t.Fatalf("non-message submission=%q, want %q", got.rows, tc.want)
			}
		})
	}
}

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
	if got := slashHint(slashMatches("/lif")); got != interactiveLiftRetirement {
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
// returns the slash commands it acts on. Reading the source is deliberate:
// a hand-maintained expected list could omit a dispatched command and leave
// the registry's completion and help coverage unchecked.
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
