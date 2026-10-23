// Package agentfile parses and loads single-file agent charters.
// Explicit Squad parsing returns a distinct charter type, not a runnable Agent.
// Parsing and inspection do not execute agents or register them with Squad.
package agentfile

import (
	"crypto/sha256"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
)

const (
	MaxFileBytes  = 256 * 1024
	MaxExamples   = 128
	MaxInputBytes = 16 * 1024
	MaxAssertions = 64
)

type Location struct {
	File         string
	Line, Column int
}
type Identity struct{ ID, Purpose string }

// Agent contains authored content, not a safe log or result projection.
// Revision binds exact bytes; Instructions normalizes CRLF and excludes Examples.
type Agent struct {
	Identity     Identity
	Title        string
	Instructions string
	Model        string
	Examples     []Example
	Revision     string
	Locations    map[string]Location
	// NativeTaskInstructions is set only by the explicit native preview adapter.
	// Real routes retain Instructions; fake orientation uses this task-only text.
	NativeTaskInstructions string
}
type Example struct {
	ID, Input          string
	Assertions         []runtime.EvaluationAssertion
	Location           Location
	AssertionLocations []Location
}

// Error names canonical fields and a repair without quoting source values.
// Field is a comma-separated canonical list when several fields are missing.
type Error struct {
	Location       Location
	Field, Problem string
}

func (e *Error) Error() string {
	file := e.Location.File
	for _, shape := range secretshapes.All() {
		if shape.Re.MatchString(file) {
			file = "[agent file]"
			break
		}
	}
	return fmt.Sprintf("%s:%d:%d: %s: %s", strconv.Quote(file), e.Location.Line, e.Location.Column, e.Field, e.Problem)
}
func refuse(file string, line, column int, field, problem string) *Error {
	return &Error{Location{file, line, column}, field, problem}
}

var (
	agentID      = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	exampleID    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)
	modelID      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,255}$`)
	metadata     = regexp.MustCompile(`^- \*\*(ID|Purpose|Preferred):\*\*[ \t]*(.*)$`)
	sectionNames = []string{"Identity", "Accountable Responsibilities", "Non-Responsibilities", "Collaboration and Review Authority", "Model", "Examples"}
)

type section struct{ start, end int }
type fence struct {
	marker byte
	count  int
}

// Parse validates one bounded charter. filename supplies diagnostics and, only
// for the canonical Squad placement, an identity constraint. It reads no agent
// files; relative charter.md names consult the current directory for placement.
func Parse(filename string, data []byte) (*Agent, error) {
	return parse(filename, data, false)
}

// Inspect reads either a canonical charter or a minimal titled agent for display.
// It grants no execution acceptance: Parse remains the Run/Test validator.
func Inspect(filename string, data []byte) (*Agent, error) {
	return parse(filename, data, true)
}

func parse(filename string, data []byte, inspection bool) (*Agent, error) {
	fail := func(field, problem string) (*Agent, error) { return nil, refuse(filename, 1, 1, field, problem) }
	text, err := checkedSourceText(filename, data)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(text, "\n")
	a := &Agent{Revision: fmt.Sprintf("sha256:%x", sha256.Sum256(data)), Locations: map[string]Location{}}
	sections := map[string]section{}
	current := ""
	var open fence
	first := true
	prelude := false
	for i, line := range lines {
		if open.count > 0 {
			if closeFence(line, open) {
				open = fence{}
			}
			continue
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		if strings.Contains(line, "<!--") {
			return nil, refuse(filename, i+1, 1, "Sections", "remove HTML comments outside fenced prose")
		}
		if first {
			first = false
			if !strings.HasPrefix(line, "# ") || strings.TrimSpace(line[2:]) == "" {
				return fail("Title", "start with one nonblank # title; frontmatter is unsupported")
			}
			a.Title = strings.TrimSpace(line[2:])
			a.Locations["Title"] = Location{filename, i + 1, 1}
			continue
		}
		if strings.HasPrefix(line, "# ") {
			return nil, refuse(filename, i+1, 1, "Title", "keep only one # title")
		}
		// Structural headings must use the canonical unindented spelling. Reject
		// lookalikes instead of accidentally sending an Examples block as prose.
		trimmed := strings.TrimLeft(line, " \t")
		if strings.HasPrefix(trimmed, "## ") || trimmed == "##" || strings.HasPrefix(trimmed, "##\t") {
			name := strings.TrimSpace(strings.TrimPrefix(trimmed, "##"))
			if current == "Examples" {
				return nil, refuse(filename, i+1, 1, "Examples", "keep exactly one Examples section last")
			}
			if !slices.Contains(sectionNames, name) || line != "## "+name {
				return nil, refuse(filename, i+1, 1, "Sections", "use only the canonical unindented ## headings, with no trailing spaces or tabs")
			}
			if _, exists := sections[name]; exists {
				return nil, refuse(filename, i+1, 1, name, "keep exactly one section")
			}
			if current != "" {
				s := sections[current]
				s.end = i
				sections[current] = s
			}
			current = name
			sections[name] = section{i, len(lines)}
			a.Locations[name] = Location{filename, i + 1, 1}
		} else if current == "" {
			if !inspection {
				return nil, refuse(filename, i+1, 1, "Sections", "put instructions inside the canonical responsibility sections")
			}
			prelude = true
		}
		if f, _, ok := openFence(line); ok {
			open = f
		}
	}
	if open.count > 0 {
		field := "Sections"
		if current == "Examples" {
			field = "Examples"
		}
		return nil, refuse(filename, len(lines), 1, field, "close the Markdown fence with matching markers")
	}
	if s, ok := sections["Examples"]; ok {
		a.Instructions = strings.Join(lines[:s.start], "\n") + "\n"
	}
	_, hasIdentity := sections["Identity"]
	minimal := inspection && !hasIdentity
	if inspection && hasIdentity && prelude {
		return fail("Sections", "put instructions inside the canonical responsibility sections")
	}
	if minimal {
		id := strings.TrimSuffix(path.Base(filename), ".agent.md")
		if !strings.HasSuffix(filename, ".agent.md") || !agentID.MatchString(id) || !prelude {
			return fail("Instructions", "use a named .agent.md file with nonblank instructions after the title")
		}
		a.Identity.ID = id
	}
	missing := []string{}
	for _, name := range sectionNames {
		if minimal && name != "Model" && name != "Examples" {
			continue
		}
		if _, ok := sections[name]; !ok {
			switch name {
			case "Identity":
				missing = append(missing, "Identity.ID", "Identity.Purpose")
			case "Model":
				missing = append(missing, "Model.Preferred")
			default:
				missing = append(missing, name)
			}
		}
	}
	// Inspect present metadata too, so a legacy shape reports all missing fields.
	for _, name := range []string{"Identity", "Model"} {
		s, ok := sections[name]
		if !ok {
			continue
		}
		values := map[string]string{}
		for i := s.start + 1; i < s.end; i++ {
			if strings.TrimSpace(lines[i]) == "" {
				continue
			}
			match := metadata.FindStringSubmatch(lines[i])
			if match == nil || (name == "Identity" && match[1] != "ID" && match[1] != "Purpose") || (name == "Model" && match[1] != "Preferred") {
				return nil, refuse(filename, i+1, 1, name, "use only the required canonical bullet fields")
			}
			if _, exists := values[match[1]]; exists {
				return nil, refuse(filename, i+1, 1, name, "remove duplicate fields")
			}
			values[match[1]] = strings.TrimSpace(match[2])
			a.Locations[name+"."+match[1]] = Location{filename, i + 1, 1}
		}
		keys := []string{"ID", "Purpose"}
		if name == "Model" {
			keys = []string{"Preferred"}
		}
		for _, key := range keys {
			if values[key] == "" {
				missing = append(missing, name+"."+key)
			}
		}
		if name == "Identity" {
			a.Identity = Identity{values["ID"], values["Purpose"]}
		} else {
			a.Model = values["Preferred"]
		}
	}
	if a.Title == "" {
		missing = append(missing, "Title")
	}
	if len(missing) > 0 {
		return fail(strings.Join(missing, ", "), "add nonblank required fields in the canonical charter format")
	}
	if !agentID.MatchString(a.Identity.ID) {
		loc := a.Locations["Identity.ID"]
		return nil, refuse(filename, loc.Line, loc.Column, "Identity.ID", "use 1–63 lowercase letters, digits or hyphens, starting with a letter")
	}
	slash := path.Clean(strings.ReplaceAll(filename, "\\", "/"))
	if strings.EqualFold(path.Base(slash), "charter.md") {
		absolute, err := filepath.Abs(filename)
		if err != nil {
			return fail("File", "resolve relative charter.md from an accessible current directory")
		}
		slash = path.Clean(strings.ReplaceAll(absolute, "\\", "/"))
	}
	parts := strings.Split(slash, "/")
	// Recognize canonical placement even through dot segments, a relative
	// basename, or case-insensitive filesystem spelling. Never infer an ID.
	if len(parts) >= 4 && strings.EqualFold(parts[len(parts)-4], ".squad") && strings.EqualFold(parts[len(parts)-3], "agents") && strings.EqualFold(parts[len(parts)-1], "charter.md") && parts[len(parts)-2] != a.Identity.ID {
		loc := a.Locations["Identity.ID"]
		return nil, refuse(filename, loc.Line, loc.Column, "Identity.ID", "match the .squad/agents/<id>/charter.md directory ID")
	}
	if !modelID.MatchString(a.Model) || strings.EqualFold(a.Model, "auto") || strings.Contains(a.Model, "://") {
		loc := a.Locations["Model.Preferred"]
		return nil, refuse(filename, loc.Line, loc.Column, "Model.Preferred", "set one concrete exact model ID, not auto, a URL or a wildcard")
	}
	for _, name := range sectionNames[1:4] {
		if minimal {
			if _, present := sections[name]; !present {
				continue
			}
		}
		s := sections[name]
		if strings.TrimSpace(strings.Join(lines[s.start+1:s.end], "\n")) == "" {
			return nil, refuse(filename, s.start+1, 1, name, "add nonblank instructions")
		}
	}
	s := sections["Examples"]
	examples, err := parseExamples(filename, lines[s.start+1:s.end], s.start+2)
	if err != nil {
		return nil, err
	}
	a.Examples = examples
	return a, nil
}

func checkedSourceText(filename string, data []byte) (string, error) {
	fail := func(problem string) (string, error) { return "", refuse(filename, 1, 1, "File", problem) }
	if len(data) > MaxFileBytes {
		return fail("use a file of at most 256 KiB")
	}
	if !utf8.Valid(data) {
		return fail("use valid UTF-8")
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	if strings.HasPrefix(text, "\uFEFF") {
		return fail("remove the UTF-8 byte-order mark")
	}
	if strings.IndexFunc(text, unsupportedControl) >= 0 {
		return fail("remove control characters except newline/tab and use LF or CRLF line breaks")
	}
	if _, at := secretshapes.MatchIndex(text); at >= 0 {
		return "", refuse(filename, strings.Count(text[:at+1], "\n")+1, 1, "File", "remove credential-shaped content; agent files must not contain secrets")
	}
	return text, nil
}

func unsupportedControl(r rune) bool {
	return (unicode.IsControl(r) && r != '\n' && r != '\t') || r == '\u2028' || r == '\u2029'
}

func openFence(line string) (fence, string, bool) {
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 || len(trimmed) < 3 {
		return fence{}, "", false
	}
	marker := trimmed[0]
	if marker != '`' && marker != '~' {
		return fence{}, "", false
	}
	n := 0
	for n < len(trimmed) && trimmed[n] == marker {
		n++
	}
	if n < 3 {
		return fence{}, "", false
	}
	info := strings.TrimSpace(trimmed[n:])
	if marker == '`' && strings.Contains(info, "`") {
		return fence{}, "", false
	}
	return fence{marker, n}, info, true
}
func closeFence(line string, open fence) bool {
	f, info, ok := openFence(line)
	return ok && f.marker == open.marker && f.count >= open.count && info == ""
}
