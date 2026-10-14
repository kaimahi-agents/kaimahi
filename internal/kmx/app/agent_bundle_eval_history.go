package app

const (
	evaluationExecutionUnavailable = "evaluation execution unavailable"
	evaluationExecutionFailed      = "evaluation execution failed"
	evaluationResultUnavailable    = "evaluation result unavailable"
)

// Native evidence identifies a fresh run by 128 bits of lowercase hex.
func evaluationRunID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, c := range id {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
