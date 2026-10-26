package orka

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/runview"
)

// ObserveEnforcement projects typed historical identifiers and fixed actionable
// guidance from current outcomes. Invalid optional detail does not hide a Task;
// policy bytes and arbitrary status messages never enter the view.
func ObserveEnforcement(raw []byte) (*runview.Enforcement, error) {
	var task struct {
		Metadata struct {
			Generation int64 `json:"generation"`
		} `json:"metadata"`
		Spec struct {
			Enforcement json.RawMessage `json:"enforcement"`
		} `json:"spec"`
		Status struct {
			Conditions []struct {
				Type, Status, Reason string
				ObservedGeneration   int64
			} `json:"conditions"`
			Admission json.RawMessage `json:"enforcementAdmission"`
			Outcome   json.RawMessage `json:"enforcementOutcome"`
		} `json:"status"`
	}
	if err := json.Unmarshal(raw, &task); err != nil {
		return nil, fmt.Errorf("invalid Task enforcement observation")
	}
	if len(task.Spec.Enforcement) == 0 || bytes.Equal(bytes.TrimSpace(task.Spec.Enforcement), []byte("null")) {
		return nil, nil
	}
	observation := &runview.Enforcement{State: "not confirmed"}
	var requirement struct {
		Mode string `json:"mode"`
	}
	if err := json.Unmarshal(task.Spec.Enforcement, &requirement); err != nil || requirement.Mode != "enforce" {
		observation.State = "invalid or unsupported requirement; workload admission not confirmed"
		return observation, nil
	}
	conditionReason, blocked := "", false
	for _, condition := range task.Status.Conditions {
		if condition.Type != "EnforcementReady" || task.Metadata.Generation < 1 ||
			condition.ObservedGeneration != task.Metadata.Generation {
			continue
		}
		conditionReason, blocked = condition.Reason, condition.Status != "True"
		if conditionReason == "EnforcementReleaseUnobserved" {
			conditionReason = "EnforcementUncertain"
		}
		switch {
		case condition.Status == "True" && condition.Reason == "EnforcementVerified":
			var admission struct {
				BindingID, RequirementDigest string
				ObservedGeneration           int64
			}
			if json.Unmarshal(task.Status.Admission, &admission) != nil ||
				admission.ObservedGeneration != task.Metadata.Generation ||
				!enforcementIdentifier(admission.BindingID, "container:") ||
				!enforcementIdentifier(admission.RequirementDigest, "sha256:") {
				observation.State = "admission reported, physical receipt unavailable"
				break
			}
			observation.State = "admitted"
			observation.BindingID, observation.RequirementDigest = admission.BindingID, admission.RequirementDigest
		case condition.Status == "False" && condition.Reason == "EnforcementRefused":
			observation.State = "refused; workload admission not confirmed"
		case condition.Reason == "EnforcementPending":
			observation.State = "waiting for enforcement; workload admission not confirmed"
		case condition.Status == "Unknown":
			observation.State = "uncertain; workload admission not confirmed"
		}
		break
	}
	if observation.State != "admitted" {
		observation.Guidance = []string{enforcementNeedGuidance["provider-recovery"]}
		if conditionReason == "EnforcementRefused" {
			observation.Guidance = []string{enforcementNeedGuidance["operator-configuration"]}
		}
		if len(task.Status.Outcome) > 0 {
			guidance, valid := observeEnforcementOutcome(task.Status.Outcome, task.Metadata.Generation, conditionReason, blocked)
			if valid {
				observation.Guidance = guidance
			} else {
				observation.Guidance = append([]string{"Detailed enforcement remediation is unavailable or stale"}, observation.Guidance...)
			}
		}
	}
	return observation, nil
}

var enforcementNeedGuidance = map[string]string{
	"complete-policy":               "Supply one complete supported policy; do not omit a required capability",
	"supported-format":              "Use a policy format supported by every mandatory provider",
	"configured-backend":            "Ask the operator to configure the native backend for the requested policy",
	"supported-target":              "Use a supported prepared container and filesystem/network profile",
	"supported-runtime":             "Use the managed native AI worker or a supported managed ACP RuntimePool",
	"mandatory-capability-coverage": "Ask the operator to configure mandatory providers covering every requested capability",
	"provider-recovery":             "Wait for the configured providers and prepared runtime to recover; do not weaken the requirement",
	"prepared-container":            "Wait for the exact managed worker container to become prepared",
	"stopped-previous-container":    "Wait for the previous physical container to stop and its controls to retire",
	"operator-configuration":        "Ask the operator to check provider enrollment, policy and target compatibility",
	"admission-recovery":            "Inspect the Task admission recovery; a missing receipt does not authorize removal of controls",
}

func observeEnforcementOutcome(raw []byte, generation int64, reason string, blocked bool) ([]string, bool) {
	var outcome struct {
		ObservedGeneration int64    `json:"observedGeneration"`
		Reason             string   `json:"reason"`
		Needs              []string `json:"needs"`
	}
	if len(raw) > 16*1024 || json.Unmarshal(raw, &outcome) != nil ||
		!blocked || generation < 1 || outcome.ObservedGeneration != generation || outcome.Reason != reason ||
		len(outcome.Needs) > 32 {
		return nil, false
	}
	switch outcome.Reason {
	case "EnforcementPending", "EnforcementAdmissionPending", "EnforcementRefused", "EnforcementUncertain":
	default:
		return nil, false
	}
	var guidance []string
	seen := make(map[string]bool)
	for _, need := range outcome.Needs {
		text, known := enforcementNeedGuidance[need]
		if !known {
			return nil, false
		}
		if !seen[need] {
			guidance = append(guidance, text)
			seen[need] = true
		}
	}
	if len(guidance) == 0 {
		need := "provider-recovery"
		if outcome.Reason == "EnforcementRefused" {
			need = "operator-configuration"
		}
		guidance = []string{enforcementNeedGuidance[need]}
	}
	return guidance, true
}

func enforcementIdentifier(value, prefix string) bool {
	if len(value) != len(prefix)+64 || !strings.HasPrefix(value, prefix) {
		return false
	}
	hexadecimal := strings.TrimPrefix(value, prefix)
	_, err := hex.DecodeString(hexadecimal)
	return err == nil && hexadecimal == strings.ToLower(hexadecimal)
}
