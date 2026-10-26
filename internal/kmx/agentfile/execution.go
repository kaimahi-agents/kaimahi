package agentfile

import "slices"

// BindJudgeModels returns isolated execution examples with only omitted judge
// models filled from the resolved route. It preserves authored pins, versions
// and source locations; it neither validates nor changes the source revision.
func BindJudgeModels(examples []Example, model string) []Example {
	copied := CloneExamples(examples)
	for i := range copied {
		for j := range copied[i].Assertions {
			a := &copied[i].Assertions[j]
			if a.Type == "llmJudge" && a.JudgeModel == "" {
				a.JudgeModel = model
			}
		}
	}
	return copied
}

// CloneExamples detaches assertions, locations and nested JSON fixture data.
// It copies content only; it neither validates fixtures nor grants live authority.
func CloneExamples(examples []Example) []Example {
	copied := slices.Clone(examples)
	for i := range copied {
		if mocks := copied[i].Mocks; mocks != nil {
			tools := slices.Clone(mocks.Tools)
			for j := range tools {
				tools[j].Parameters = cloneFixtureValue(tools[j].Parameters).(map[string]any)
				tools[j].Calls = slices.Clone(tools[j].Calls)
				for k := range tools[j].Calls {
					call := &tools[j].Calls[k]
					call.Arguments = cloneFixtureValue(call.Arguments).(map[string]any)
					call.Result = cloneFixtureValue(call.Result).(map[string]any)
				}
			}
			copied[i].Mocks = &Mocks{Tools: tools}
		}
		copied[i].AssertionLocations = slices.Clone(copied[i].AssertionLocations)
		copied[i].Assertions = slices.Clone(copied[i].Assertions)
		for j := range copied[i].Assertions {
			a := &copied[i].Assertions[j]
			if a.MinScore != nil {
				score := *a.MinScore
				a.MinScore = &score
			}
		}
	}
	return copied
}

func cloneFixtureValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		if value == nil {
			return value
		}
		copied := make(map[string]any, len(value))
		for key, child := range value {
			copied[key] = cloneFixtureValue(child)
		}
		return copied
	case []any:
		copied := slices.Clone(value)
		for i := range copied {
			copied[i] = cloneFixtureValue(copied[i])
		}
		return copied
	default:
		return value
	}
}
