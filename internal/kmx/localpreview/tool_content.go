package localpreview

import (
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
)

func checkToolContent(host *localtools.Host, r agentpreview.Request) error {
	a := r.Agent
	fields := []string{r.FileLabel, a.Title, a.Identity.ID, a.Identity.Purpose, a.Revision, a.Model, a.Instructions, a.NativeTaskInstructions, r.Route.Provider, r.Route.Model}
	fields = append(fields, a.Tools...)
	if r.JudgeRoute != nil {
		fields = append(fields, r.JudgeRoute.Provider, r.JudgeRoute.Model)
	}
	if r.Input != nil {
		fields = append(fields, *r.Input)
	}
	for _, ex := range r.Examples {
		fields = append(fields, ex.ID, ex.Input)
		for _, as := range ex.Assertions {
			fields = append(fields, as.ID, as.Type, as.Value, as.Pattern, as.Tool, as.Rubric, as.JudgeModel, as.JudgeVersion)
		}
	}
	return host.Check(fields...)
}
