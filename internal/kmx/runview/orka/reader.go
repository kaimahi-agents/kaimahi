// Package orka projects caller-authorized Orka v0.2.0 Tasks into a runview.
package orka

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/runview"
)

// ErrDenied marks a Kubernetes read refused by the caller's context.
var ErrDenied = errors.New("permission denied")

// ErrConnectionLost means a previously established read path dropped.
var ErrConnectionLost = errors.New("connection lost")

// ErrPageTooLarge permits bounded retry with a smaller event page.
var ErrPageTooLarge = errors.New("event page too large")

// ErrInvalidResponse marks an HTTP response whose content is not usable evidence.
var ErrInvalidResponse = errors.New("invalid response")

// ParentSelector reproduces Orka's Task-name label normalization for DNS names.
func ParentSelector(name string) string {
	if len(name) <= 63 {
		return name
	}
	digest := sha256.Sum256([]byte(name))
	prefix := strings.Trim(name[:50], "-.")
	if prefix == "" {
		prefix = "label"
	}
	return prefix + "-" + hex.EncodeToString(digest[:])[:12]
}

// HelperPolicy is the currently observed Agent permission, not a historical
// assertion about the policy under which this Task ran.
type HelperPolicy struct {
	State   string
	Helpers []runview.Agent
}

// Source is the caller-scoped read boundary. Task must check the caller's own
// Kubernetes get permission even if Events and Trace use a separate bearer.
type Source interface {
	Task(context.Context, string, string) ([]byte, error)
	Children(context.Context, string, string) ([][]byte, error)
	Helpers(context.Context, string, string) (HelperPolicy, error)
	Events(context.Context, string, string, int64, int) ([]byte, int, error)
	Trace(context.Context, string, string) (int, error)
}

type Reader struct{ source Source }

func NewReader(source Source) Reader { return Reader{source: source} }

type nativeTask struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name              string                             `json:"name"`
		Namespace         string                             `json:"namespace"`
		UID               string                             `json:"uid"`
		CreationTimestamp time.Time                          `json:"creationTimestamp"`
		Labels            map[string]string                  `json:"labels"`
		Annotations       map[string]string                  `json:"annotations"`
		OwnerReferences   []struct{ Kind, Name, UID string } `json:"ownerReferences"`
	} `json:"metadata"`
	Spec struct {
		AgentRef struct{ Name, Namespace string } `json:"agentRef"`
	} `json:"spec"`
	Status struct {
		Phase          string    `json:"phase"`
		CompletionTime time.Time `json:"completionTime"`
		ChildTasks     []struct {
			Name string `json:"name"`
		} `json:"childTasks"`
		Conditions []struct{ Reason, Type, Status string } `json:"conditions"`
	} `json:"status"`
}

func decodeTask(raw []byte, ns string) (nativeTask, error) {
	var task nativeTask
	if err := json.Unmarshal(raw, &task); err != nil || task.Kind != "Task" || task.Metadata.Namespace != ns || task.Metadata.Name == "" || task.Metadata.UID == "" {
		return task, errors.New("invalid Task identity")
	}
	return task, nil
}
func missing(reason, source string) *runview.Missing {
	return &runview.Missing{Reason: reason, Source: source}
}
func readReason(err error) string {
	if errors.Is(err, ErrDenied) {
		return "permission denied"
	}
	if errors.Is(err, ErrConnectionLost) {
		return "connection lost"
	}
	if errors.Is(err, ErrPageTooLarge) {
		return "event too large"
	}
	if errors.Is(err, ErrInvalidResponse) {
		return "invalid response"
	}
	return "read unavailable"
}
func httpMissing(code int) string {
	switch code {
	case http.StatusForbidden, http.StatusUnauthorized:
		return "permission denied"
	case http.StatusNotImplemented:
		return "history unavailable"
	case http.StatusNotFound:
		return "not found"
	case http.StatusRequestEntityTooLarge:
		return "history too large"
	default:
		return "read unavailable"
	}
}
func agentOf(t nativeTask) runview.Agent {
	ns := t.Spec.AgentRef.Namespace
	if ns == "" {
		ns = t.Metadata.Namespace
	}
	return runview.Agent{Name: t.Spec.AgentRef.Name, Namespace: ns}
}
func delegated(child, parent nativeTask) bool {
	if child.Metadata.Namespace != parent.Metadata.Namespace || child.Metadata.UID == parent.Metadata.UID || child.Metadata.Annotations["orka.ai/parent-task-name"] != parent.Metadata.Name || child.Metadata.Labels["orka.ai/parent-task"] != ParentSelector(parent.Metadata.Name) || child.Metadata.Labels["orka.ai/delegated-agent"] == "" || child.Metadata.Labels["orka.ai/delegated-agent"] != child.Spec.AgentRef.Name || child.Metadata.Labels["orka.ai/coordinator"] != "true" {
		return false
	}
	for _, owner := range child.Metadata.OwnerReferences {
		if owner.Kind == "Task" && owner.Name == parent.Metadata.Name && owner.UID == parent.Metadata.UID {
			return true
		}
	}
	return false
}

// Read creates a bounded, read-only snapshot. rootUID protects against a
// deleted Task name being reused; all child Tasks must pass their own get read.
func (r Reader) Read(ctx context.Context, clusterUID, namespace, rootName, rootUID string) (runview.Run, error) {
	var out runview.Run
	if r.source == nil || clusterUID == "" || namespace == "" || rootName == "" || rootUID == "" {
		return out, errors.New("run requires source, cluster UID, namespace, root name and root UID")
	}
	raw, err := r.source.Task(ctx, namespace, rootName)
	if err != nil {
		return out, fmt.Errorf("root Task read: %w", err)
	}
	root, err := decodeTask(raw, namespace)
	if err != nil {
		return out, err
	}
	if root.Metadata.UID != rootUID {
		return out, errors.New("root Task UID changed")
	}
	out = runview.Run{ID: rootUID, ClusterUID: clusterUID, Namespace: namespace, RootTask: rootUID, Status: root.Status.Phase, StartedAt: root.Metadata.CreationTimestamp, FinishedAt: root.Status.CompletionTime}
	if out.Status == "" {
		out.Status = "Unknown"
	}
	coordinator := agentOf(root)
	policy, err := r.source.Helpers(ctx, coordinator.Namespace, coordinator.Name)
	out.DeclaredSource = "current Orka Agent coordination"
	out.DeclaredState = policy.State
	if err != nil {
		out.DeclaredState = "unknown"
		out.DeclaredMissing = missing(readReason(err), "Orka Agent coordination")
	} else if policy.State == "unbounded" {
		out.DeclaredMissing = missing("unbounded helper policy", "Orka Agent coordination")
	} else if policy.State == "enabled" {
		for _, helper := range policy.Helpers {
			if helper.Namespace == "" {
				helper.Namespace = namespace
			}
			out.DeclaredHelpers = append(out.DeclaredHelpers, runview.DeclaredHelper{From: coordinator, To: helper})
		}
	}
	queue := []nativeTask{root}
	seen := map[string]bool{rootUID: true}
	parents := map[string]string{}
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		task := runview.Task{ID: parent.Metadata.UID, Name: parent.Metadata.Name, Agent: agentOf(parent), ParentTask: parents[parent.Metadata.UID], Status: parent.Status.Phase, StartedAt: parent.Metadata.CreationTimestamp, FinishedAt: parent.Status.CompletionTime, Summary: "Task activity (content redacted)", RevisionMissing: missing("no Task-bound bundle revision", "Orka Task")}
		if task.Status == "" {
			task.Status = "Unknown"
		}
		if task.Status == "Failed" {
			task.FailureMissing = missing("failure detail redacted", "Orka Task condition")
			for _, condition := range parent.Status.Conditions {
				if condition.Type == "Ready" && (condition.Reason == "TaskFailed" || condition.Reason == "DeadlineExceeded" || condition.Reason == "TaskTimeout") {
					task.FailureReason = condition.Reason
					task.FailureMissing = nil
				}
			}
		}
		for _, a := range out.Agents {
			if a == task.Agent {
				goto agentKnown
			}
		}
		out.Agents = append(out.Agents, task.Agent)
	agentKnown:
		out.Tasks = append(out.Tasks, task)
		previous := out.EventsMissing
		out.EventsMissing = nil
		r.events(ctx, &out, namespace, parent)
		out.Tasks[len(out.Tasks)-1].EventsMissing = out.EventsMissing
		if previous != nil {
			out.EventsMissing = previous
		}
		var traceMissing *runview.Missing
		if check := r.verifyTask(ctx, namespace, parent); check != nil {
			traceMissing = check
		} else if code, err := r.source.Trace(ctx, namespace, parent.Metadata.Name); err != nil {
			reason := readReason(err)
			if errors.Is(err, ErrPageTooLarge) {
				reason = "history too large"
			}
			traceMissing = missing(reason, "Orka trace")
			if errors.Is(err, ErrConnectionLost) {
				out.FreshnessMissing = missing("connection lost", "Orka API")
			}
		} else if check := r.verifyTask(ctx, namespace, parent); check != nil {
			traceMissing = check
		} else if code != http.StatusOK {
			traceMissing = missing(httpMissing(code), "Orka trace")
		}
		out.Tasks[len(out.Tasks)-1].TraceMissing = traceMissing
		if out.TraceMissing == nil {
			out.TraceMissing = traceMissing
		}
		rawChildren, err := r.source.Children(ctx, namespace, parent.Metadata.Name)
		if err != nil {
			out.DiscoveryMissing = missing(readReason(err), "Kubernetes Task list")
			if errors.Is(err, ErrConnectionLost) {
				out.FreshnessMissing = missing("connection lost", "Kubernetes Task list")
			}
			// The native projection carries names, not UIDs. Treat it only as a
			// discovery hint; individual gets and owner UID checks remain mandatory.
			for _, c := range parent.Status.ChildTasks {
				if c.Name != "" {
					raw, readErr := r.source.Task(ctx, namespace, c.Name)
					if readErr == nil {
						rawChildren = append(rawChildren, raw)
					} else {
						out.DiscoveryMissing = missing(readReason(readErr), "Kubernetes child Task get")
					}
				}
			}
		}
		var children []nativeTask
		for _, raw := range rawChildren {
			candidate, err := decodeTask(raw, namespace)
			if err != nil {
				out.DiscoveryMissing = missing("invalid Task list item", "Kubernetes Task discovery")
				continue
			}
			if !delegated(candidate, parent) || seen[candidate.Metadata.UID] {
				continue
			}
			// A namespace list is not proof that this caller can get the child.
			checked, err := r.source.Task(ctx, namespace, candidate.Metadata.Name)
			if err != nil {
				out.DiscoveryMissing = missing(readReason(err), "Kubernetes child Task get")
				continue
			}
			child, err := decodeTask(checked, namespace)
			if err != nil || child.Metadata.UID != candidate.Metadata.UID || !delegated(child, parent) {
				out.DiscoveryMissing = missing("identity changed", "Kubernetes child Task get")
				continue
			}
			if len(seen) >= 100 {
				out.DiscoveryMissing = missing("Task limit reached", "Orka adapter")
				break
			}
			seen[child.Metadata.UID] = true
			children = append(children, child)
		}
		sort.Slice(children, func(i, j int) bool {
			a, b := children[i], children[j]
			if a.Metadata.CreationTimestamp.Equal(b.Metadata.CreationTimestamp) {
				return a.Metadata.UID < b.Metadata.UID
			}
			return a.Metadata.CreationTimestamp.Before(b.Metadata.CreationTimestamp)
		})
		for _, child := range children {
			out.HandOffs = append(out.HandOffs, runview.HandOff{FromTask: parent.Metadata.UID, ToTask: child.Metadata.UID, ObservedAt: child.Metadata.CreationTimestamp})
			parents[child.Metadata.UID] = parent.Metadata.UID
			queue = append(queue, child)
		}
	}
	return out, nil
}

type eventPage struct {
	Namespace  string `json:"namespace"`
	StreamType string `json:"streamType"`
	StreamID   string `json:"streamID"`
	LatestSeq  int64  `json:"latestSeq"`
	Events     []struct {
		Seq       int64     `json:"seq"`
		Type      string    `json:"type"`
		CreatedAt time.Time `json:"createdAt"`
	} `json:"events"`
}

func (r Reader) verifyTask(ctx context.Context, ns string, task nativeTask) *runview.Missing {
	raw, err := r.source.Task(ctx, ns, task.Metadata.Name)
	if err != nil {
		return missing(readReason(err), "Kubernetes Task get")
	}
	current, err := decodeTask(raw, ns)
	if err != nil || current.Metadata.UID != task.Metadata.UID {
		return missing("identity changed", "Kubernetes Task get")
	}
	return nil
}

func (r Reader) events(ctx context.Context, out *runview.Run, ns string, task nativeTask) {
	var cursor int64
	seen := map[int64]bool{}
	pageLimit := 100
	for len(seen) < 5000 {
		after := cursor
		if cursor > 0 && pageLimit > 1 {
			after--
		} // A one-record page has no room for both an overlap and progress.
		if check := r.verifyTask(ctx, ns, task); check != nil {
			out.EventsMissing = check
			return
		}
		raw, code, err := r.source.Events(ctx, ns, task.Metadata.Name, after, pageLimit)
		if errors.Is(err, ErrPageTooLarge) && pageLimit > 1 {
			pageLimit = max(1, pageLimit/2)
			continue
		}
		if err != nil {
			out.EventsMissing = missing(readReason(err), "Orka events")
			if errors.Is(err, ErrConnectionLost) {
				out.FreshnessMissing = missing("connection lost", "Orka API")
			}
			return
		}
		if check := r.verifyTask(ctx, ns, task); check != nil {
			out.EventsMissing = check
			return
		}
		if code != http.StatusOK {
			out.EventsMissing = missing(httpMissing(code), "Orka events")
			return
		}
		var page eventPage
		if json.Unmarshal(raw, &page) != nil || page.Namespace != ns || page.StreamType != "task" || page.StreamID != task.Metadata.Name || page.LatestSeq < cursor {
			out.EventsMissing = missing("invalid event page", "Orka events")
			return
		}
		var next int64 = cursor
		for _, e := range page.Events {
			if e.Seq <= 0 || e.Seq > page.LatestSeq {
				out.EventsMissing = missing("invalid sequence", "Orka events")
				return
			}
			if e.Seq > next {
				next = e.Seq
			}
			if seen[e.Seq] {
				continue
			}
			if e.Seq != int64(len(seen))+1 {
				out.EventsMissing = missing("history gap", "Orka events")
				return
			}
			seen[e.Seq] = true
			summary, status := eventProjection(e.Type)
			out.Events = append(out.Events, runview.Event{TaskID: task.Metadata.UID, Seq: e.Seq, At: e.CreatedAt, Summary: summary})
			if status != "" {
				out.StatusChanges = append(out.StatusChanges, runview.StatusChange{TaskID: task.Metadata.UID, Status: status, At: e.CreatedAt, Seq: e.Seq})
			}
		}
		if next == page.LatestSeq {
			if next == 0 && task.Status.Phase != "Pending" {
				out.EventsMissing = missing("no event history", "Orka events")
			}
			return
		}
		if next <= cursor {
			out.EventsMissing = missing("history gap", "Orka events")
			return
		}
		cursor = next
	}
	out.EventsMissing = missing("event limit reached", "Orka adapter")
}
func eventProjection(typ string) (string, string) {
	switch typ {
	case "TaskCreated":
		return "Task created", ""
	case "TaskStarted":
		return "Task started", "Running"
	case "TaskSucceeded":
		return "Task succeeded", "Succeeded"
	case "TaskFailed":
		return "Task failed", "Failed"
	case "TaskCancelled":
		return "Task cancelled", "Cancelled"
	case "WorkerStarted":
		return "Worker started", ""
	default:
		return "Activity recorded (content redacted)", ""
	}
}
