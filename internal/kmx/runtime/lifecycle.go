package runtime

import (
	"context"
)

type ResourceOutcome string

const (
	ResourceCreated ResourceOutcome = "created"
	ResourceReused  ResourceOutcome = "reused"
	ResourceUpdated ResourceOutcome = "updated"
	ResourceAdopted ResourceOutcome = "adopted"
)

// ResourceEvidence carries identity and non-secret spec evidence. Key is a
// reference name only; credentials and their hashes never belong in history.
type ResourceEvidence struct {
	Kind, Name, Namespace, UID string
	ResourceVersion            string `json:",omitempty"`
	Generation                 int64  `json:",omitempty"`
	SpecDigest                 string `json:",omitempty"`
	Key                        string `json:",omitempty"`
}

// EvaluationJudge evaluates an entire case while the attributed answer remains
// ephemeral. The app-owned implementation returns ordinary and judge rows.
// Adapters invoke it only after successful execution and revision attribution.
type EvaluationJudge func(context.Context, EvaluationCase, EvaluationEvidence) []EvaluationAssertionResult

// EvaluationVerdict is one case's outcome. Unknown is neither a pass nor a
// failure: the case ran, or may have, but its outcome could not be observed.
type EvaluationVerdict string

const (
	EvaluationPass    EvaluationVerdict = "pass"
	EvaluationFail    EvaluationVerdict = "fail"
	EvaluationUnknown EvaluationVerdict = "unknown"
)

// EvaluationReceipt is one case's recorded outcome. It deliberately carries
// no answer text: AnswerSHA256 is the only trace of what the agent said.
// Receipts remain local, not committed beside a public bundle. Matched and
// Missing partition ExpectContains only when an answer was read; TaskName and
// TaskUID name the one execution this case caused, and TaskUID is empty when the
// create itself was ambiguous. Detail explains a fail or unknown verdict.
type EvaluationReceipt struct {
	CaseID           string
	Verdict          EvaluationVerdict
	Matched, Missing []string
	Assertions       []EvaluationAssertionResult
	TaskName         string
	TaskUID          string
	AnswerSHA256     string
	Detail           string
	Execution        *EvaluationExecution
	// Stop prevents later cases after an ambiguous send or lost attribution.
	Stop bool
}

// EvaluationExecution records native request and Task identities, not
// Kubernetes Task names or UIDs. Unknown sends retain only request identities.
type EvaluationExecution struct {
	RPCID            string `json:"rpcID"`
	RequestMessageID string `json:"requestMessageID"`
	TaskID           string `json:"taskID,omitempty"`
	ContextID        string `json:"contextID,omitempty"`
	State            string `json:"state,omitempty"`
}

// Document is one rendered document: the exact bytes plus whether Deploy may
// apply them. Both kinds are rendered output and both are covered by the
// rendered digest; they differ only in what Deploy is allowed to do. A
// review-only document is one an adapter renders for a human to read but must
// never write — a value-free credential skeleton naming a prerequisite an
// operator provisions separately, which applying would create empty.
type Document struct {
	bytes      []byte
	deployable bool
}

// ApplyDocument is a rendered document Deploy applies, in the order given.
func ApplyDocument(document []byte) Document {
	return Document{bytes: copyBytes(document), deployable: true}
}

// ReviewDocument is a rendered document that belongs to the artifact and the
// rendered digest but is never written to a cluster.
func ReviewDocument(document []byte) Document {
	return Document{bytes: copyBytes(document)}
}

// Bytes returns a copy of the document's exact rendered bytes.
func (d Document) Bytes() []byte { return copyBytes(d.bytes) }

// Deployable reports whether Deploy may apply this document.
func (d Document) Deployable() bool { return d.deployable }

func copyBytes(data []byte) []byte {
	if data == nil {
		return nil
	}
	return append([]byte(nil), data...)
}
