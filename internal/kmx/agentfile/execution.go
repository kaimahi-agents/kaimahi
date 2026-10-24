package agentfile

import "slices"

// BindJudgeModels returns isolated execution examples with only omitted judge
// models filled from the resolved route. It preserves authored pins, versions
// and source locations; it neither validates nor changes the source revision.
func BindJudgeModels(examples []Example, model string) []Example {
	copied := slices.Clone(examples)
	for i := range copied {
		copied[i].AssertionLocations = slices.Clone(copied[i].AssertionLocations)
		copied[i].Assertions = slices.Clone(copied[i].Assertions)
		for j := range copied[i].Assertions {
			a := &copied[i].Assertions[j]
			if a.MinScore != nil {
				score := *a.MinScore
				a.MinScore = &score
			}
			if a.Type == "llmJudge" && a.JudgeModel == "" {
				a.JudgeModel = model
			}
		}
	}
	return copied
}
