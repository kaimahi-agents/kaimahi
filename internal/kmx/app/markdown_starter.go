package app

import (
	"fmt"
	"os"
	"path/filepath"
)

func InitMarkdownAgent(root, name string) error {
	if !markdownAgentName.MatchString(name) {
		return fmt.Errorf("use a safe 1-63 character name")
	}
	agent := filepath.Join(root, ".github", "agents", name+".agent.md")
	tests := filepath.Join(root, ".github", "agent-tests", name+".agent.test.md")
	for _, path := range []string{agent, tests} {
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
	if err := write(agent, fmt.Sprintf("---\nname: %s\ndescription: Greets people by name\ntools: []\n---\n\nWhen the user gives their name, reply with exactly Hello, NAME!\nReplace NAME with their name, preserving spelling and capitalization.\n", name)); err != nil {
		return err
	}
	return write(tests, "---\nschemaVersion: 1\n---\n\n# Greeting tests\n\n```yaml agent-test\nid: greet-sam\ninput: My name is Sam\nassertions:\n  - id: greeting\n    type: regex\n    pattern: '^Hello, Sam!$'\n```\n")
}
