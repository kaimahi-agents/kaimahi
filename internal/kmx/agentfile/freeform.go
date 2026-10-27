package agentfile

import (
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
	"go.yaml.in/yaml/v3"
)

var (
	// Only for a document that is not valid YAML: still claim a broken tests block
	// so its refusal is reported instead of the file silently running no tests.
	examplesKey = regexp.MustCompile(`(?m)^[{ \t]*['"]?examples['"]?[ \t]*:`)
	// Documented Copilot tool aliases and compatible names (case-insensitive).
	// They name host tool sets, never an exact kmx tool, so they are not mapped.
	copilotToolAliases = []string{"execute", "shell", "bash", "powershell", "read", "notebookread",
		"edit", "multiedit", "write", "notebookedit", "search", "grep", "glob",
		"agent", "custom-agent", "task", "web", "websearch", "webfetch", "todo", "todowrite"}
)

func hasFrontmatter(lines []string) bool { return strings.TrimRight(lines[0], " \t") == "---" }

// walk calls visit for each line outside a fenced block, tracking fences as the
// charter scanner does; an unclosed fence swallows the rest of the file.
func walk(lines []string, from int, visit func(i int, line string)) {
	var open fence
	for i := from; i < len(lines); i++ {
		if open.count > 0 {
			if closeFence(lines[i], open) {
				open = fence{}
			}
			continue
		}
		visit(i, lines[i])
		if f, _, ok := openFence(lines[i]); ok {
			open = f
		}
	}
}

// completeCharter reports an explicit canonical charter: all four required
// unindented headings appear outside fences. Anything less is a free-form prompt.
func completeCharter(lines []string) bool {
	found := map[string]bool{}
	walk(lines, 0, func(_ int, line string) {
		// Spell-checked like the charter scanner, so a lookalike such as
		// "## Identity " still reports its repair instead of becoming prompt text.
		trimmed := strings.TrimLeft(line, " \t")
		if name, ok := strings.CutPrefix(trimmed, "##"); ok && (name == "" || name[0] == ' ' || name[0] == '\t') && slices.Contains(sectionNames[:4], strings.TrimSpace(name)) {
			found[strings.TrimSpace(name)] = true
		}
	})
	return len(found) == 4
}

// parseFreeForm reads a Copilot-format file (YAML frontmatter, then a free-form
// prompt) or a plain prompt with optional canonical Model/Tools controls. Both end
// in an optional final structured Examples section; every other heading, comment
// or fence is prompt text. Only exact valid model and tool identifiers map.
func parseFreeForm(filename string, lines []string, a *Agent) (*Agent, error) {
	start := 0
	a.Identity.ID = filenameID(filename)
	if hasFrontmatter(lines) {
		end := slices.IndexFunc(lines[1:], func(l string) bool { return strings.TrimRight(l, " \t") == "---" }) + 1
		if end == 0 {
			return nil, refuse(filename, 1, 1, "Frontmatter", "close the YAML frontmatter with a --- line")
		}
		start, a.HasFrontmatter = end+1, true
		if end > 1 {
			a.Frontmatter = strings.Join(lines[1:end], "\n") + "\n"
		}
		if err := mapFrontmatter(filename, a); err != nil {
			return nil, err
		}
	}
	type control struct {
		section
		set bool
	}
	var model, tools, examples control
	last, repeated := -1, -1 // last candidate Examples heading; first repeated control
	var open *control
	walk(lines, start, func(i int, line string) {
		if open != nil && (strings.HasPrefix(line, "# ") || strings.HasPrefix(line, "## ")) {
			open.end, open = i, nil
		}
		if line == "## Examples" {
			last = i
		}
		if first, ok := strings.CutPrefix(line, "# "); ok && a.Title == "" && strings.TrimSpace(first) != "" {
			a.Title = strings.TrimSpace(first)
			a.Locations["Title"] = Location{filename, i + 1, 1}
		}
		if a.HasFrontmatter || line != "## Model" && line != "## Tools" {
			return
		}
		c := &model
		if line == "## Tools" {
			c = &tools
		}
		if c.set {
			repeated = max(repeated, i)
			return
		}
		*c, open = control{section{i, len(lines)}, true}, c
		a.Locations[line[3:]] = Location{filename, i + 1, 1}
	})
	if repeated >= 0 {
		return nil, refuse(filename, repeated+1, 1, "Sections", "keep at most one Model and one Tools section")
	}
	if last >= 0 && structuredExamples(lines[last+1:]) {
		examples = control{section{last, len(lines)}, true}
		a.Locations["Examples"] = Location{filename, last + 1, 1}
	}
	if model.set {
		for i := model.start + 1; i < model.end; i++ {
			if strings.TrimSpace(lines[i]) == "" {
				continue
			}
			match := metadata.FindStringSubmatch(lines[i])
			if match == nil || match[1] != "Preferred" || a.Model != "" {
				return nil, refuse(filename, i+1, 1, "Model", "use only one canonical Preferred bullet")
			}
			a.Model = strings.TrimSpace(match[2])
			a.Locations["Model.Preferred"] = Location{filename, i + 1, 1}
		}
		if a.Model == "" || !ValidModel(a.Model) {
			loc := a.Locations["Model"]
			return nil, refuse(filename, loc.Line, loc.Column, "Model.Preferred", "set one concrete exact model ID, not auto, a URL or a wildcard")
		}
	}
	if tools.set {
		var err *Error
		if a.Tools, err = readTools(filename, lines, tools.section); err != nil {
			return nil, err
		}
	}
	// Control sections are source, not prompt; headings and text around them stay.
	var prompt []string
	keptLast := false
	for i := start; i < len(lines); i++ {
		if !slices.ContainsFunc([]control{model, tools, examples}, func(c control) bool { return c.set && i >= c.start && i < c.end }) {
			prompt = append(prompt, lines[i])
			keptLast = i == len(lines)-1
		}
	}
	a.Instructions = strings.Join(prompt, "\n")
	if len(prompt) < len(lines)-start && !keptLast {
		a.Instructions += "\n" // terminate the line before a removed control section
	}
	if strings.TrimSpace(a.Instructions) == "" {
		return nil, refuse(filename, 1, 1, "Instructions", "add nonblank agent instructions")
	}
	if a.Title == "" {
		a.Title = a.Identity.ID // display name for a file that names none
	}
	if examples.set {
		parsed, err := parseExamples(filename, lines[examples.start+1:], examples.start+2, true)
		if err != nil {
			return nil, err
		}
		a.Examples = parsed
	}
	if err := checkExampleTools(filename, a); err != nil {
		return nil, err
	}
	return a, nil
}

// structuredExamples reports whether the lines after a final "## Examples" are
// solely one closed yaml fence whose document has a root examples key. Anything
// else is prose, so a Copilot prompt may discuss examples freely.
func structuredExamples(rest []string) bool {
	i := 0
	for i < len(rest) && strings.TrimSpace(rest[i]) == "" {
		i++
	}
	if i == len(rest) {
		return false
	}
	open, info, ok := openFence(rest[i])
	if !ok || info != "yaml" {
		return false
	}
	end := i + 1
	for end < len(rest) && !closeFence(rest[end], open) {
		end++
	}
	if end == len(rest) || strings.TrimSpace(strings.Join(rest[end+1:], "")) != "" {
		return false
	}
	document := strings.Join(rest[i+1:end], "\n")
	var root yaml.Node
	if yaml.Unmarshal([]byte(document), &root) != nil {
		return examplesKey.MatchString(document)
	}
	// A root mapping with an examples key, in block, flow or quoted form.
	if root.Kind != yaml.DocumentNode || root.Content[0].Kind != yaml.MappingNode {
		return false
	}
	m := root.Content[0]
	for k := 0; k < len(m.Content); k += 2 {
		if m.Content[k].Kind == yaml.ScalarNode && m.Content[k].Value == "examples" {
			return true
		}
	}
	return false
}

// mapFrontmatter maps only settings whose meaning is identical in kmx; the raw
// header remains the source of truth and everything unmapped stays in it.
func mapFrontmatter(filename string, a *Agent) *Error {
	var doc yaml.Node
	if yaml.Unmarshal([]byte(a.Frontmatter), &doc) != nil {
		return refuse(filename, 2, 1, "Frontmatter", "provide valid YAML between the --- lines")
	}
	if doc.Kind == 0 || doc.Content[0].Kind == yaml.ScalarNode && doc.Content[0].Tag == "!!null" {
		a.CopilotToolsOmitted = true
		return nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return refuse(filename, 2, 1, "Frontmatter", "use a YAML mapping of settings")
	}
	fields := map[string]*yaml.Node{}
	for i := 0; i < len(root.Content); i += 2 {
		key := root.Content[i].Value
		if _, dup := fields[key]; dup {
			return refuse(filename, 1+root.Content[i].Line, 1, "Frontmatter", "remove duplicate settings")
		}
		fields[key] = root.Content[i+1]
	}
	text := func(n *yaml.Node) string {
		if n != nil && n.Kind == yaml.ScalarNode && n.Tag == "!!str" {
			return strings.TrimSpace(n.Value)
		}
		return ""
	}
	a.Title, a.Identity.Purpose = text(fields["name"]), text(fields["description"])
	if model := text(fields["model"]); ValidModel(model) {
		a.Model = model
	}
	list, ok := fields["tools"]
	a.CopilotToolsOmitted = !ok
	// Copilot accepts a YAML list or one comma-separated string of tool names.
	var names []string
	switch {
	case !ok:
	case list.Kind == yaml.SequenceNode:
		for _, item := range list.Content {
			names = append(names, text(item))
		}
	default:
		names = strings.Split(text(list), ",")
	}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if localtools.ValidToolName(name) && !slices.Contains(copilotToolAliases, strings.ToLower(name)) && !slices.Contains(a.Tools, name) {
			a.Tools = append(a.Tools, name)
			// Keep larger declarations wholly unmapped and visible in the
			// header, never activate a truncated preview-only subset.
			if len(a.Tools) > 32 {
				a.Tools = nil
				return nil
			}
		}
	}
	return nil
}

// filenameID derives a bounded lowercase agent ID from the file name, so no valid
// Copilot file name is refused: punctuation runs become one hyphen.
func filenameID(filename string) string {
	base := path.Base(strings.ReplaceAll(filename, "\\", "/"))
	for _, suffix := range []string{".agent.md", ".md"} {
		if len(base) >= len(suffix) && strings.EqualFold(base[len(base)-len(suffix):], suffix) {
			base = base[:len(base)-len(suffix)]
			break
		}
	}
	var id []byte
	for i := 0; i < len(base); i++ {
		c := base[i]
		switch {
		case c >= 'A' && c <= 'Z':
			c += 'a' - 'A'
			fallthrough
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			id = append(id, c)
		case len(id) > 0 && id[len(id)-1] != '-':
			id = append(id, '-')
		}
	}
	name := strings.Trim(string(id), "-")
	if name == "" {
		return "agent"
	}
	if name[0] < 'a' || name[0] > 'z' {
		name = "agent-" + name
	}
	return strings.TrimRight(name[:min(len(name), 63)], "-")
}
