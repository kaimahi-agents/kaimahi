package kmx

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
)

// EnvironmentSpec is the caller's desired logical environment. It deliberately
// contains no context, runtime, infrastructure, inference, or implementation
// selector. Those choices belong to the AgentEnvironment implementation.
type EnvironmentSpec struct {
	Name string `json:"name"`
}

func (s EnvironmentSpec) Validate() error {
	return validateIdentity("environment name", s.Name)
}

type environmentSpecJSON EnvironmentSpec

func (s EnvironmentSpec) MarshalJSON() ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(environmentSpecJSON(s))
}

func (s *EnvironmentSpec) UnmarshalJSON(data []byte) error {
	var decoded environmentSpecJSON
	if err := decodeEnvironmentJSON(data, &decoded, "name"); err != nil {
		return err
	}
	value := EnvironmentSpec(decoded)
	if err := value.Validate(); err != nil {
		return err
	}
	*s = value
	return nil
}

// EnvironmentRef is the durable identity of one logical environment. It is
// distinct from a TargetRef: targets identify exact placement destinations,
// while an environment can later resolve to a replacement target.
type EnvironmentRef struct {
	ID EnvironmentID `json:"id"`
}

func (r EnvironmentRef) Validate() error {
	return validateIdentity("environment ID", string(r.ID))
}

type environmentRefJSON EnvironmentRef

func (r EnvironmentRef) MarshalJSON() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(environmentRefJSON(r))
}

func (r *EnvironmentRef) UnmarshalJSON(data []byte) error {
	var decoded environmentRefJSON
	if err := decodeEnvironmentJSON(data, &decoded, "id"); err != nil {
		return err
	}
	value := EnvironmentRef(decoded)
	if err := value.Validate(); err != nil {
		return err
	}
	*r = value
	return nil
}

// Environment is the established logical environment returned by Up. It does
// not claim that any particular suite is deployed or has executed there.
type Environment struct {
	Ref  EnvironmentRef  `json:"ref"`
	Spec EnvironmentSpec `json:"spec"`
}

func (e Environment) Validate() error {
	if err := e.Ref.Validate(); err != nil {
		return err
	}
	return e.Spec.Validate()
}

type environmentJSON Environment

func (e Environment) MarshalJSON() ([]byte, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(environmentJSON(e))
}

func (e *Environment) UnmarshalJSON(data []byte) error {
	var decoded environmentJSON
	if err := decodeEnvironmentJSON(data, &decoded, "ref", "spec"); err != nil {
		return err
	}
	value := Environment(decoded)
	if err := value.Validate(); err != nil {
		return err
	}
	*e = value
	return nil
}

func decodeEnvironmentJSON(data []byte, value any, fields ...string) error {
	if err := validateJSONInput(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return fmt.Errorf("environment JSON must be an object")
	}
	allowed := make(map[string]bool, len(fields))
	for _, field := range fields {
		allowed[field] = true
	}
	seen := make(map[string]bool, len(fields))
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		name, ok := token.(string)
		if !ok || !allowed[name] {
			return fmt.Errorf("environment JSON contains an unknown field")
		}
		if seen[name] {
			return fmt.Errorf("environment JSON contains a duplicate field")
		}
		seen[name] = true
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return err
		}
	}
	if _, err := decoder.Token(); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return fmt.Errorf("environment JSON must contain exactly one value")
		}
		return err
	}
	return json.Unmarshal(data, value)
}

// AgentEnvironment establishes a logical environment. The first contract is
// intentionally limited to setup; inspection, teardown, recovery, and exact
// target resolution require production semantics before they become methods.
type AgentEnvironment interface {
	Up(context.Context, EnvironmentSpec) (Environment, error)
}
