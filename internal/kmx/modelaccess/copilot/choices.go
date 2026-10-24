package copilot

import (
	"errors"
	"slices"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
)

// CatalogIDs projects validated, non-secret concrete choices for runtime UI and errors.
func CatalogIDs(routes []modelaccess.Route) []string {
	ids := []string{}
	for _, r := range routes {
		if r.Provider == "copilot" && r.Validate() == nil && r.Model != "fake" && !strings.EqualFold(r.Model, "auto") && !strings.Contains(r.Model, "://") && secretshapes.Match(r.Model) == nil {
			ids = append(ids, r.Model)
		}
	}
	slices.Sort(ids)
	return slices.Compact(ids)
}

type unavailableModel struct{ choices []string }

func (e *unavailableModel) Unwrap() error { return ErrModelUnavailable }
func (e *unavailableModel) Error() string {
	choices := strings.Join(e.choices, ", ")
	if choices == "" {
		choices = "none"
	}
	return "copilot_model_unavailable: available models: " + choices
}
func unavailableWithChoices(routes []modelaccess.Route) error {
	return &unavailableModel{CatalogIDs(routes)}
}

// ModelChoices distinguishes an owned catalog-selection failure from a service refusal.
func ModelChoices(err error) ([]string, bool) {
	var selected *unavailableModel
	if !errors.As(err, &selected) {
		return nil, false
	}
	return slices.Clone(selected.choices), true
}
