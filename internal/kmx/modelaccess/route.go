// Package modelaccess discovers model endpoints and resolves explicit model routes.
// Route selection requires exact provider and model matches; it does not
// authorize agent execution.
package modelaccess

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
)

const CopilotEndpoint = "https://api.githubcopilot.com"

// Route is the non-secret selection consumed by model callers. Auth names a
// credential facility, never a credential value. Selection grants no execution.
type Route struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Endpoint string `json:"endpoint,omitempty"`
	Auth     string `json:"auth"`
}

var modelName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,255}$`)

func (r Route) Validate() error {
	if !modelName.MatchString(r.Model) {
		return errors.New("invalid model identifier")
	}
	if r.Provider == "fake" && r.Endpoint == "" && r.Auth == "none" {
		return nil
	}
	if r.Auth != "none" && r.Auth != "copilot" {
		return errors.New("unknown model authentication source")
	}
	if r.Provider != "ollama" && r.Provider != "foundry-local" && r.Provider != "compatible" && r.Provider != "copilot" {
		return errors.New("unknown model provider")
	}
	u, err := url.Parse(r.Endpoint)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("model endpoint must be an HTTP URL without credentials, query or fragment")
	}
	if r.Provider == "copilot" && (r.Auth != "copilot" || r.Endpoint != CopilotEndpoint) {
		return errors.New("invalid Copilot route")
	}
	if r.Provider != "copilot" && r.Auth != "none" {
		return errors.New("authentication source does not match provider")
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
