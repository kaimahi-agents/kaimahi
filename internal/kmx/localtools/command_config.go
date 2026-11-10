package localtools

import (
	"path/filepath"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

type commandDefinition struct {
	name, command, directory string
	args                     []CommandArgument
	parameters               map[string]CommandParameter
	checks                   []CommandCheck
	env                      map[string]string
}

func commandConfigFromDefinition(def commandDefinition, dir string) CommandConfig {
	cfg := CommandConfig{Name: def.name, Command: def.command, Dir: dir, Parameters: copyCommandParameters(def.parameters), Checks: copyCommandChecks(def.checks), Env: map[string]string{}}
	dynamic := false
	for _, arg := range def.args {
		dynamic = dynamic || arg.Parameter != ""
	}
	if dynamic {
		cfg.ArgumentPlan = slices.Clone(def.args)
	} else {
		for _, arg := range def.args {
			cfg.Args = append(cfg.Args, arg.Literal)
		}
	}
	return cfg
}

func parseCommandConfig(raw []byte, names []string) ([]commandDefinition, error) {
	doc, _, err := settingsDocument(raw, nil)
	if err != nil {
		return nil, ErrServerSelection
	}
	root := map[string]*yaml.Node{}
	if len(doc.Content) > 0 {
		for i := 0; i < len(doc.Content[0].Content); i += 2 {
			root[doc.Content[0].Content[i].Value] = doc.Content[0].Content[i+1]
		}
	}
	commands := root["commandTools"]
	if commands == nil {
		return nil, nil
	}
	if commands.Kind != yaml.MappingNode || commands.Tag != "!!map" || len(commands.Content) > 64 {
		return nil, ErrServerSelection
	}
	wanted := map[string]bool{}
	for _, name := range names {
		wanted[name] = true
	}
	text := func(n *yaml.Node, empty bool) bool {
		return n != nil && n.Kind == yaml.ScalarNode && n.Tag == "!!str" && len(n.Value) <= 8192 && !strings.ContainsRune(n.Value, 0) && (empty || strings.TrimSpace(n.Value) != "")
	}
	seen := map[string]bool{}
	var selected []commandDefinition
	for i := 0; i < len(commands.Content); i += 2 {
		name, entry := commands.Content[i].Value, commands.Content[i+1]
		if !wanted[name] {
			continue
		}
		if !ValidToolName(name) || seen[name] || entry.Kind != yaml.MappingNode || entry.Tag != "!!map" {
			return nil, ErrServerSelection
		}
		seen[name] = true
		def := commandDefinition{name: name, directory: ".", env: map[string]string{}, parameters: map[string]CommandParameter{}}
		keys := map[string]bool{}
		for j := 0; j < len(entry.Content); j += 2 {
			key, n := entry.Content[j].Value, entry.Content[j+1]
			if keys[key] {
				return nil, ErrServerSelection
			}
			keys[key] = true
			switch key {
			case "command":
				if !text(n, false) || !filepath.IsAbs(n.Value) {
					return nil, ErrServerSelection
				}
				def.command = n.Value
			case "args":
				def.args, err = parseCommandArguments(n, text)
				if err != nil {
					return nil, err
				}
			case "parameters":
				def.parameters, err = parseCommandParameters(n, text)
				if err != nil {
					return nil, err
				}
			case "checks":
				def.checks, err = parseCommandChecks(n, text)
				if err != nil {
					return nil, err
				}
			case "workingDirectory":
				if !text(n, false) || filepath.IsAbs(n.Value) {
					return nil, ErrServerSelection
				}
				def.directory = n.Value
			case "env":
				if n.Kind != yaml.MappingNode || n.Tag != "!!map" || len(n.Content) > 128 {
					return nil, ErrServerSelection
				}
				for k := 0; k < len(n.Content); k += 2 {
					key, value := n.Content[k].Value, n.Content[k+1]
					if !envID.MatchString(key) || !text(value, false) || !envID.MatchString(value.Value) {
						return nil, ErrServerSelection
					}
					def.env[key] = value.Value
				}
			default:
				return nil, ErrServerSelection
			}
		}
		if def.command == "" {
			return nil, ErrServerSelection
		}
		selected = append(selected, def)
	}
	return selected, nil
}
