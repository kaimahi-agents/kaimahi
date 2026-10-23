package app

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
	"go.yaml.in/yaml/v3"
)

// ImportMarkdownAgent returns the saved path. New-file publication never replaces
// an existing file; replacing the original requires a separate explicit choice.
func ImportMarkdownAgent(ctx context.Context, path string, in io.Reader, out io.Writer) (string, error) {
	if !strings.HasSuffix(path, ".agent.md") {
		return "", errors.New("import requires a .agent.md file")
	}
	raw, err := mdRead(path, markdownAgentLimit)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(raw) || bytes.ContainsRune(raw, 0) || secretshapes.Match(string(raw)) != nil {
		return "", errors.New("import requires UTF-8 instructions without credential-shaped content")
	}
	if strings.TrimSpace(string(raw)) == "" {
		return "", errors.New("add instructions before importing the agent")
	}
	first := strings.TrimSuffix(strings.SplitN(string(raw), "\n", 2)[0], "\r")
	if first == "---" {
		return "", errors.New("agent already has front matter; use validate to inspect it")
	}
	name := strings.TrimSuffix(filepath.Base(path), ".agent.md")
	if !markdownAgentName.MatchString(name) {
		name = "my-agent"
	}
	scanner := bufio.NewScanner(mdImportLineReader{in})
	scanner.Buffer(make([]byte, 1024), 4096)
	ask := func(label, defaultValue string) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if _, err := fmt.Fprintf(out, "%s [%s]: ", label, defaultValue); err != nil {
			return "", err
		}
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return "", err
			}
			return "", io.EOF
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
		v := strings.TrimSpace(scanner.Text())
		if v == "" {
			v = defaultValue
		}
		return v, nil
	}
	description := strings.NewReplacer("-", " ", "_", " ").Replace(name) + " agent"
	model := ""
	destination := strings.TrimSuffix(path, ".agent.md") + ".imported.agent.md"
	replace := false
	for {
		if _, err = fmt.Fprintf(out, "\nImport agent\n  Name: %s\n  Description: %s\n  Model: %s\n  Tools: none\n  Save new file: %s\n", name, description, mdModelLabel(model), safeTerminal(destination)); err != nil {
			return "", err
		}
		choice, e := ask("[Y] Save new file  [e] Edit fields  [r] Replace original  [n] Cancel", "Y")
		if e != nil {
			return "", e
		}
		switch strings.ToLower(choice) {
		case "y", "yes", "new":
			if !strings.HasSuffix(destination, ".agent.md") {
				return "", errors.New("new file must end in .agent.md")
			}
			if _, err = os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
				return "", errors.New("new file already exists or is unavailable; choose Edit fields to select another path")
			}
		case "e", "edit":
			name, err = ask("Name", name)
			if err != nil {
				return "", err
			}
			description, err = ask("Description", description)
			if err != nil {
				return "", err
			}
			model, err = ask("Model (default uses Copilot selection)", valueOr(model, "default"))
			if err != nil {
				return "", err
			}
			if model == "default" {
				model = ""
			}
			destination, err = ask("New file", destination)
			if err != nil {
				return "", err
			}
			continue
		case "r", "replace":
			confirm, e := ask("Replace "+safeTerminal(path)+"? y/N", "N")
			if e != nil {
				return "", e
			}
			if !strings.EqualFold(confirm, "y") && !strings.EqualFold(confirm, "yes") {
				return "", errors.New("replacement cancelled; original unchanged")
			}
			destination, replace = path, true
		default:
			return "", errors.New("agent import cancelled; original unchanged")
		}
		break
	}
	header := struct {
		Name        string   `yaml:"name"`
		Description string   `yaml:"description"`
		Model       string   `yaml:"model,omitempty"`
		Tools       []string `yaml:"tools"`
	}{name, description, model, []string{}}
	front, err := yaml.Marshal(header)
	if err != nil {
		return "", err
	}
	result := append([]byte("---\n"), front...)
	result = append(result, []byte("---\n\n")...)
	result = append(result, raw...)
	if _, err = parseMarkdownAgent(path, result); err != nil {
		return "", err
	}
	current, err := mdRead(path, markdownAgentLimit)
	if err != nil || !bytes.Equal(current, raw) {
		return "", errors.New("agent changed while importing; reload and try again")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("agent is no longer a regular file")
	}
	temp, err := os.CreateTemp(filepath.Dir(destination), ".kmx-import-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(temp.Name())
	if err = temp.Chmod(info.Mode().Perm()); err != nil {
		temp.Close()
		return "", err
	}
	if _, err = temp.Write(result); err != nil {
		temp.Close()
		return "", err
	}
	if err = temp.Sync(); err != nil {
		temp.Close()
		return "", err
	}
	if err = temp.Close(); err != nil {
		return "", err
	}
	if err = ctx.Err(); err != nil {
		return "", err
	}
	current, err = mdRead(path, markdownAgentLimit)
	if err != nil || !bytes.Equal(current, raw) {
		return "", errors.New("agent changed before save; original unchanged")
	}
	if replace {
		err = os.Rename(temp.Name(), destination)
	} else {
		err = os.Link(temp.Name(), destination)
	}
	if err != nil {
		return "", err
	}
	_, err = fmt.Fprintln(out, "Imported agent:", safeTerminal(destination))
	return destination, err
}

type mdImportLineReader struct{ io.Reader }

func (r mdImportLineReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	return r.Reader.Read(p[:1])
}
