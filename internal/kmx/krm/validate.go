package krm

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

var (
	nameRE      = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	objectRE    = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)
	secretKeyRE = regexp.MustCompile(`^[-._a-zA-Z0-9]+$`)
	headerRE    = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")
)

// credentialHeaders carry credentials; tools reference them via credentials.
var credentialHeaders = map[string]bool{"authorization": true, "proxy-authorization": true, "cookie": true}

var httpMethods = map[string]bool{
	http.MethodGet: true, http.MethodPost: true, http.MethodPut: true, http.MethodPatch: true, http.MethodDelete: true,
}

func (p *Provider) validate() error {
	s := p.Spec
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	if err := ValidateName(p.Metadata.Name); err != nil {
		add("metadata.name: %w", err)
	}
	if strings.TrimSpace(s.Model) == "" || HasControl(s.Model) {
		add("spec.model is required and must be plain text")
	}
	switch s.Type {
	case ProviderOpenAI:
		if s.Azure != nil {
			add("spec.azure is only valid for type %q", ProviderAzureOpenAI)
		}
		if s.OpenAI != nil && s.OpenAI.BaseURL != "" {
			if err := validateEndpoint(s.OpenAI.BaseURL, false); err != nil {
				add("spec.openAI.baseURL: %w", err)
			}
		}
	case ProviderAnthropic:
		if s.OpenAI != nil || s.Azure != nil {
			add("spec.openAI and spec.azure are not valid for type %q", ProviderAnthropic)
		}
	case ProviderAzureOpenAI:
		if s.OpenAI != nil {
			add("spec.openAI is only valid for type %q", ProviderOpenAI)
		}
		if s.Azure == nil {
			add("spec.azure is required for type %q", ProviderAzureOpenAI)
		} else {
			if err := validateEndpoint(s.Azure.Endpoint, true); err != nil {
				add("spec.azure.endpoint: %w", err)
			}
			if s.Azure.APIVersion != "" && (strings.TrimSpace(s.Azure.APIVersion) == "" || HasControl(s.Azure.APIVersion)) {
				add("spec.azure.apiVersion must be plain text when stated")
			}
		}
	default:
		add("spec.type must be one of %q, %q or %q (found %q)", ProviderOpenAI, ProviderAnthropic, ProviderAzureOpenAI, s.Type)
	}
	if err := s.Credentials.Secret.validate(); err != nil {
		add("spec.credentials.secret: %w", err)
	}
	if err := s.RateLimits.validate(); err != nil {
		add("spec.rateLimits: %w", err)
	}
	return errors.Join(errs...)
}

func (t *Tool) validate() error {
	s := t.Spec
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	if err := ValidateName(t.Metadata.Name); err != nil {
		add("metadata.name: %w", err)
	}
	if strings.TrimSpace(s.Description) == "" {
		add("spec.description is required: the model reads it to decide when to call the tool")
	}
	if s.Parameters != nil {
		if err := validateParameters(s.Parameters); err != nil {
			add("spec.parameters: %w", err)
		}
	}
	if s.HTTP == nil {
		add("spec.http is required: HTTP is the only supported tool implementation")
		return errors.Join(errs...)
	}
	h := s.HTTP
	if !httpMethods[h.Method] {
		add("spec.http.method must be one of GET, POST, PUT, PATCH or DELETE (found %q)", h.Method)
	}
	if err := validateToolURL(h.URL); err != nil {
		add("spec.http.url: %w", err)
	}
	if h.Timeout != "" {
		if d, err := time.ParseDuration(h.Timeout); err != nil || d <= 0 {
			add("spec.http.timeout must be a positive duration such as 30s (found %q)", h.Timeout)
		}
	}
	for _, name := range sortedKeys(h.Headers) {
		switch {
		case !headerRE.MatchString(name):
			add("spec.http.headers: %q is not a valid header name", name)
		case credentialHeaders[strings.ToLower(name)]:
			add("spec.http.headers.%s carries credentials; use spec.http.credentials instead", name)
		case HasControl(h.Headers[name]):
			add("spec.http.headers.%s must not contain control characters", name)
		}
	}
	if h.Credentials != nil {
		if err := h.Credentials.Secret.validate(); err != nil {
			add("spec.http.credentials.secret: %w", err)
		}
	}
	return errors.Join(errs...)
}

func (a *Agent) validate() error {
	s := a.Spec
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	if err := ValidateName(a.Metadata.Name); err != nil {
		add("metadata.name: %w", err)
	}
	if strings.TrimSpace(s.Instructions) == "" {
		add("spec.instructions is required")
	}
	if err := ValidateName(s.Provider); err != nil {
		add("spec.provider: %w", err)
	}
	if err := validateNameList(s.Tools); err != nil {
		add("spec.tools: %w", err)
	}
	if err := validateNameList(s.AllowedAgents); err != nil {
		add("spec.allowedAgents: %w", err)
	}
	for _, name := range s.AllowedAgents {
		if name == a.Metadata.Name {
			add("spec.allowedAgents: an Agent cannot delegate to itself")
		}
	}
	return errors.Join(errs...)
}

func (r SecretKeyRef) validate() error {
	var errs []error
	if !objectRE.MatchString(r.Name) || len(r.Name) > 253 {
		errs = append(errs, fmt.Errorf("name %q is not a Kubernetes Secret name", r.Name))
	}
	if len(r.Key) > 253 || !secretKeyRE.MatchString(r.Key) || r.Key == "." || strings.HasPrefix(r.Key, "..") {
		errs = append(errs, errors.New("key must be 1-253 letters, digits, dashes, underscores or dots, and must not be '.' or start with '..'"))
	}
	return errors.Join(errs...)
}

func (l *RateLimits) validate() error {
	if l == nil {
		return nil
	}
	if l.RequestsPerMinute == nil && l.TokensPerMinute == nil {
		return errors.New("state at least one limit or omit rateLimits")
	}
	if l.RequestsPerMinute != nil && *l.RequestsPerMinute <= 0 {
		return errors.New("requestsPerMinute must be positive")
	}
	if l.TokensPerMinute != nil && *l.TokensPerMinute <= 0 {
		return errors.New("tokensPerMinute must be positive")
	}
	return nil
}

// ValidateName checks a resource name: an RFC 1123 label, because every
// resource renders to a Kubernetes object.
func ValidateName(name string) error {
	switch {
	case name == "":
		return errors.New("a name is required")
	case len(name) > 63 || !nameRE.MatchString(name):
		return fmt.Errorf("%q is not a valid name: up to 63 lowercase letters, digits and dashes, starting and ending with a letter or digit", name)
	}
	return nil
}

func validateNameList(names []string) error {
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if err := ValidateName(name); err != nil {
			return err
		}
		if seen[name] {
			return fmt.Errorf("%q is listed more than once", name)
		}
		seen[name] = true
	}
	return nil
}

// validateEndpoint never echoes the URL: it may carry credentials.
func validateEndpoint(raw string, azure bool) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" ||
		u.User != nil || u.RawQuery != "" || u.ForceQuery || strings.Contains(raw, "#") {
		return errors.New("must be an absolute HTTP(S) URL with a host and no credentials, query or fragment")
	}
	if azure {
		if u.Scheme != "https" {
			return errors.New("Azure OpenAI requires HTTPS")
		}
		if strings.TrimRight(u.Path, "/") != "" {
			return errors.New("must be the Azure resource root without a path")
		}
	}
	return nil
}

// validateToolURL allows a query, which many HTTP APIs need, but never
// userinfo or a fragment. It never echoes the URL.
func validateToolURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" ||
		u.User != nil || strings.Contains(raw, "#") {
		return errors.New("must be an absolute HTTP(S) URL with a host and no credentials or fragment")
	}
	return nil
}

// validateParameters requires a JSON Schema describing an input object.
func validateParameters(params map[string]any) error {
	if params["type"] != "object" {
		return errors.New(`the root schema must have type: object`)
	}
	data, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("must be JSON-compatible: %w", err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return err
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.AssertFormat()
	const location = "kmx:///tool-parameters.json"
	if err := compiler.AddResource(location, doc); err != nil {
		return err
	}
	if _, err := compiler.Compile(location); err != nil {
		return fmt.Errorf("is not a valid JSON Schema: %w", err)
	}
	return nil
}

// HasControl reports whether s contains a control character.
func HasControl(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return unicode.IsControl(r) }) >= 0
}
