package app

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
)

type MarkdownPreferences struct {
	SchemaVersion int `json:"schemaVersion"`
	LocalAgents   struct {
		CopilotChoice   string   `json:"copilotChoice"`
		DefaultProvider string   `json:"defaultProvider,omitempty"`
		Editor          []string `json:"editor,omitempty"`
	} `json:"localAgents"`
}

func mdPrivateDir(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	for p := abs; ; p = filepath.Dir(p) {
		info, e := os.Lstat(p)
		if e == nil && info.Mode()&os.ModeSymlink != 0 {
			return errors.New("KMX state must not use symlink directories")
		}
		if e != nil && !errors.Is(e, os.ErrNotExist) {
			return e
		}
		if p == filepath.Dir(p) {
			break
		}
	}
	if err = os.MkdirAll(abs, 0700); err != nil {
		return err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return err
	}
	if !mdOwned(info, false) || info.Mode().Perm()&0022 != 0 {
		return errors.New("KMX state directory must be owned by you and not writable by other users")
	}
	return nil
}
func mdStateRead(path string, v any) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !mdOwned(info, true) || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return errors.New("unsafe local state file; expected owner-only regular file")
	}
	b, err := mdRead(path, 256<<10)
	if err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err = d.Decode(v); err != nil {
		return errors.New("invalid local state; repair or remove the selected state file")
	}
	var extra any
	if !errors.Is(d.Decode(&extra), io.EOF) {
		return errors.New("invalid trailing local state")
	}
	return nil
}
func mdStateUpdate(path string, update func() ([]byte, error)) error {
	if err := mdPrivateDir(filepath.Dir(path)); err != nil {
		return err
	}
	lock, err := openAgentLease(path+".lock", true)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = mdLock(lock); err != nil {
		return errors.New("local state busy; try again")
	}
	defer mdUnlock(lock)
	b, err := update()
	if err != nil {
		return err
	}
	if len(b) > 256<<10 {
		return errors.New("local state exceeds limit")
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".md-state-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func mdPreferencesPath() (string, error) {
	dir, err := config.StateDir()
	return filepath.Join(dir, "preferences.json"), err
}
func ReadMarkdownPreferences() (MarkdownPreferences, error) {
	p := MarkdownPreferences{SchemaVersion: 1}
	path, err := mdPreferencesPath()
	if err != nil {
		return p, err
	}
	err = mdStateRead(path, &p)
	if errors.Is(err, os.ErrNotExist) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	if p.SchemaVersion != 1 || (p.LocalAgents.CopilotChoice != "accepted" && p.LocalAgents.CopilotChoice != "declined" && p.LocalAgents.CopilotChoice != "") || (p.LocalAgents.DefaultProvider != "" && p.LocalAgents.DefaultProvider != "copilot") {
		return p, errors.New("unsupported local preferences; repair preferences.json")
	}
	return p, nil
}
func SetMarkdownProvider(choice string) error {
	if choice != "accepted" && choice != "declined" && choice != "" {
		return errors.New("invalid provider choice")
	}
	path, err := mdPreferencesPath()
	if err != nil {
		return err
	}
	return mdStateUpdate(path, func() ([]byte, error) {
		p, err := ReadMarkdownPreferences()
		if err != nil {
			return nil, err
		}
		p.LocalAgents.CopilotChoice = choice
		p.LocalAgents.DefaultProvider = ""
		if choice == "accepted" {
			p.LocalAgents.DefaultProvider = "copilot"
		}
		return json.MarshalIndent(p, "", "  ")
	})
}

// SetupMarkdownProvider performs local PATH detection only; login belongs to execution.
func SetupMarkdownProvider(in io.Reader, out io.Writer, force bool) (bool, error) {
	p, err := ReadMarkdownPreferences()
	if err != nil {
		return false, err
	}
	if !force && p.LocalAgents.CopilotChoice != "" {
		return p.LocalAgents.DefaultProvider == "copilot", nil
	}
	if detectCopilotCLI() == "" {
		fmt.Fprintln(out, "Copilot CLI not found. Install: https://docs.github.com/en/copilot/how-tos/copilot-cli/cli-getting-started")
		return false, nil
	}
	fmt.Fprint(out, "Copilot CLI found. Use it by default for local agents? [Y/n] ")
	scanner := bufio.NewScanner(in)
	for scanner.Scan() {
		v := strings.ToLower(strings.TrimSpace(scanner.Text()))
		choice := "accepted"
		switch v {
		case "", "y", "yes":
		case "n", "no":
			choice = "declined"
		default:
			fmt.Fprint(out, "Please enter yes or no [Y/n]: ")
			continue
		}
		if err = SetMarkdownProvider(choice); err != nil {
			return false, fmt.Errorf("could not save local inference preference: %w", err)
		}
		path, _ := mdPreferencesPath()
		fmt.Fprintln(out, "Saved local inference preference:", path)
		return choice == "accepted", nil
	}
	return false, scanner.Err()
}
