package app

type destinationStatusCandidateKind struct{ plural, kind string }

func statusCandidateKinds(runtime string) []destinationStatusCandidateKind {
	if runtime == "orka" {
		return []destinationStatusCandidateKind{{"providers", "Provider"}, {"tasks", "Task"}}
	}
	return []destinationStatusCandidateKind{{"modelconfigs", "ModelConfig"}}
}
func statusIsCandidate(doc map[string]any) bool {
	_, present := statusMap(statusMap(doc, "metadata"), "annotations")[suiteCandidateRunMarker]
	return present
}

// Candidates are counted as separately reserved evaluation roots, not deployed
// Agents. Their execution/placement progress cannot change deployment readiness.
func (o *destinationStatusObserver) candidate(api destinationStatusAPI, doc map[string]any) destinationStatusAgent {
	resource := o.resource(api, doc)
	resource.Readiness = destinationStatusReadiness{"not applicable", "evaluation candidate; excluded from deployed Agent readiness"}
	row := destinationStatusAgent{Kind: resource.Kind, Name: resource.Name, Runtime: api.runtime, Provenance: resource.Provenance, Readiness: resource.Readiness, Resources: []destinationStatusResource{resource}, Workloads: []destinationStatusWorkload{}}
	meta := statusMap(doc, "metadata")
	if owner, _ := statusOwnership(api.runtime, meta); owner != nil {
		row.Suite, row.Member, row.SuiteDigest = owner.Suite, owner.Member, owner.Digest
	}
	if run, ok := statusMap(meta, "annotations")[suiteCandidateRunMarker].(string); ok && evaluationRunID(run) {
		row.CandidateRun = &run
	}
	return row
}
func (o *destinationStatusObserver) candidateRoots(api destinationStatusAPI) {
	for _, kind := range statusCandidateKinds(api.runtime) {
		if !o.collections[api.runtime+"/"+kind.plural] {
			continue
		}
		rows, _ := o.list(api.prefix, kind.plural, kind.kind, api.version, "")
		for _, doc := range rows {
			if statusIsCandidate(doc) {
				o.report.Candidates = append(o.report.Candidates, o.candidate(api, doc))
			}
		}
	}
}
