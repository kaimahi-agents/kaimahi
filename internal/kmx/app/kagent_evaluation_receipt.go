package app

import (
	"regexp"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

type kagentEvaluationImage struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	Digest       string `json:"digest"`
}

type kagentEvaluationExecution = agentruntime.EvaluationExecution

var kagentEvaluationID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,255}$`)

func safeKagentEvaluationID(value string) bool {
	return kagentEvaluationID.MatchString(value) && verificationIdentity(value)
}

func qualifiedKagentEvaluationImage(image kagentEvaluationImage, pins [3]string) bool {
	if image.OS != "linux" {
		return false
	}
	platform := 1
	switch image.Architecture {
	case "amd64":
	case "arm64":
		platform = 2
	default:
		return false
	}
	return image.Digest == pins[0] || image.Digest == pins[platform]
}

func kagentEvaluationResource(p agentruntime.ResourceEvidence) bool {
	return scaffold.ValidateObjectName(p.Name) == nil && scaffold.ValidateNamespace(p.Namespace) == nil && safeKagentEvaluationID(p.UID) && (p.ResourceVersion == "" || safeKagentEvaluationID(p.ResourceVersion)) && p.Generation >= 0
}
