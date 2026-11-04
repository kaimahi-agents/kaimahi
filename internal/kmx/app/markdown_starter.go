package app

import (
	"fmt"
	"os"
	"path/filepath"

	"go.yaml.in/yaml/v3"
)

func InitMarkdownAgent(root, name string) error {
	return createMarkdownPair(root, name, "", "", true)
}

// CreateMarkdownDraft authors custom instructions with an empty test companion.
func CreateMarkdownDraft(root, name, instructions, model string) error {
	return createMarkdownPair(root, name, instructions, model, false)
}

func createMarkdownPair(root, name, instructions, model string, starter bool) error {
	if !markdownAgentName.MatchString(name) {
		return fmt.Errorf("use a safe 1-63 character name")
	}
	agent := filepath.Join(root, name, name+".agent.md")
	tests := MarkdownTestsPath(agent)
	tools := MarkdownToolsPath(agent)
	body := fmt.Sprintf("---\nname: %s\ndescription: Greets people by name\ntools: []\n---\n\nWhen the user gives their name, reply with exactly Hello, NAME!\nReplace NAME with their name, preserving spelling and capitalization.\n", name)
	testBody := "---\nschemaVersion: 1\n---\n\n# Greeting tests\n\n```yaml agent-test\nid: greet-sam\ninput: My name is Sam\nassertions:\n  - id: greeting\n    type: regex\n    pattern: '^Hello, Sam!$'\n```\n"
	if !starter {
		header, err := yaml.Marshal(struct {
			Name        string   `yaml:"name"`
			Description string   `yaml:"description"`
			Model       string   `yaml:"model,omitempty"`
			Tools       []string `yaml:"tools"`
		}{name, name + " agent", model, []string{}})
		if err != nil {
			return err
		}
		body = "---\n" + string(header) + "---\n\n" + instructions + "\n"
		testBody = "---\nschemaVersion: 1\n---\n\n# Tests\n\nAdd reviewed yaml agent-test blocks here before testing.\n"
	}
	if _, err := parseMarkdownAgent(agent, []byte(body)); err != nil {
		return err
	}
	for _, path := range []string{agent, tests, tools} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			return fmt.Errorf("refusing to overwrite %s", path)
		}
	}
	for _, path := range []string{agent, tests} {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return err
		}
	}
	write := func(path, body string) error {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if err != nil {
			return err
		}
		_, err = f.WriteString(body)
		closeErr := f.Close()
		if err != nil {
			return err
		}
		return closeErr
	}
	if err := write(agent, body); err != nil {
		return err
	}
	if err := write(tests, testBody); err != nil {
		return err
	}
	return write(tools, emptyMarkdownTools)
}
