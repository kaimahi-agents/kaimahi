package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"text/tabwriter"
)

func statusShortDigest(value string) string {
	if len(value) > 19 {
		return value[:19]
	}
	return valueOr(value, "unknown")
}
func statusEvidence(p destinationStatusProvenance) (receipt, evidence, attempt string) {
	if p.Record == nil {
		return p.State, p.State, p.State
	}
	r := p.Record
	name := r.EvaluationName
	if name == "" {
		name = r.Evaluation.Context + "/" + r.Evaluation.Namespace
	}
	evidence = "prior pass on " + name + "; same-subject (not a fresh evaluation)"
	if r.Gate == "promotion" {
		evidence = "passed on " + name + "; promoted; not evaluated on this target"
	}
	actor := "not recorded"
	if r.Actor != nil {
		actor = *r.Actor
	}
	return statusShortDigest(r.ReceiptDigest), evidence, r.AttemptedAt + "/" + actor
}
func (o *destinationStatusObserver) render(format string) error {
	if format == "json" {
		return json.NewEncoder(o.a.Out).Encode(o.report)
	}
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "Destination: %s (context=%s; namespace=%s)\n", o.report.Destination, o.report.Target.Context, o.report.Target.Namespace)
	fmt.Fprintln(&buf, o.report.Observation)
	writer := tabwriter.NewWriter(&buf, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "AGENT/MEMBER\tRUNTIME\tSUITE DIGEST\tRECEIPT\tEVIDENCE\tATTEMPT/ACTOR\tAGENT PROVENANCE\tREADY")
	for _, r := range o.report.Agents {
		receipt, evidence, attempt := statusEvidence(r.Provenance)
		fmt.Fprintf(writer, "%s/%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s: %s\n", r.Name, valueOr(r.Member, "unknown"), r.Runtime, statusShortDigest(r.SuiteDigest), receipt, evidence, attempt, r.Provenance.State, r.Readiness.State, r.Readiness.Reason)
	}
	if err := writer.Flush(); err != nil {
		return err
	}
	for _, r := range o.report.Agents {
		for _, resource := range r.Resources {
			fmt.Fprintf(&buf, "  %s/%s: provenance=%s (%s); spec=%s; %s: %s\n", resource.Kind, resource.Name, resource.Provenance.State, resource.Provenance.Reason, resource.SpecDrift, resource.Readiness.State, resource.Readiness.Reason)
		}
		for _, workload := range r.Workloads {
			fmt.Fprintf(&buf, "  %s/%s: %s: %s\n", workload.Kind, workload.Name, workload.Readiness.State, workload.Readiness.Reason)
		}
	}
	fmt.Fprintf(&buf, "Agents: %d; evaluation candidate roots (separate): %d; complete: %t\n", len(o.report.Agents), len(o.report.Candidates), o.report.Complete)
	for _, r := range o.report.Candidates {
		run := "unknown"
		if r.CandidateRun != nil {
			run = *r.CandidateRun
		}
		fmt.Fprintf(&buf, "  Candidate %s %s/%s run=%s: provenance=%s (%s); %s: %s\n", r.Runtime, r.Kind, r.Name, run, r.Provenance.State, r.Provenance.Reason, r.Readiness.State, r.Readiness.Reason)
	}
	for _, reason := range o.report.Errors {
		fmt.Fprintln(&buf, "Unknown:", reason)
	}
	_, err := fmt.Fprint(o.a.Out, buf.String())
	return err
}
