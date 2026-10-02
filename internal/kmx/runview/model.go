// Package runview contains an in-memory, runtime-neutral projection of one run.
// It does not attest to execution history or persist runtime records.
package runview

import "time"

// Missing distinguishes unavailable evidence from an observed negative outcome.
type Missing struct {
	Reason string
	Source string
}

type Agent struct {
	Name      string
	Namespace string
}

type Task struct {
	ID              string
	Name            string
	Agent           Agent
	ParentTask      string
	Status          string
	StartedAt       time.Time
	FinishedAt      time.Time
	Summary         string
	FailureReason   string
	FailureMissing  *Missing
	RevisionMissing *Missing
	EventsMissing   *Missing
	TraceMissing    *Missing
}

type DeclaredHelper struct {
	From Agent
	To   Agent
}

type HandOff struct {
	FromTask   string
	ToTask     string
	ObservedAt time.Time
}

type StatusChange struct {
	TaskID string
	Status string
	At     time.Time
	Seq    int64
}

type Event struct {
	TaskID  string
	Seq     int64
	At      time.Time
	Summary string
}

type Run struct {
	// ID is the root's native Task ID, never a kmx-generated identifier.
	ID              string
	ClusterUID      string
	Namespace       string
	RootTask        string
	Status          string
	StartedAt       time.Time
	FinishedAt      time.Time
	Tasks           []Task
	Agents          []Agent
	DeclaredHelpers []DeclaredHelper
	DeclaredState   string
	DeclaredSource  string
	DeclaredMissing *Missing
	HandOffs        []HandOff
	StatusChanges   []StatusChange
	// Events preserve each Task's sequence; timestamps do not establish
	// causal ordering across different Tasks.
	Events           []Event
	DiscoveryMissing *Missing
	EventsMissing    *Missing
	TraceMissing     *Missing
	FreshnessMissing *Missing
}
