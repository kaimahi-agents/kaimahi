package app

import (
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/conditionfacts"
)

func statusInteger(value any) (int64, bool) {
	return conditionfacts.Integer(value)
}

func statusTerminating(meta map[string]any) (bool, bool) {
	value := meta["deletionTimestamp"]
	if value == nil {
		return false, true
	}
	text, ok := value.(string)
	if !ok {
		return false, false
	}
	_, err := time.Parse(time.RFC3339Nano, text)
	return true, err == nil
}

// Consume exact Kubernetes keys rather than encoding/json's case-folded field
// aliases. Unknown payload fields and raw controller messages never enter output.
func statusPrimaryReadiness(runtime string, doc map[string]any) destinationStatusReadiness {
	unknown := destinationStatusReadiness{"unknown", "primary identity or readiness is malformed or indeterminate"}
	pending := destinationStatusReadiness{"pending", "current-generation readiness has not been acknowledged"}
	meta, _ := doc["metadata"].(map[string]any)
	generation, ok := statusInteger(meta["generation"])
	if !ok || generation < 1 {
		return unknown
	}
	terminating, valid := statusTerminating(meta)
	if !valid {
		return unknown
	}
	if terminating {
		return destinationStatusReadiness{"unhealthy", "primary is terminating"}
	}
	status, ok := doc["status"].(map[string]any)
	if doc["status"] == nil {
		return pending
	}
	if !ok {
		return unknown
	}
	facts := conditionfacts.ReadMap(status)
	if facts.Shape() == conditionfacts.Invalid || facts.Len() > 64 || facts.HasDuplicateTypes() {
		return unknown
	}
	conditions := map[string]serverCondition{}
	for _, c := range facts.Entries() {
		if !c.Object || !c.TypeValid || !c.StateValid || !c.ObservedValid || c.ObservedGeneration > generation {
			return unknown
		}
		conditions[c.Type] = serverCondition{Type: c.Type, Status: c.State, ObservedGeneration: c.ObservedGeneration, Reason: c.Reason}
	}
	if runtime == "kagent" {
		observed := int64(0)
		if value, present := status["observedGeneration"]; present {
			var valid bool
			observed, valid = statusInteger(value)
			if !valid || observed > generation {
				return unknown
			}
		}
		if observed != generation {
			return pending
		}
		kind, _ := doc["kind"].(string)
		accepted, exists := conditions["Accepted"]
		if !exists || accepted.ObservedGeneration != generation && !(kind == "ModelConfig" && accepted.ObservedGeneration == 0) {
			return pending
		}
		if accepted.Status == "Unknown" {
			return unknown
		}
		if accepted.Status == "False" {
			return destinationStatusReadiness{"unhealthy", "current-generation primary rejection"}
		}
		if kind == "ModelConfig" {
			return destinationStatusReadiness{"ready", "current-generation ModelConfig Accepted"}
		}
		ready, exists := conditions["Ready"]
		if !exists || ready.ObservedGeneration != generation {
			return pending
		}
		if ready.Status == "Unknown" {
			return unknown
		}
		if ready.Status != "True" || ready.Reason != "DeploymentReady" {
			return pending
		}
		return destinationStatusReadiness{"ready", "current-generation Agent Accepted and DeploymentReady"}
	}
	if runtime != "orka" {
		return unknown
	}
	ready, hasReady := status["ready"].(bool)
	if raw, present := status["ready"]; present && raw != nil && !hasReady {
		return unknown
	}
	condition, exists := conditions["Ready"]
	if !exists || condition.ObservedGeneration != generation {
		return pending
	}
	if condition.Status == "Unknown" {
		return unknown
	}
	if condition.Status == "False" {
		if ready {
			return unknown
		}
		return destinationStatusReadiness{"unhealthy", "current-generation primary reports not Ready"}
	}
	if !hasReady {
		return pending
	}
	if !ready {
		return unknown
	}
	return destinationStatusReadiness{"ready", "current-generation Ready placement"}
}
