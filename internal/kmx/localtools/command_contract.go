package localtools

import (
	"encoding/json"
	"errors"
	"math"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/agent-primitives/sessions/api"
)

type CommandArgument struct {
	Literal   string `json:"literal,omitempty"`
	Parameter string `json:"parameter,omitempty"`
}

type CommandParameter struct {
	Type        string   `json:"type"`
	Description string   `json:"description,omitempty"`
	Pattern     string   `json:"pattern,omitempty"`
	Enum        []string `json:"enum,omitempty"`
	MaxLength   int      `json:"maxLength,omitempty"`
	Minimum     *int64   `json:"minimum,omitempty"`
	Maximum     *int64   `json:"maximum,omitempty"`
}

type CommandCheck struct {
	Name                string   `json:"name"`
	Args                []string `json:"args"`
	TimeoutMilliseconds int      `json:"timeoutMilliseconds"`
	ExitCode            int      `json:"exitCode"`
	StdoutPattern       string   `json:"stdoutPattern,omitempty"`
}

func copyCommandParameters(values map[string]CommandParameter) map[string]CommandParameter {
	copy := make(map[string]CommandParameter, len(values))
	for name, value := range values {
		value.Enum = slices.Clone(value.Enum)
		copy[name] = value
	}
	return copy
}

func copyCommandChecks(values []CommandCheck) []CommandCheck {
	copy := slices.Clone(values)
	for i := range copy {
		copy[i].Args = slices.Clone(copy[i].Args)
	}
	return copy
}

func commandStaticValues(cfg CommandConfig) []string {
	values := []string{cfg.Name, cfg.Command, cfg.Dir}
	values = append(values, cfg.Args...)
	for _, arg := range cfg.ArgumentPlan {
		values = append(values, arg.Literal, arg.Parameter)
	}
	for name, parameter := range cfg.Parameters {
		values = append(values, name, parameter.Type, parameter.Description, parameter.Pattern)
		values = append(values, parameter.Enum...)
	}
	for _, check := range cfg.Checks {
		values = append(values, check.Name, check.StdoutPattern)
		values = append(values, check.Args...)
	}
	return values
}

func validateCommandContract(cfg CommandConfig) error {
	if len(cfg.ArgumentPlan) == 0 {
		if len(cfg.Parameters) != 0 {
			return ErrCommand
		}
	} else {
		if len(cfg.Args) != 0 || len(cfg.ArgumentPlan) > 128 || len(cfg.Parameters) == 0 || len(cfg.Parameters) > 16 {
			return ErrCommand
		}
		used := map[string]bool{}
		for i, arg := range cfg.ArgumentPlan {
			if (arg.Literal == "") == (arg.Parameter == "") || len(arg.Literal)+len(arg.Parameter) > 4096 {
				return ErrCommand
			}
			if arg.Parameter != "" {
				if i == 0 || cfg.Parameters[arg.Parameter].Type == "" {
					return ErrCommand
				}
				used[arg.Parameter] = true
			}
		}
		for name, parameter := range cfg.Parameters {
			if !ValidToolName(name) || !used[name] || len(parameter.Description) > 1024 || strings.ContainsAny(parameter.Description, "\x00\r\n") {
				return ErrCommand
			}
			switch parameter.Type {
			case "string":
				if parameter.Minimum != nil || parameter.Maximum != nil || parameter.MaxLength < 1 || parameter.MaxLength > 4096 || (parameter.Pattern == "") == (len(parameter.Enum) == 0) || len(parameter.Enum) > 64 {
					return ErrCommand
				}
				if parameter.Pattern != "" {
					if len(parameter.Pattern) > 512 || !strings.HasPrefix(parameter.Pattern, "^") || !strings.HasSuffix(parameter.Pattern, "$") {
						return ErrCommand
					}
					if _, err := regexp.Compile(parameter.Pattern); err != nil {
						return ErrCommand
					}
				}
				for _, value := range parameter.Enum {
					if value == "" || len(value) > parameter.MaxLength || strings.ContainsRune(value, 0) {
						return ErrCommand
					}
				}
			case "path":
				if parameter.Pattern != "" || len(parameter.Enum) != 0 || parameter.Minimum != nil || parameter.Maximum != nil || parameter.MaxLength < 1 || parameter.MaxLength > 4096 {
					return ErrCommand
				}
			case "integer":
				if parameter.Pattern != "" || len(parameter.Enum) != 0 || parameter.MaxLength != 0 || parameter.Minimum == nil || parameter.Maximum == nil || *parameter.Minimum < 0 || *parameter.Maximum < *parameter.Minimum || *parameter.Maximum > math.MaxInt32 {
					return ErrCommand
				}
			default:
				return ErrCommand
			}
		}
	}
	if len(cfg.Checks) > 2 {
		return ErrCommand
	}
	prior := ""
	for _, check := range cfg.Checks {
		if check.Name <= prior || check.Name != "compatibility" && check.Name != "readiness" || len(check.Args) > 32 || check.TimeoutMilliseconds < 1000 || check.TimeoutMilliseconds > 15000 || check.ExitCode < 0 || check.ExitCode > 255 || len(check.StdoutPattern) > 512 {
			return ErrCommand
		}
		prior = check.Name
		bytes := 0
		for _, arg := range check.Args {
			bytes += len(arg)
			if strings.ContainsRune(arg, 0) || bytes > 16<<10 {
				return ErrCommand
			}
		}
		if check.StdoutPattern != "" {
			if !strings.HasPrefix(check.StdoutPattern, "^") || !strings.HasSuffix(check.StdoutPattern, "$") {
				return ErrCommand
			}
			if _, err := regexp.Compile(check.StdoutPattern); err != nil {
				return ErrCommand
			}
		}
	}
	return nil
}

func commandDefinitionFor(cfg CommandConfig) api.ToolDefinition {
	properties := map[string]any{}
	required := make([]string, 0, len(cfg.Parameters))
	for name, parameter := range cfg.Parameters {
		property := map[string]any{"type": parameter.Type}
		if parameter.Type == "path" {
			property["type"] = "string"
		}
		if parameter.Description != "" {
			property["description"] = parameter.Description
		}
		if parameter.Pattern != "" {
			property["pattern"] = parameter.Pattern
		}
		if len(parameter.Enum) > 0 {
			property["enum"] = slices.Clone(parameter.Enum)
		}
		if parameter.MaxLength > 0 {
			property["maxLength"] = parameter.MaxLength
		}
		if parameter.Minimum != nil {
			property["minimum"] = *parameter.Minimum
			property["maximum"] = *parameter.Maximum
		}
		properties[name] = property
		required = append(required, name)
	}
	slices.Sort(required)
	schema := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		schema["required"] = required
	}
	return api.ToolDefinition{Name: cfg.Name, Description: "Run the host-configured command with reviewed arguments.", InputSchema: schema}
}

// CommandDefinitions returns detached model-facing contracts without exposing
// executable, argument template, probe or local resolution details.
func CommandDefinitions(plan CommandPlan) ([]api.ToolDefinition, error) {
	definitions := make([]api.ToolDefinition, 0, len(plan.Commands))
	for _, cfg := range plan.Commands {
		if err := validateCommandContract(cfg); err != nil || !ValidToolName(cfg.Name) {
			return nil, ErrCommand
		}
		definitions = append(definitions, commandDefinitionFor(cfg))
	}
	return CopyDefinitions(definitions)
}

func commandArguments(cfg CommandConfig, values map[string]any) ([]string, error) {
	if len(cfg.ArgumentPlan) == 0 {
		if len(values) != 0 {
			return nil, ErrCommand
		}
		return slices.Clone(cfg.Args), nil
	}
	if len(values) != len(cfg.Parameters) {
		return nil, ErrCommand
	}
	resolved := make(map[string]string, len(cfg.Parameters))
	for name, parameter := range cfg.Parameters {
		value, ok := values[name]
		if !ok {
			return nil, ErrCommand
		}
		switch parameter.Type {
		case "string":
			text, ok := value.(string)
			if !ok || text == "" || len(text) > parameter.MaxLength || strings.ContainsRune(text, 0) || strings.HasPrefix(text, "-") {
				return nil, ErrCommand
			}
			if parameter.Pattern != "" {
				matched, _ := regexp.MatchString(parameter.Pattern, text)
				if !matched {
					return nil, ErrCommand
				}
			} else if !slices.Contains(parameter.Enum, text) {
				return nil, ErrCommand
			}
			resolved[name] = text
		case "path":
			text, ok := value.(string)
			if !ok || text == "" || len(text) > parameter.MaxLength || strings.ContainsAny(text, "\x00\\") || strings.HasPrefix(text, "-") || !filepath.IsLocal(text) || filepath.Clean(text) != text {
				return nil, ErrCommand
			}
			resolved[name] = text
		case "integer":
			var integer int64
			switch value := value.(type) {
			case float64:
				if value != math.Trunc(value) || value < math.MinInt64 || value > math.MaxInt64 {
					return nil, ErrCommand
				}
				integer = int64(value)
			case json.Number:
				var err error
				integer, err = value.Int64()
				if err != nil {
					return nil, ErrCommand
				}
			default:
				return nil, ErrCommand
			}
			if integer < *parameter.Minimum || integer > *parameter.Maximum {
				return nil, ErrCommand
			}
			resolved[name] = strconv.FormatInt(integer, 10)
		}
	}
	args := make([]string, 0, len(cfg.ArgumentPlan))
	for _, entry := range cfg.ArgumentPlan {
		if entry.Parameter != "" {
			args = append(args, resolved[entry.Parameter])
		} else {
			args = append(args, entry.Literal)
		}
	}
	return args, nil
}

func commandCheckTimeout(check CommandCheck) time.Duration {
	return time.Duration(check.TimeoutMilliseconds) * time.Millisecond
}

func checkCommandObservation(check CommandCheck, result map[string]any) error {
	exit, ok := result["exitCode"].(int)
	if !ok {
		if number, numberOK := result["exitCode"].(float64); numberOK && number == math.Trunc(number) {
			exit, ok = int(number), true
		}
	}
	if !ok || exit != check.ExitCode {
		return errors.New("command check exit code did not match")
	}
	if check.StdoutPattern != "" {
		stdout, _ := result["stdout"].(string)
		matched, err := regexp.MatchString(check.StdoutPattern, strings.TrimSuffix(stdout, "\n"))
		if err != nil || !matched {
			return errors.New("command check output did not match")
		}
	}
	return nil
}
