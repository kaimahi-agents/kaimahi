// Package imagelift plans and deploys built AgentSuite HTTP images on Kubernetes.
package imagelift

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite"
)

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,61}[a-z0-9]$|^[a-z]$`)
var keyPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,253}$`)

// Environment maps declared image inputs to one explicit, identity-pinned target.
type Environment struct {
	Inference        *InferenceBinding            `json:"inference,omitempty"`
	Instructions     string                       `json:"-"`
	Members          map[string]MemberEnvironment `json:"members,omitempty"`
	PlainHTTP        bool                         `json:"plainHTTP,omitempty"`
	APIVersion       string                       `json:"apiVersion"`
	Name             string                       `json:"name"`
	Context          string                       `json:"context"`
	ClusterUID       string                       `json:"clusterUID"`
	Namespace        string                       `json:"namespace"`
	Adapter          string                       `json:"adapter"`
	Platform         agentsuite.Platform          `json:"platform"`
	Inputs           map[string]InputBinding      `json:"inputs"`
	ImagePullSecrets []string                     `json:"imagePullSecrets,omitempty"`
}

type MemberEnvironment struct {
	Inference *InferenceBinding       `json:"inference,omitempty"`
	Name      string                  `json:"name"`
	Image     string                  `json:"image"`
	Inputs    map[string]InputBinding `json:"inputs"`
}

type InputBinding struct {
	Value     *string                  `json:"value,omitempty"`
	SecretRef *agentsuite.SecretKeyRef `json:"secretRef,omitempty"`
}

func DecodeEnvironment(data []byte) (Environment, error) {
	var env Environment
	if len(data) > 64<<10 {
		return env, errors.New("deployment environment exceeds 64 KiB")
	}
	// Reject duplicate keys before decoding: bindings must have one interpretation.
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := uniqueJSON(decoder, 0); err != nil {
		return env, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return env, errors.New("environment must contain one JSON document")
	}
	decoder = json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&env); err != nil {
		return env, err
	}
	if err := exactEnvironmentFields(data, reflect.TypeOf(env)); err != nil {
		return env, err
	}
	return env, env.Validate()
}

func exactEnvironmentFields(raw []byte, target reflect.Type) error {
	if target.Kind() == reflect.Pointer {
		return exactEnvironmentFields(raw, target.Elem())
	}
	switch target.Kind() {
	case reflect.Struct:
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return err
		}
		known := map[string]reflect.Type{}
		for i := 0; i < target.NumField(); i++ {
			field := target.Field(i)
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name != "-" {
				known[name] = field.Type
			}
		}
		for name, value := range fields {
			field, ok := known[name]
			if !ok {
				return fmt.Errorf("unknown environment field %q", name)
			}
			if err := exactEnvironmentFields(value, field); err != nil {
				return err
			}
		}
	case reflect.Map:
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return err
		}
		for _, value := range fields {
			if err := exactEnvironmentFields(value, target.Elem()); err != nil {
				return err
			}
		}
	case reflect.Slice:
		var values []json.RawMessage
		if err := json.Unmarshal(raw, &values); err != nil {
			return err
		}
		for _, value := range values {
			if err := exactEnvironmentFields(value, target.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}

func uniqueJSON(d *json.Decoder, depth int) error {
	if depth > 16 {
		return errors.New("environment JSON nesting exceeds 16")
	}
	token, err := d.Token()
	if err != nil {
		return err
	}
	if token == nil {
		return errors.New("environment null values are not allowed")
	}
	switch token {
	case json.Delim('{'):
		seen := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok {
				return errors.New("invalid JSON key")
			}
			fold := strings.ToLower(name)
			if seen[fold] {
				return errors.New("duplicate environment JSON field")
			}
			seen[fold] = true
			if err := uniqueJSON(d, depth+1); err != nil {
				return err
			}
		}
		_, err = d.Token()
	case json.Delim('['):
		for d.More() {
			if err := uniqueJSON(d, depth+1); err != nil {
				return err
			}
		}
		_, err = d.Token()
	}
	return err
}

func (e Environment) Validate() error {
	if e.APIVersion != "kaimahi.dev/lift/v1alpha1" || e.Adapter != agentsuite.ExecutionHTTPV1 {
		return errors.New("unsupported environment apiVersion or adapter")
	}
	if !namePattern.MatchString(e.Name) || !namePattern.MatchString(e.Namespace) {
		return errors.New("environment name and namespace must be Kubernetes label names")
	}
	if e.Context == "" || e.ClusterUID == "" || strings.ContainsAny(e.Context+e.ClusterUID, "\r\n\x00") {
		return errors.New("environment requires an explicit context and clusterUID")
	}
	if e.Platform.OS != "linux" || (e.Platform.Architecture != "amd64" && e.Platform.Architecture != "arm64") || e.Platform.Variant != "" {
		return errors.New("environment platform must be linux/amd64 or linux/arm64")
	}
	if len(e.Members) > 0 {
		if len(e.Inputs) != 0 || e.Inference != nil {
			return errors.New("suite environment uses member inputs, not top-level inputs")
		}
		names := map[string]bool{}
		for id, member := range e.Members {
			if id == "" || !namePattern.MatchString(member.Name) || names[member.Name] {
				return errors.New("suite members require identifiers and unique deployment names")
			}
			names[member.Name] = true
			child := e
			child.Members = nil
			child.Name = member.Name
			child.Inputs = member.Inputs
			child.Inference = member.Inference
			if err := child.Validate(); err != nil {
				return fmt.Errorf("member %s: %w", id, err)
			}
		}
	}
	if e.Inference != nil {
		if err := e.Inference.Validate(); err != nil {
			return err
		}
	}
	for name, binding := range e.Inputs {
		if name == "" || (binding.Value == nil) == (binding.SecretRef == nil) {
			return errors.New("each input requires exactly one value or secretRef")
		}
		if binding.SecretRef != nil && (!namePattern.MatchString(binding.SecretRef.Name) || !keyPattern.MatchString(binding.SecretRef.Key)) {
			return fmt.Errorf("input %q has an invalid secret reference", name)
		}
	}
	seen := map[string]bool{}
	for _, name := range e.ImagePullSecrets {
		if !namePattern.MatchString(name) || seen[name] {
			return errors.New("imagePullSecrets must contain unique Kubernetes names")
		}
		seen[name] = true
	}
	return nil
}
