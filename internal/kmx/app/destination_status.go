package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

var ErrDestinationStatusPending = errors.New("destination placement is pending; inspect reported readiness")
var errDestinationStatus = errors.New("destination status is incomplete, unhealthy or drifted; inspect reported reasons")

type DestinationStatusOptions struct{ Destination, Namespace, ExpectedRuntime, Output string }

type destinationStatusReadiness struct {
	State  string `json:"state"`
	Reason string `json:"reason"`
}
type destinationStatusRecord struct {
	releaseLiftProvenance
	Actor *string `json:"actor"`
}
type destinationStatusProvenance struct {
	State  string                   `json:"state"`
	Reason string                   `json:"reason"`
	Record *destinationStatusRecord `json:"record"`
}
type destinationStatusResource struct {
	Kind       string                      `json:"kind"`
	Name       string                      `json:"name"`
	UID        string                      `json:"uid"`
	Generation int64                       `json:"generation"`
	Readiness  destinationStatusReadiness  `json:"readiness"`
	Provenance destinationStatusProvenance `json:"provenance"`
	SpecDrift  string                      `json:"specDrift"`
}
type destinationStatusWorkload struct {
	Kind      string                     `json:"kind"`
	Name      string                     `json:"name"`
	UID       string                     `json:"uid"`
	Readiness destinationStatusReadiness `json:"readiness"`
}
type destinationStatusAgent struct {
	Kind         string                      `json:"kind"`
	Name         string                      `json:"name"`
	Runtime      string                      `json:"runtime"`
	Suite        string                      `json:"suite,omitempty"`
	Member       string                      `json:"member,omitempty"`
	SuiteDigest  string                      `json:"suiteDigest,omitempty"`
	CandidateRun *string                     `json:"candidateRun"`
	Provenance   destinationStatusProvenance `json:"provenance"`
	Readiness    destinationStatusReadiness  `json:"readiness"`
	Resources    []destinationStatusResource `json:"resources"`
	Workloads    []destinationStatusWorkload `json:"workloads"`
}
type destinationStatusRuntime struct {
	Runtime string `json:"runtime"`
	API     string `json:"api"`
	State   string `json:"state"`
}
type destinationStatusReport struct {
	Version     int                        `json:"version"`
	Destination string                     `json:"destination"`
	Target      ReleaseLiftTarget          `json:"target"`
	Complete    bool                       `json:"complete"`
	Observation string                     `json:"observation"`
	Runtimes    []destinationStatusRuntime `json:"runtimes"`
	Agents      []destinationStatusAgent   `json:"agents"`
	Candidates  []destinationStatusAgent   `json:"candidates"`
	Errors      []string                   `json:"errors"`
}

type destinationStatusObserver struct {
	a            *App
	ctx          context.Context
	report       destinationStatusReport
	calls, bytes int
	collections  map[string]bool
}
type destinationStatusAPI struct{ runtime, version, prefix string }

var destinationStatusAPIs = []destinationStatusAPI{{"orka", "core.orka.ai/v1alpha1", "/apis/core.orka.ai/v1alpha1"}, {"kagent", "kagent.dev/v1alpha2", "/apis/kagent.dev/v1alpha2"}}

// DestinationStatus observes a namespace without setup, private history, retries,
// admission or evaluation. Every subprocess is an explicitly targeted raw GET.
func (a *App) DestinationStatus(opt DestinationStatusOptions) error {
	if opt.Output != "" && opt.Output != "table" && opt.Output != "json" {
		return fmt.Errorf("status output must be table or json")
	}
	if a == nil || a.Cfg == nil || a.Run == nil || !statusSafeText(opt.Destination) || !statusSafeText(a.Cfg.KubeContext) || scaffold.ValidateNamespace(opt.Namespace) != nil || opt.ExpectedRuntime != "" && opt.ExpectedRuntime != "orka" && opt.ExpectedRuntime != "kagent" {
		return fmt.Errorf("status requires an explicit destination, context and namespace with a supported runtime expectation")
	}
	ctx, cancel := context.WithTimeout(a.operationContext(), 90*time.Second)
	defer cancel()
	worker, cfg := *a, *a.Cfg
	worker.Cfg = &cfg
	o := &destinationStatusObserver{a: &worker, ctx: ctx, report: destinationStatusReport{Version: 1, Destination: opt.Destination, Target: ReleaseLiftTarget{Context: a.Cfg.KubeContext, Namespace: opt.Namespace}, Complete: true, Observation: "Unsigned cluster observations; original resource UID continuity is unverified. Ready is placement, not answered or evaluated. No local history enrichment.", Runtimes: []destinationStatusRuntime{}, Agents: []destinationStatusAgent{}, Candidates: []destinationStatusAgent{}, Errors: []string{}}}
	o.report.Target.ClusterUID = o.namespaceUID("kube-system")
	o.report.Target.NamespaceUID = o.namespaceUID(opt.Namespace)
	served := o.discover()
	for _, api := range destinationStatusAPIs {
		state := served[api.runtime]
		o.report.Runtimes = append(o.report.Runtimes, destinationStatusRuntime{api.runtime, api.version, state})
		if opt.ExpectedRuntime == api.runtime && state != "served" {
			o.problem("expected runtime Agent API is unavailable or unknown")
		}
		if state != "served" {
			o.candidateRoots(api)
			continue
		}
		objects, _ := o.list(api.prefix, "agents", "Agent", api.version, "")
		for _, doc := range objects {
			if opt.ExpectedRuntime != "" && opt.ExpectedRuntime != api.runtime {
				o.problem("observed Agent runtime differs from destination expectation")
			}
			if statusIsCandidate(doc) {
				o.report.Candidates = append(o.report.Candidates, o.candidate(api, doc))
			} else {
				row := o.agent(api, doc)
				o.report.Agents = append(o.report.Agents, row)
			}
		}
		o.candidateRoots(api)
	}
	// Bracket successful identity reads; an unavailable initial read is never retried.
	for _, identity := range []struct{ namespace, uid string }{{"kube-system", o.report.Target.ClusterUID}, {opt.Namespace, o.report.Target.NamespaceUID}} {
		if identity.uid != "" {
			if observed := o.namespaceUID(identity.namespace); observed != "" && observed != identity.uid {
				o.problem("cluster or namespace identity changed during observation")
			}
		}
	}
	less := func(rows []destinationStatusAgent) {
		sort.Slice(rows, func(i, j int) bool {
			if rows[i].Runtime != rows[j].Runtime {
				return rows[i].Runtime < rows[j].Runtime
			}
			if rows[i].Name != rows[j].Name {
				return rows[i].Name < rows[j].Name
			}
			return rows[i].Kind < rows[j].Kind
		})
	}
	less(o.report.Agents)
	less(o.report.Candidates)
	if ctx.Err() != nil {
		o.problem("observation cancelled or time budget exhausted")
	}
	var result error
	for _, row := range append(append([]destinationStatusAgent{}, o.report.Agents...), o.report.Candidates...) {
		if row.Readiness.State == "unknown" || row.Provenance.State == "unknown" {
			o.report.Complete = false
		}
		if row.Readiness.State == "unhealthy" || row.Readiness.State == "drifted" {
			result = errDestinationStatus
		}
		if row.Readiness.State == "pending" && result == nil {
			result = ErrDestinationStatusPending
		}
	}
	if !o.report.Complete {
		result = errDestinationStatus
	}
	if err := o.render(opt.Output); err != nil {
		return fmt.Errorf("write destination status report: %w", err)
	}
	return errors.Join(result, ctx.Err())
}

func (o *destinationStatusObserver) problem(reason string) {
	o.report.Complete = false
	if !slices.Contains(o.report.Errors, reason) {
		o.report.Errors = append(o.report.Errors, reason)
	}
}
