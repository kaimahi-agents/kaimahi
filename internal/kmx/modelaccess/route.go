// Package modelaccess discovers model endpoints, resolves explicit model routes,
// matches declared capabilities, and owns bounded endpoint transports. Route
// selection requires exact provider and model matches; selection alone does not
// authorize agent execution or establish evaluation evidence.
package modelaccess

import (
	"errors"
	"net"
	"net/url"
	"regexp"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
)

const CopilotEndpoint = "https://api.githubcopilot.com"

// MaxEndpointBytes bounds route metadata before it enters retained state.
const MaxEndpointBytes = 4096

// CopilotDefaultModel is the file-preview choice when no runtime or authored model is supplied.
const CopilotDefaultModel = "gpt-5.6-terra"

// Route is the non-secret selection consumed by model callers. Auth names a
// credential facility, never a credential value. Selection grants no execution.
type Route struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Endpoint string `json:"endpoint,omitempty"`
	Auth     string `json:"auth"`
	KeyEnv   string `json:"keyEnv,omitempty"`
}

var modelName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,255}$`)
var environmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

func (r Route) Validate() error {
	if len(r.Endpoint) > MaxEndpointBytes {
		return errors.New("model endpoint exceeds 4096 bytes")
	}
	if !modelName.MatchString(r.Model) {
		return errors.New("invalid model identifier")
	}
	if secretshapes.Match(r.Model) != nil || secretshapes.Match(r.Endpoint) != nil || secretshapes.Match(r.KeyEnv) != nil {
		return errors.New("model route contains credential-shaped content")
	}
	if r.KeyEnv != "" && !environmentName.MatchString(r.KeyEnv) {
		return errors.New("model key must name an environment variable")
	}
	if (r.Provider == "fake" || r.Provider == "copilot") && r.KeyEnv != "" {
		return errors.New("selected provider owns its authentication")
	}
	if r.Provider == "fake" && r.Endpoint == "" && r.Auth == "none" {
		return nil
	}
	if r.Auth != "none" && r.Auth != "copilot" && r.Auth != "env" {
		return errors.New("unknown model authentication source")
	}
	if r.Provider != "ollama" && r.Provider != "foundry-local" && r.Provider != "compatible" && r.Provider != "copilot" && r.Provider != "openai" && r.Provider != "anthropic" {
		return errors.New("unknown model provider")
	}
	u, err := url.Parse(r.Endpoint)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("model endpoint must be an HTTP URL without credentials, query or fragment")
	}
	if r.Provider == "copilot" && (r.Auth != "copilot" || r.Endpoint != CopilotEndpoint) {
		return errors.New("invalid Copilot route")
	}
	if r.Provider != "copilot" && (r.Auth != "none" && r.Auth != "env" || (r.Auth == "env") != (r.KeyEnv != "")) {
		return errors.New("authentication source does not match provider")
	}
	return nil
}

// ValidateEndpoint checks transport policy without reading credentials. Keeping
// this separate from record syntax lets older records remain visible/stoppable.
func (r Route) ValidateEndpoint() error {
	if err := r.Validate(); err != nil {
		return err
	}
	if r.Provider == "fake" || r.Provider == "copilot" {
		return errors.New("provider owns its transport")
	}
	u, _ := url.Parse(r.Endpoint)
	if u.Scheme != "https" && r.KeyEnv != "" {
		return errors.New("credentialed model endpoints require HTTPS")
	}
	if u.Scheme != "https" && !net.ParseIP(u.Hostname()).IsLoopback() && u.Hostname() != "localhost" {
		return errors.New("model endpoint requires HTTPS or keyless loopback HTTP")
	}
	return nil
}

// Select resolves an exact explicit provider/model pair; absence is never fallback.
func Select(routes []Route, provider, model string) (Route, error) {
	for _, r := range routes {
		if r.Provider == provider && r.Model == model {
			if err := r.Validate(); err != nil {
				return Route{}, err
			}
			return r, nil
		}
	}
	return Route{}, errors.New("selected model is unavailable; choose an installed model explicitly")
}
func cleanBase(endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Scheme != "http" || (u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1" && u.Hostname() != "::1") || (u.Path != "" && u.Path != "/" && u.Path != "/v1" && u.Path != "/v1/" && u.Path != "/openai/status") {
		return "", errors.New("runtime did not report a usable loopback HTTP endpoint")
	}
	if u.Port() == "0" {
		return "", errors.New("runtime endpoint is not listening")
	}
	u.Path = ""
	u.RawPath = ""
	return strings.TrimRight(u.String(), "/"), nil
}
