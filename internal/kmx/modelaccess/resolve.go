package modelaccess

import (
	"context"
	"errors"
)

// Resolve uses existing read-only runtime discovery only when a local endpoint
// is omitted. Exact model selection never downloads, starts, or substitutes one.
func Resolve(ctx context.Context, route Route, discover func(context.Context) []Report) (Route, error) {
	if route.Endpoint == "" {
		switch route.Provider {
		case "openai":
			route.Endpoint = "https://api.openai.com/v1"
		case "anthropic":
			route.Endpoint = "https://api.anthropic.com/v1"
		case "copilot":
			route.Endpoint = CopilotEndpoint
			route.Auth = "copilot"
		case "ollama", "foundry-local":
			if discover == nil {
				discover = Discover
			}
			var routes []Route
			for _, report := range discover(ctx) {
				if report.State == Installed {
					for _, model := range report.Models {
						routes = append(routes, model.Route)
					}
				}
			}
			selected, err := Select(routes, route.Provider, route.Model)
			if err != nil {
				return Route{}, err
			}
			route.Endpoint = selected.Endpoint
		}
	}
	if route.KeyEnv == "" {
		switch route.Provider {
		case "openai":
			route.KeyEnv = "OPENAI_API_KEY"
		case "anthropic":
			route.KeyEnv = "ANTHROPIC_API_KEY"
		}
	}
	if route.KeyEnv != "" {
		route.Auth = "env"
	}
	if route.Validate() != nil {
		return Route{}, errors.New("invalid selected model route")
	}
	return route, nil
}
