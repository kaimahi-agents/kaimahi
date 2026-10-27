package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/spf13/cobra"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
)

const starterPrompt = "Greet the user by name.\nReply with a short, friendly greeting.\n"
const starterTest = "```yaml agent-test\nid: greeting\ninput: Ada\nassertions:\n  - id: greeting-text\n    type: contains\n    value: Ada\n```\n"

func newFileCreateCommand(state *commandState) *cobra.Command {
	var directory, description string
	cmd := &cobra.Command{
		Use: "create NAME", Short: "Write an agent file and one example test",
		Long: "Write NAME.agent.md and NAME.agent.test.md in the current folder.\n" +
			"The agent file has GitHub frontmatter and a starter prompt.\n" +
			"Nothing is sent to a model. Existing files are never overwritten.\n" +
			"In .github/agents, tests go in sibling .github/agent-tests.",
		Example: "  kmx create greeter\n  kmx create greeter --path .github/agents\n  kmx create greeter --description \"A friendly greeting agent\"",
		Args: func(cmd *cobra.Command, args []string) error {
			if err := usageArgs(1, 1, "kmx create NAME [--path DIR] [--description TEXT]")(cmd, args); err != nil {
				return fileUsageError(err)
			}
			if state.contextFlag != "" {
				return fileUsageError(fmt.Errorf("agent-file creation refuses --context"))
			}
			if cmd.Flags().Changed("container-engine") {
				return fileUsageError(fmt.Errorf("agent-file creation refuses --container-engine"))
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			agent, tests, err := createAgentFiles(cmd.Context(), directory, args[0], description, writeNewAgentFile)
			if err != nil {
				return fileUsageError(err)
			}
			operand := agent
			if strings.HasPrefix(operand, "-") {
				operand = "./" + operand
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Wrote %s and %s\n\nNext:\n  %s run %s \"hi\"\n  %s test %s\n",
				agent, tests, quoteShell(state.executable()), quoteShell(operand), quoteShell(state.executable()), quoteShell(operand))
			return fileUsageError(err)
		},
	}
	cmd.Flags().StringVar(&directory, "path", ".", "Folder for the agent file")
	cmd.Flags().StringVar(&description, "description", "A friendly greeting agent", "One-line frontmatter description")
	_ = cmd.MarkFlagDirname("path")
	cmd.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return fileUsageError(flagErrorWithHelp(cmd, state, err))
	})
	return cmd
}

// Creation is deliberately not a pair transaction. Preflight avoids ordinary
// partial pairs; exclusive opens protect against races. Once visible, even an
// incomplete file belongs to the caller and is retained with a precise error.
func createAgentFiles(ctx context.Context, directory, name, description string, write func(string, string) (bool, error)) (agent, tests string, err error) {
	if strings.TrimSpace(name) == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return "", "", fmt.Errorf("NAME must be a nonempty filename stem, without path separators or control characters")
	}
	if strings.TrimSpace(directory) == "" || strings.IndexFunc(directory, unicode.IsControl) >= 0 {
		return "", "", fmt.Errorf("--path requires a nonempty directory without control characters")
	}
	if strings.IndexFunc(description, unicode.IsControl) >= 0 {
		return "", "", fmt.Errorf("--description must be one line without control characters")
	}
	// JSON double-quoted scalars are also YAML scalars, without interpolation.
	yamlName, _ := json.Marshal(name)
	yamlDescription, _ := json.Marshal(description)
	contents := []string{
		fmt.Sprintf("---\nname: %s\ndescription: %s\n---\n\n%s", yamlName, yamlDescription, starterPrompt),
		fmt.Sprintf("---\nschemaVersion: 1\n---\n\n# %s tests\n\n%s", name, starterTest),
	}
	// Use the reader's existing content policy before paths can echo an invalid
	// name or any source bytes become visible. Parser diagnostics may carry the
	// filename, so expose only a fixed refusal here.
	if _, err := agentfile.Parse("created.agent.md", []byte(contents[0])); err != nil {
		return "", "", fmt.Errorf("cannot create agent file: invalid name or description")
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return "", "", fmt.Errorf("cannot resolve --path: %q", err.Error())
	}
	testsDirectory := directory
	if filepath.Base(absolute) == "agents" && filepath.Base(filepath.Dir(absolute)) == ".github" {
		testsDirectory = filepath.Join(directory, "..", "agent-tests")
	}
	agent = filepath.Join(directory, name+".agent.md")
	tests = filepath.Join(testsDirectory, name+".agent.test.md")
	for _, path := range []string{agent, tests} {
		if _, err := os.Lstat(path); err == nil {
			return "", "", fmt.Errorf("%q already exists; refusing to overwrite", path)
		} else if !os.IsNotExist(err) {
			return "", "", fmt.Errorf("cannot check %q: %q", path, err.Error())
		}
	}
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	for _, dir := range []string{directory, testsDirectory} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return "", "", fmt.Errorf("cannot create directory %q: %q", dir, err.Error())
		}
	}
	var created []string
	for i, path := range []string{agent, tests} {
		err := ctx.Err()
		if err == nil {
			var visible bool
			visible, err = write(path, contents[i])
			if visible {
				created = append(created, fmt.Sprintf("%q", path))
			}
		}
		if err != nil {
			detail := fmt.Sprintf("cannot write %q: %q", path, err.Error())
			if os.IsExist(err) {
				detail = fmt.Sprintf("%q already exists; refusing to overwrite", path)
			}
			if len(created) > 0 {
				detail += "; created files retained (may be incomplete): " + strings.Join(created, ", ")
			}
			return "", "", errors.New(detail)
		}
	}
	return agent, tests, nil
}

func writeNewAgentFile(path, content string) (bool, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return false, err
	}
	return true, finishAgentFile(file, content)
}

type agentCreationFile interface {
	WriteString(string) (int, error)
	Sync() error
	Close() error
}

func finishAgentFile(file agentCreationFile, content string) error {
	n, err := file.WriteString(content)
	if err == nil && n != len(content) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = file.Sync()
	}
	return errors.Join(err, file.Close())
}
