package localpreview

import (
	"errors"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// FakeModel is the exact Model.Preferred value that selects kmx's offline,
// deterministic orientation route. It names no general-purpose model.
const FakeModel = "fake"

// fakeBaseURL identifies the fake route in judge configuration. It is served
// only by an in-memory transport and is never resolved or dialed.
const fakeBaseURL = "https://fake.kmx.invalid/v1"

// Endpoint is a non-secret OpenAI-compatible route selection. KeyEnv names an
// environment variable holding the bearer credential; the value is read into
// memory only and never written to files, journals, results or errors.
type Endpoint struct {
	BaseURL string
	KeyEnv  string
}

var keyEnvName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// route is a resolved model route. key is transient and never serialized.
type route struct {
	fake      bool
	model     string
	baseURL   string
	keyEnv    string
	key       string
	transport http.RoundTripper
}

// Kind is "fake" or "compatible".
func (r route) kind() string {
	if r.fake {
		return "fake"
	}
	return "compatible"
}

// resolveRoute validates an explicit selection without any network or
// filesystem effect. model is the agent's exact Model.Preferred; there is no
// fallback to another model or route.
func resolveRoute(model string, endpoint *Endpoint, transport http.RoundTripper, role string) (route, error) {
	if model == FakeModel {
		if endpoint != nil {
			return route{}, refuse(RefusedModelUnconfigured, role+" selects the offline fake model but an endpoint was also configured",
				"remove the endpoint, or set Model.Preferred to the endpoint's exact model")
		}
		return route{fake: true, model: model, baseURL: fakeBaseURL, transport: fakeTransport{}}, nil
	}
	if endpoint == nil || strings.TrimSpace(endpoint.BaseURL) == "" {
		return route{}, refuse(RefusedModelUnconfigured, role+" selects a model with no configured endpoint",
			"configure an OpenAI-compatible endpoint for Model.Preferred, or set Model.Preferred to fake for offline orientation")
	}
	base, err := agentruntime.CanonicalOpenAIBaseURL(endpoint.BaseURL)
	if errors.Is(err, agentruntime.ErrOpenAIEndpointInsecure) {
		return route{}, refuse(RefusedEndpoint, role+" endpoint is plaintext HTTP outside literal loopback",
			"use HTTPS, or keyless HTTP at 127.0.0.1 or ::1")
	}
	if err != nil {
		return route{}, refuse(RefusedEndpoint, role+" endpoint is not an absolute HTTP(S) base URL without credentials, query or fragment",
			"set the endpoint base URL, for example http://127.0.0.1:11434/v1")
	}
	r := route{model: model, baseURL: base, keyEnv: endpoint.KeyEnv, transport: transport}
	if endpoint.KeyEnv == "" {
		return r, nil
	}
	if !keyEnvName.MatchString(endpoint.KeyEnv) {
		return route{}, refuse(RefusedCredential, role+" credential must be named by a valid environment variable",
			"name an environment variable, not a credential value")
	}
	if !strings.HasPrefix(base, "https://") {
		return route{}, refuse(RefusedCredential, role+" credentials require an HTTPS endpoint",
			"use an HTTPS endpoint, or remove the key for a keyless loopback endpoint")
	}
	key := os.Getenv(endpoint.KeyEnv)
	if key == "" {
		return route{}, refuse(RefusedCredential, role+" credential environment variable is empty",
			"export the named variable, or remove the key for a keyless endpoint")
	}
	for i := 0; i < len(key); i++ {
		if key[i] < 32 || key[i] == 127 {
			return route{}, refuse(RefusedCredential, role+" credential contains invalid header bytes", "export a single-line credential")
		}
	}
	parsed, _ := url.Parse(base)
	if strings.Contains(base, key) || strings.Contains(parsed.Path, key) {
		return route{}, refuse(RefusedEndpoint, role+" endpoint must not contain credentials", "remove the credential from the URL")
	}
	r.key = key
	return r, nil
}
