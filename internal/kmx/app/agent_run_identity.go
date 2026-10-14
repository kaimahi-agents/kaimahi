package app

import "encoding/json"

type runAgentIdentity struct {
	Found, Ready bool
	UID          string
	Generation   int64
}

// Readiness is attributable only to the Agent's current generation.
func describeRunAgent(raw []byte) runAgentIdentity {
	var result runAgentIdentity
	if len(raw) == 0 {
		return result
	}
	result.Found = true
	var doc struct {
		Metadata struct {
			UID        string
			Generation int64
		}
		Status struct {
			Ready      bool
			Conditions []serverCondition
		}
	}
	if json.Unmarshal(raw, &doc) != nil {
		return result
	}
	result.UID, result.Generation = doc.Metadata.UID, doc.Metadata.Generation
	if doc.Status.Ready && doc.Metadata.Generation > 0 {
		for _, condition := range doc.Status.Conditions {
			if condition.Type == "Ready" && condition.Status == "True" && condition.ObservedGeneration == doc.Metadata.Generation {
				result.Ready = true
			}
		}
	}
	return result
}
