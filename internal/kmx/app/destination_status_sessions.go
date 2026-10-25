package app

import (
	"encoding/json"
	"reflect"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsessions"
)

func statusSessionsReadiness(doc map[string]any) destinationStatusReadiness {
	raw, _ := json.Marshal(doc)
	a, err := agentsessions.DecodeAgent(raw)
	if err != nil {
		return destinationStatusReadiness{"unknown", "Agent contract or identity is malformed"}
	}
	if len(a.Metadata.DeletionTimestamp) > 0 && string(a.Metadata.DeletionTimestamp) != "null" {
		return destinationStatusReadiness{"unhealthy", "Agent is terminating"}
	}
	if agentsessions.AgentRegistered(a) {
		return destinationStatusReadiness{"registered", "last controller-observed admission; not live availability or target evaluation"}
	}
	return destinationStatusReadiness{"pending", "current UID, generation, member and package registration have not all been acknowledged"}
}

func (o *destinationStatusObserver) sessionsAgent(doc map[string]any, api destinationStatusAPI) destinationStatusAgent {
	meta := statusMap(doc, "metadata")
	generation, _ := statusInteger(meta["generation"])
	provenance := destinationStatusProvenance{State: "provenance not recorded", Reason: "Agent contract is a controller observation, not kmx evaluation or ownership proof"}
	uid := statusString(meta, "uid")
	if !statusOpaque(uid) {
		uid = ""
	}
	primary := destinationStatusResource{Kind: "Agent", Name: statusString(meta, "name"), UID: uid, Generation: generation, Readiness: statusSessionsReadiness(doc), Provenance: provenance, SpecDrift: "not recorded"}
	row := destinationStatusAgent{Kind: "Agent", Name: primary.Name, Runtime: "agentsessions", Readiness: primary.Readiness, Provenance: provenance, Resources: []destinationStatusResource{primary}, Workloads: []destinationStatusWorkload{}}
	raw, _ := json.Marshal(doc)
	agent, err := agentsessions.DecodeAgent(raw)
	if err != nil || !o.validObject(doc, "Agent", api.version) {
		row.Readiness = destinationStatusReadiness{"unknown", "Agent contract or identity is malformed"}
		row.Resources[0].Readiness = row.Readiness
		return row
	}
	if statusSafeText(agent.Spec.Member) {
		row.Member = agent.Spec.Member
	}
	_, row.SuiteDigest, _ = strings.Cut(agent.Spec.Package.Reference, "@")
	after, ok := o.workloadObject(api.prefix, "agents", "Agent", api.version, primary.Name)
	latest := statusMap(after, "metadata")
	if !ok || latest["uid"] != meta["uid"] || !reflect.DeepEqual(latest["generation"], meta["generation"]) || !reflect.DeepEqual(after["spec"], doc["spec"]) {
		row.Readiness = destinationStatusReadiness{"unknown", "Agent identity or spec changed or became unreadable"}
	} else {
		row.Readiness = statusSessionsReadiness(after)
	}
	row.Resources[0].Readiness = row.Readiness
	return row
}
