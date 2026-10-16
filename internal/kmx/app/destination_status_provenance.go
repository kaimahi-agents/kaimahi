package app

import (
	"strings"
	"time"

	agentsuite "github.com/agent-primitives/suite"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

type destinationStatusOwner struct{ Suite, Member, Digest, Binding, Revision, Run string }

// Ownership comes only from the complete native markers, never lift-record.
func statusOwnership(runtime string, meta map[string]any) (*destinationStatusOwner, string) {
	ann, ok := meta["annotations"].(map[string]any)
	if meta["annotations"] != nil && !ok {
		return nil, "unknown"
	}
	legacy := false
	for _, key := range []string{orkaBundleMarker, orkaPortableMarker, orkaRenderedMarker, kagentVersionMarker} {
		if _, present := ann[key]; present {
			legacy = true
		}
	}
	if !hasOrkaSuiteReservation(ann) && !legacy {
		return nil, "not kmx-owned"
	}
	if legacy {
		return nil, "unknown"
	}
	v := make([]string, 6)
	for i, key := range orkaSuiteMarkers {
		var good bool
		v[i], good = ann[key].(string)
		if !good {
			return nil, "unknown"
		}
	}
	if !statusSafeText(v[0]) || !statusSafeText(v[2]) {
		return nil, "unknown"
	}
	run := ""
	if raw, present := ann[suiteCandidateRunMarker]; present {
		var good bool
		run, good = raw.(string)
		if !good || !evaluationRunID(run) {
			return nil, "unknown"
		}
	}
	if runtime == "orka" {
		owner := &orkaSuiteOwnership{v[0], v[1], v[2], v[3], v[4], v[5], run}
		if !owner.valid() || owner.check(ann) != nil {
			return nil, "unknown"
		}
	} else {
		owner := &suiteKagentOwnership{v[0], v[1], v[2], v[3], v[4], v[5], workspace.RuntimeProfile{ID: "kagent", Version: "v0.10.3", Engine: "go"}}
		labels, _ := meta["labels"].(map[string]any)
		if !owner.valid() || labels["app.kubernetes.io/managed-by"] != "kmx" {
			return nil, "unknown"
		}
	}
	return &destinationStatusOwner{v[0], v[2], v[1], v[4], v[5], run}, "kmx-owned"
}

func statusTargetValid(target ReleaseLiftTarget) bool {
	return statusSafeText(target.Context) && scaffold.ValidateNamespace(target.Namespace) == nil && statusOpaque(target.ClusterUID) && statusOpaque(target.NamespaceUID)
}
func statusSameTarget(a, b ReleaseLiftTarget) bool {
	// Kubeconfig aliases supply access, not cluster/environment identity.
	return a.ClusterUID == b.ClusterUID && a.Namespace == b.Namespace && a.NamespaceUID == b.NamespaceUID
}
func statusRecord(raw string, runtime string) *destinationStatusRecord {
	if len(raw) == 0 || len(raw) > 8<<10 {
		return nil
	}
	var fields map[string]any
	var r releaseLiftProvenance
	if agentsuite.Unmarshal([]byte(raw), &fields) != nil || agentsuite.Unmarshal([]byte(raw), &r) != nil {
		return nil
	}
	if _, ok := fields["evaluatedOnTarget"].(bool); !ok {
		return nil
	}
	if r.Version != 1 || !evaluationRunID(r.LiftID) || !evaluationRunID(r.ReceiptRunID) || !agentsuite.IsValidDigest(r.SuiteDigest) || !agentsuite.IsValidDigest(r.ReceiptDigest) || !statusTargetValid(r.Target) || !statusTargetValid(r.Evaluation) || r.EvaluationName != "" && !statusSafeText(r.EvaluationName) || r.Actor != "" && !statusSafeText(r.Actor) {
		return nil
	}
	if r.Gate != "promotion" && r.Gate != "same-subject" || r.EvaluatedOnTarget != (r.Gate == "same-subject") || r.Gate == "same-subject" && !statusSameTarget(r.Evaluation, r.Target) {
		return nil
	}
	if _, err := time.Parse(time.RFC3339Nano, r.AttemptedAt); err != nil || !strings.HasSuffix(r.AttemptedAt, "Z") {
		return nil
	}
	companion := "Provider"
	if runtime == "kagent" {
		companion = "ModelConfig"
	}
	if len(r.ResourceSpecs) != 2 || !validOrkaMarkerDigest(r.ResourceSpecs["Agent"]) || !validOrkaMarkerDigest(r.ResourceSpecs[companion]) {
		return nil
	}
	// Match the producer's typed-ID exemption, not a blanket secret exemption.
	masked := r
	masked.LiftID, masked.ReceiptRunID = "lift", "run"
	checked, err := agentsuite.Marshal(masked)
	if err != nil || scaffold.RefuseKeyShapes(string(checked)) != nil {
		return nil
	}
	var actor *string
	if r.Actor != "" {
		value := r.Actor
		actor = &value
	}
	return &destinationStatusRecord{r, actor}
}

func (o *destinationStatusObserver) resource(api destinationStatusAPI, doc map[string]any) (resource destinationStatusResource) {
	meta := doc["metadata"].(map[string]any)
	kind, _ := doc["kind"].(string)
	name, _ := meta["name"].(string)
	uid, _ := meta["uid"].(string)
	generation, _ := statusInteger(meta["generation"])
	if !statusOpaque(uid) {
		uid = ""
	}
	resource = destinationStatusResource{Kind: kind, Name: name, UID: uid, Generation: generation, Readiness: statusPrimaryReadiness(api.runtime, doc), SpecDrift: "unknown"}
	defer func() {
		if !o.validObject(doc, kind, api.version) {
			resource.Readiness = destinationStatusReadiness{"unknown", "primary identity is incomplete or malformed"}
			resource.Provenance.State, resource.Provenance.Reason = "unknown", "identity is unknown; readable metadata is retained without authority"
			resource.SpecDrift = "unknown"
		}
	}()
	owner, state := statusOwnership(api.runtime, meta)
	p := destinationStatusProvenance{State: state, Reason: "no native suite ownership markers"}
	if state == "unknown" {
		p.Reason = "native suite ownership is incomplete, conflicting or malformed"
	}
	if owner == nil {
		resource.Provenance = p
		if state == "not kmx-owned" {
			resource.SpecDrift = "not kmx-owned"
		}
		return resource
	}
	ann, _ := meta["annotations"].(map[string]any)
	value, present := ann[releaseLiftRecordAnnotation]
	if !present {
		resource.Provenance = destinationStatusProvenance{State: "provenance not recorded", Reason: "kmx-owned object has no create-time Lift observation"}
		resource.SpecDrift = "not recorded"
		return resource
	}
	text, ok := value.(string)
	p = destinationStatusProvenance{State: "unknown", Reason: "Lift observation is malformed, unsupported or incomplete"}
	if !ok {
		resource.Provenance = p
		return resource
	}
	p.Record = statusRecord(text, api.runtime)
	if p.Record == nil {
		resource.Provenance = p
		return resource
	}
	if _, described := p.Record.ResourceSpecs[kind]; !described {
		p.Reason = "Lift observation does not describe this resource kind"
		resource.Provenance = p
		return resource
	}
	if p.Record.SuiteDigest != owner.Digest {
		p.Reason = "Lift observation conflicts with native suite identity"
		resource.Provenance = p
		return resource
	}
	if !statusSameTarget(p.Record.Target, o.report.Target) {
		p.Reason = "Lift observation target differs from observed cluster or namespace identity"
		resource.Provenance = p
		return resource
	}
	p.State, p.Reason = "recorded", "unsigned create-time Lift observation; not authentication or UID continuity proof"
	resource.Provenance = p
	var digest string
	var err error
	if api.runtime == "orka" {
		digest, err = suiteNativeDigest("kmx.native-orka.spec/v1", doc["spec"])
	} else {
		digest, err = kagentSpecFingerprint(doc["spec"])
	}
	if _, ok := doc["spec"].(map[string]any); !ok || err != nil {
		resource.Readiness = destinationStatusReadiness{"unknown", "current spec cannot be compared"}
		return resource
	}
	resource.SpecDrift = "matched"
	if digest != p.Record.ResourceSpecs[kind] {
		resource.SpecDrift = "drifted"
		resource.Readiness = destinationStatusReadiness{"drifted", "current spec differs from the recorded expectation"}
	}
	return resource
}

func (o *destinationStatusObserver) agent(api destinationStatusAPI, doc map[string]any) destinationStatusAgent {
	meta := doc["metadata"].(map[string]any)
	primary := o.resource(api, doc)
	row := destinationStatusAgent{Kind: "Agent", Name: primary.Name, Runtime: api.runtime, Provenance: primary.Provenance, Readiness: primary.Readiness, Resources: []destinationStatusResource{primary}, Workloads: []destinationStatusWorkload{}}
	if owner, _ := statusOwnership(api.runtime, meta); owner != nil {
		row.Suite, row.Member, row.SuiteDigest = owner.Suite, owner.Member, owner.Digest
	}
	if !o.validObject(doc, "Agent", api.version) {
		return row
	}
	kind, plural, name := "Provider", "providers", ""
	spec, _ := doc["spec"].(map[string]any)
	if api.runtime == "orka" {
		ref, _ := spec["providerRef"].(map[string]any)
		name, _ = ref["name"].(string)
		if ns, ok := ref["namespace"].(string); ok && ns != "" && ns != o.report.Target.Namespace {
			name = ""
		}
	} else {
		kind, plural = "ModelConfig", "modelconfigs"
		decl, _ := spec["declarative"].(map[string]any)
		name, _ = decl["modelConfig"].(string)
	}
	companion := destinationStatusResource{Kind: kind, Name: "unknown", Readiness: destinationStatusReadiness{"unknown", "prerequisite reference or observation is unknown"}, Provenance: destinationStatusProvenance{State: "unknown", Reason: "prerequisite could not be independently observed"}, SpecDrift: "unknown"}
	var observed map[string]any
	if scaffold.ValidateObjectName(name) == nil && statusSafeText(name) {
		companion.Name = name
		if value, ok := o.workloadObject(api.prefix, plural, kind, api.version, name); ok {
			observed = value
			companion = o.resource(api, observed)
			owner, _ := statusOwnership(api.runtime, meta)
			other, _ := statusOwnership(api.runtime, statusMap(observed, "metadata"))
			if owner != nil && (other == nil || *owner != *other) {
				row.Readiness = statusWorseReadiness(row.Readiness, destinationStatusReadiness{"drifted", "prerequisite has foreign or changed native ownership"})
			}
		}
	}
	row.Resources = append(row.Resources, companion)
	for _, r := range row.Resources {
		if r.Provenance.State == "unknown" {
			o.report.Complete = false
		}
		row.Readiness = statusWorseReadiness(row.Readiness, r.Readiness)
	}
	if api.runtime == "kagent" && primary.Readiness.State != "unknown" {
		var workloadReadiness destinationStatusReadiness
		row.Workloads, workloadReadiness = o.kagentWorkloads(doc)
		row.Readiness = statusWorseReadiness(row.Readiness, workloadReadiness)
	}
	// Reobserve acknowledged primaries after their prerequisites/workloads.
	// UID/spec/ownership changes fail closed; status-only RV churn is not drift.
	for i, before := range []map[string]any{doc, observed} {
		if before == nil {
			continue
		}
		collection := "agents"
		if i == 1 {
			collection = plural
		}
		latest, unchanged := o.recheck(api, collection, before)
		if !unchanged {
			latest = destinationStatusReadiness{"unknown", "primary identity, spec or ownership changed or became unreadable"}
			row.Resources[i].Provenance.State = "unknown"
			row.Resources[i].Provenance.Reason = "initial observation retained; current object changed or became unreadable"
			o.report.Complete = false
		}
		row.Resources[i].Readiness = statusWorseReadiness(row.Resources[i].Readiness, latest)
		row.Readiness = statusWorseReadiness(row.Readiness, latest)
	}
	row.Provenance = row.Resources[0].Provenance
	return row
}
func statusWorseReadiness(a, b destinationStatusReadiness) destinationStatusReadiness {
	rank := map[string]int{"ready": 0, "pending": 1, "unhealthy": 2, "drifted": 3, "unknown": 4}
	if rank[b.State] > rank[a.State] {
		return b
	}
	return a
}
