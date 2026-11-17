package localtools

import (
	"slices"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

type commandTextCheck func(*yaml.Node, bool) bool

func parseCommandArguments(node *yaml.Node, text commandTextCheck) ([]CommandArgument, error) {
	if node.Kind != yaml.SequenceNode || node.Tag != "!!seq" || len(node.Content) > 128 {
		return nil, ErrServerSelection
	}
	args := make([]CommandArgument, 0, len(node.Content))
	for _, value := range node.Content {
		if text(value, true) {
			args = append(args, CommandArgument{Literal: value.Value})
			continue
		}
		if value.Kind != yaml.MappingNode || value.Tag != "!!map" || len(value.Content) != 2 || value.Content[0].Value != "argument" || !text(value.Content[1], false) {
			return nil, ErrServerSelection
		}
		args = append(args, CommandArgument{Parameter: value.Content[1].Value})
	}
	return args, nil
}

func parseCommandParameters(node *yaml.Node, text commandTextCheck) (map[string]CommandParameter, error) {
	if node.Kind != yaml.MappingNode || node.Tag != "!!map" || len(node.Content) == 0 || len(node.Content) > 32 {
		return nil, ErrServerSelection
	}
	parameters := map[string]CommandParameter{}
	for i := 0; i < len(node.Content); i += 2 {
		name, value := node.Content[i].Value, node.Content[i+1]
		if !ValidToolName(name) || parameters[name].Type != "" || value.Kind != yaml.MappingNode || value.Tag != "!!map" {
			return nil, ErrServerSelection
		}
		parameter := CommandParameter{}
		seen := map[string]bool{}
		for j := 0; j < len(value.Content); j += 2 {
			key, field := value.Content[j].Value, value.Content[j+1]
			if seen[key] {
				return nil, ErrServerSelection
			}
			seen[key] = true
			switch key {
			case "type":
				if !text(field, false) {
					return nil, ErrServerSelection
				}
				parameter.Type = field.Value
			case "description":
				if !text(field, true) || len(field.Value) > 1024 {
					return nil, ErrServerSelection
				}
				parameter.Description = field.Value
			case "pattern":
				if !text(field, false) || len(field.Value) > 512 {
					return nil, ErrServerSelection
				}
				parameter.Pattern = field.Value
			case "enum":
				if field.Kind != yaml.SequenceNode || field.Tag != "!!seq" || len(field.Content) == 0 || len(field.Content) > 64 {
					return nil, ErrServerSelection
				}
				for _, item := range field.Content {
					if !text(item, false) {
						return nil, ErrServerSelection
					}
					parameter.Enum = append(parameter.Enum, item.Value)
				}
			case "maxLength":
				parsed, err := parseCommandInteger(field)
				if err != nil {
					return nil, err
				}
				parameter.MaxLength = int(parsed)
			case "minimum", "maximum":
				parsed, err := parseCommandInteger(field)
				if err != nil {
					return nil, err
				}
				if key == "minimum" {
					parameter.Minimum = &parsed
				} else {
					parameter.Maximum = &parsed
				}
			default:
				return nil, ErrServerSelection
			}
		}
		parameters[name] = parameter
	}
	return parameters, nil
}

func parseCommandChecks(node *yaml.Node, text commandTextCheck) ([]CommandCheck, error) {
	if node.Kind != yaml.MappingNode || node.Tag != "!!map" || len(node.Content) == 0 || len(node.Content) > 4 {
		return nil, ErrServerSelection
	}
	var checks []CommandCheck
	seen := map[string]bool{}
	for i := 0; i < len(node.Content); i += 2 {
		name, value := node.Content[i].Value, node.Content[i+1]
		if seen[name] || name != "compatibility" && name != "readiness" || value.Kind != yaml.MappingNode || value.Tag != "!!map" {
			return nil, ErrServerSelection
		}
		seen[name] = true
		check := CommandCheck{Name: name, TimeoutMilliseconds: 5000}
		fields := map[string]bool{}
		for j := 0; j < len(value.Content); j += 2 {
			key, field := value.Content[j].Value, value.Content[j+1]
			if fields[key] {
				return nil, ErrServerSelection
			}
			fields[key] = true
			switch key {
			case "args":
				if field.Kind != yaml.SequenceNode || field.Tag != "!!seq" || len(field.Content) > 32 {
					return nil, ErrServerSelection
				}
				for _, arg := range field.Content {
					if !text(arg, true) {
						return nil, ErrServerSelection
					}
					check.Args = append(check.Args, arg.Value)
				}
			case "timeout":
				if !text(field, false) {
					return nil, ErrServerSelection
				}
				duration, err := time.ParseDuration(field.Value)
				if err != nil || duration%time.Millisecond != 0 {
					return nil, ErrServerSelection
				}
				check.TimeoutMilliseconds = int(duration / time.Millisecond)
			case "when":
				if !text(field, false) || field.Value != "activation" {
					return nil, ErrServerSelection
				}
			case "expect":
				if field.Kind != yaml.MappingNode || field.Tag != "!!map" || len(field.Content) == 0 || len(field.Content) > 4 {
					return nil, ErrServerSelection
				}
				expectFields := map[string]bool{}
				for k := 0; k < len(field.Content); k += 2 {
					expectKey, expectValue := field.Content[k].Value, field.Content[k+1]
					if expectFields[expectKey] {
						return nil, ErrServerSelection
					}
					expectFields[expectKey] = true
					switch expectKey {
					case "exitCode":
						parsed, err := parseCommandInteger(expectValue)
						if err != nil {
							return nil, err
						}
						check.ExitCode = int(parsed)
					case "stdoutRegex":
						if !text(expectValue, false) {
							return nil, ErrServerSelection
						}
						check.StdoutPattern = expectValue.Value
					default:
						return nil, ErrServerSelection
					}
				}
			default:
				return nil, ErrServerSelection
			}
		}
		if !fields["expect"] {
			return nil, ErrServerSelection
		}
		checks = append(checks, check)
	}
	slices.SortFunc(checks, func(a, b CommandCheck) int { return strings.Compare(a.Name, b.Name) })
	return checks, nil
}

func parseCommandInteger(node *yaml.Node) (int64, error) {
	if node.Kind != yaml.ScalarNode || node.Tag != "!!int" {
		return 0, ErrServerSelection
	}
	value, err := strconv.ParseInt(node.Value, 10, 64)
	if err != nil {
		return 0, ErrServerSelection
	}
	return value, nil
}
