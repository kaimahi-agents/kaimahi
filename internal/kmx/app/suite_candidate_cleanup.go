package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"reflect"
	"strings"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

var errSuiteCandidateCleanup = errors.New("native candidate cleanup refused; known resources may remain")

// Cleanup proves positive root/child absence, not execution or export authority.
type suiteCandidateCleanupResult struct {
	AllGone                    bool
	Reason                     string
	Absent, ForeignOccupancies []suiteCandidateResource
}

const suiteCandidateCleanupBudget = 90 * time.Second
const suiteCandidateCleanupObjects = 512
const suiteCandidateCleanupRounds = 120
const suiteCandidateCleanupPageSize = 128
const suiteCandidateCleanupPages = 4
const suiteCandidateCleanupPageBytes = 4 << 20

// Disappearance polling projects metadata at kubectl. Child inventory uses
// bounded raw pages, then decodes only metadata: full server JSON exists only
// transiently in private memory, never in retained state or diagnostics.
const suiteCandidateCleanupMetadata = `{"name":{{printf "%q" .metadata.name}},"namespace":{{printf "%q" .metadata.namespace}},"uid":{{printf "%q" .metadata.uid}},"resourceVersion":{{printf "%q" .metadata.resourceVersion}},"generation":{{if .metadata.generation}}{{.metadata.generation}}{{else}}0{{end}},"taskLabel":{{if .metadata.labels}}{{if index .metadata.labels "orka.ai/task"}}{{printf "%q" (index .metadata.labels "orka.ai/task")}}{{else}}""{{end}}{{else}}""{{end}},"ownerReferences":[{{range $i,$o := .metadata.ownerReferences}}{{if $i}},{{end}}{"apiVersion":{{printf "%q" $o.apiVersion}},"kind":{{printf "%q" $o.kind}},"name":{{printf "%q" $o.name}},"uid":{{printf "%q" $o.uid}},"controller":{{if $o.controller}}true{{else}}false{{end}}}{{end}}]}`

type suiteCandidateCleanupMetadataRow struct {
	Name, Namespace, UID, ResourceVersion, TaskLabel string
	Generation                                       int64
	Labels                                           struct {
		Task string `json:"orka.ai/task"`
	}
	Owners []struct {
		APIVersion, Kind, Name, UID string
		Controller                  bool
	} `json:"ownerReferences"`
}

func (a *App) suiteCandidateCleanupScope(ctx context.Context, s suiteNativeSubject) error {
	if a.Cfg == nil || a.Cfg.KubeContext != s.Target.Context {
		return errSuiteCandidateCleanup
	}
	uid, terminating, err := a.readLiftClusterUID(ctx)
	if err != nil || terminating || uid != s.Target.ClusterUID {
		return errSuiteCandidateCleanup
	}
	raw, err := a.orkaCapture(ctx, nil, "get", "namespace", s.Target.Namespace, "--ignore-not-found=true", "-o", "json")
	var ns struct {
		Kind     string
		Metadata struct {
			Name, UID         string
			DeletionTimestamp *string
		}
	}
	if err != nil || json.Unmarshal(raw, &ns) != nil || ns.Kind != "Namespace" || ns.Metadata.Name != s.Target.Namespace || ns.Metadata.UID != s.Target.NamespaceUID || ns.Metadata.DeletionTimestamp != nil {
		return errSuiteCandidateCleanup
	}
	return nil
}

// Only this closed map can reach DELETE. Children are GC-only in this primitive.
func suiteCandidateCleanupPath(kind, ns, name string) string {
	if scaffold.ValidateNamespace(ns) != nil || scaffold.ValidateObjectName(name) != nil {
		return ""
	}
	plural := map[string]string{"Provider": "providers", "Agent": "agents", "Task": "tasks"}[kind]
	if plural == "" {
		return ""
	}
	return "/apis/core.orka.ai/v1alpha1/namespaces/" + ns + "/" + plural + "/" + name
}

func (a *App) suiteCandidateCleanupRoot(ctx context.Context, s suiteNativeSubject, r suiteCandidateResource) (map[string]any, bool, bool, error) {
	raw, err := a.orkaCapture(ctx, nil, "-n", s.Target.Namespace, "get", orkaPlural(r.Kind), r.Name, "--ignore-not-found=true", "-o", "json")
	if err != nil {
		return nil, false, false, errSuiteCandidateCleanup
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, true, false, nil
	}
	var doc map[string]any
	if !unambiguousSessionsReceiptJSON(raw) || json.Unmarshal(raw, &doc) != nil || doc == nil {
		return nil, false, false, errSuiteCandidateCleanup
	}
	meta, _ := doc["metadata"].(map[string]any)
	if doc["apiVersion"] != "core.orka.ai/v1alpha1" || doc["kind"] != r.Kind || meta["name"] != r.Name || meta["namespace"] != s.Target.Namespace {
		return nil, false, false, errSuiteCandidateCleanup
	}
	uid, _ := meta["uid"].(string)
	rv, _ := meta["resourceVersion"].(string)
	if !verificationIdentity(uid) || !verificationIdentity(rv) {
		return nil, false, false, errSuiteCandidateCleanup
	}
	if uid != r.UID {
		return nil, true, true, nil
	}
	ann, _ := meta["annotations"].(map[string]any)
	digest, err := suiteCandidateSpecDigest(raw)
	generation, _ := meta["generation"].(float64)
	// Kubernetes increments generation once when it timestamps deletion of a
	// finalized non-graceful resource. This permits waiting, never adoption or
	// a DELETE retry; all UID/spec/ownership/scope checks remain exact.
	terminating := meta["deletionTimestamp"] != nil
	if terminating {
		timestamp, ok := meta["deletionTimestamp"].(string)
		if !ok {
			return nil, false, false, errSuiteCandidateCleanup
		}
		if _, err := time.Parse(time.RFC3339Nano, timestamp); err != nil {
			return nil, false, false, errSuiteCandidateCleanup
		}
	}
	generationMatches := generation == float64(r.Generation) || terminating && generation == float64(r.Generation)+1
	if err != nil || digest != r.SpecDigest || !generationMatches || suiteNativeOwner(s).check(ann) != nil || !reflect.DeepEqual(suiteNativeOwner(s).markers(), suiteOnlyMarkers(ann)) {
		return nil, false, false, errSuiteCandidateCleanup
	}
	if r.Kind == "Task" {
		spec, _ := doc["spec"].(map[string]any)
		ref, _ := spec["agentRef"].(map[string]any)
		if spec["type"] != "ai" || ref["name"] != s.PhysicalMember || ref["namespace"] != s.Target.Namespace {
			return nil, false, false, errSuiteCandidateCleanup
		}
	}
	return doc, false, false, nil
}

func (a *App) suiteCandidateCleanupPollRoot(ctx context.Context, s suiteNativeSubject, r suiteCandidateResource) (bool, bool, error) {
	raw, err := a.orkaCapture(ctx, nil, "-n", s.Target.Namespace, "get", orkaPlural(r.Kind), r.Name, "--ignore-not-found=true", "-o", "go-template="+suiteCandidateCleanupMetadata)
	if err != nil {
		return false, false, errSuiteCandidateCleanup
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return true, false, nil
	}
	var row suiteCandidateCleanupMetadataRow
	if json.Unmarshal(raw, &row) != nil || !validSuiteCandidateCleanupRow(row, s.Target.Namespace) || row.Name != r.Name {
		return false, false, errSuiteCandidateCleanup
	}
	return row.UID != r.UID, row.UID != r.UID, nil
}
func validSuiteCandidateCleanupRow(row suiteCandidateCleanupMetadataRow, ns string) bool {
	return row.Namespace == ns && scaffold.ValidateObjectName(row.Name) == nil && verificationIdentity(row.UID) && verificationIdentity(row.ResourceVersion) && row.Generation >= 0 && len(row.Owners) <= 8
}

func (a *App) suiteCandidateCleanupInventory(ctx context.Context, s suiteNativeSubject, kind string) ([]suiteCandidateCleanupMetadataRow, error) {
	base := map[string]string{"Job": "/apis/batch/v1", "Pod": "/api/v1", "ConfigMap": "/api/v1"}[kind]
	plural := map[string]string{"Job": "jobs", "Pod": "pods", "ConfigMap": "configmaps"}[kind]
	version := "v1"
	if kind == "Job" {
		version = "batch/v1"
	}
	if base == "" || scaffold.ValidateNamespace(s.Target.Namespace) != nil {
		return nil, errSuiteCandidateCleanup
	}
	path := base + "/namespaces/" + s.Target.Namespace + "/" + plural + "?limit=128"
	rows := []suiteCandidateCleanupMetadataRow{}
	seenUID, seenName, seenToken := map[string]bool{}, map[string]bool{}, map[string]bool{}
	continuation, snapshotRV := "", ""
	for page := 0; page < suiteCandidateCleanupPages; page++ {
		requestPath := path
		if continuation != "" {
			requestPath += "&continue=" + url.QueryEscape(continuation)
		}
		raw, err := a.orkaCapture(ctx, nil, "get", "--raw", requestPath)
		var list struct {
			APIVersion, Kind string
			Metadata         struct{ Continue, ResourceVersion string }
			Items            []struct {
				Metadata suiteCandidateCleanupMetadataRow
			}
		}
		if err != nil || len(raw) > suiteCandidateCleanupPageBytes || !suiteCandidateCleanupPageJSON(raw) || json.Unmarshal(raw, &list) != nil || list.APIVersion != version || list.Kind != kind+"List" || !verificationIdentity(list.Metadata.ResourceVersion) || list.Items == nil || len(list.Items) > suiteCandidateCleanupPageSize {
			return nil, errSuiteCandidateCleanup
		}
		if snapshotRV != "" && list.Metadata.ResourceVersion != snapshotRV {
			return nil, errSuiteCandidateCleanup
		}
		snapshotRV = list.Metadata.ResourceVersion
		for _, item := range list.Items {
			row := item.Metadata
			row.TaskLabel = row.Labels.Task
			if !validSuiteCandidateCleanupRow(row, s.Target.Namespace) || seenUID[row.UID] || seenName[row.Name] {
				return nil, errSuiteCandidateCleanup
			}
			seenUID[row.UID], seenName[row.Name] = true, true
			rows = append(rows, row)
		}
		continuation = list.Metadata.Continue
		if continuation == "" {
			return rows, nil
		}
		if len(continuation) > 4096 || seenToken[continuation] {
			return nil, errSuiteCandidateCleanup
		}
		seenToken[continuation] = true
	}
	return nil, errSuiteCandidateCleanup // Never treat a partial inventory as complete.
}

// Only consumed page/envelope/ownership keys need ambiguity checks. Kubernetes
// managedFields, arbitrary labels/annotations and workload/data payloads are
// opaque here: legitimate case-distinct keys and wide objects are not authority.
// Still walk the entire bounded JSON page to enforce syntax and depth <=32.
func suiteCandidateCleanupPageJSON(raw []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var scan func(int, string) bool
	scan = func(depth int, scope string) bool {
		if depth > 32 {
			return false
		}
		token, err := decoder.Token()
		if err != nil {
			return false
		}
		delimiter, container := token.(json.Delim)
		if !container {
			return true
		}
		if delimiter == '[' {
			itemScope := ""
			switch scope {
			case "items":
				itemScope = "item"
			case "ownerReferences":
				itemScope = "owner"
			}
			for decoder.More() {
				if !scan(depth+1, itemScope) {
					return false
				}
			}
			closing, err := decoder.Token()
			return err == nil && closing == json.Delim(']')
		}
		if delimiter != '{' {
			return false
		}
		var consumed []string
		switch scope {
		case "page":
			consumed = []string{"apiVersion", "kind", "metadata", "items"}
		case "listMetadata":
			consumed = []string{"resourceVersion", "continue"}
		case "item":
			consumed = []string{"metadata"}
		case "objectMetadata":
			consumed = []string{"name", "namespace", "uid", "resourceVersion", "generation", "labels", "ownerReferences"}
		case "labels":
			consumed = []string{"orka.ai/task"}
		case "owner":
			consumed = []string{"apiVersion", "kind", "name", "uid", "controller"}
		}
		seen := make(map[string]bool, len(consumed))
		for decoder.More() {
			token, err := decoder.Token()
			key, ok := token.(string)
			if err != nil || !ok {
				return false
			}
			nextScope := ""
			for _, field := range consumed {
				if !strings.EqualFold(key, field) {
					continue
				}
				if key != field || seen[field] {
					return false
				}
				seen[field] = true
				switch {
				case scope == "page" && field == "metadata":
					nextScope = "listMetadata"
				case scope == "item" && field == "metadata":
					nextScope = "objectMetadata"
				case field == "items":
					nextScope = "items"
				case scope == "objectMetadata" && field == "labels":
					nextScope = "labels"
				case scope == "objectMetadata" && field == "ownerReferences":
					nextScope = "ownerReferences"
				}
				break
			}
			if !scan(depth+1, nextScope) {
				return false
			}
		}
		closing, err := decoder.Token()
		return err == nil && closing == json.Delim('}')
	}
	if !scan(0, "page") {
		return false
	}
	_, err := decoder.Token()
	return err == io.EOF
}

// Discover only by an exact typed UID+name controller-owner link. Labels can
// only make an otherwise unanchored potential child uncertain, never owned.
func (a *App) suiteCandidateCleanupChildren(ctx context.Context, r *suiteCandidateRun, known *[]suiteCandidateResource) (bool, error) {
	gone := true
	for _, kind := range []string{"Job", "ConfigMap", "Pod"} {
		rows, err := a.suiteCandidateCleanupInventory(ctx, r.subject, kind)
		if err != nil {
			return false, err
		}
		for _, row := range rows {
			parentKind := "Task"
			parentVersion := "core.orka.ai/v1alpha1"
			if kind == "Pod" {
				parentKind = "Job"
				parentVersion = "batch/v1"
			}
			var parent *suiteCandidateResource
			for _, owner := range row.Owners {
				for i := range *known {
					p := &(*known)[i]
					if owner.UID == p.UID && owner.Kind == p.Kind && p.Kind == parentKind {
						if owner.APIVersion != parentVersion || owner.Name != p.Name || !owner.Controller || len(row.Owners) != 1 {
							return false, errSuiteCandidateCleanup
						}
						parent = p
					}
				}
			}
			if parent == nil {
				for _, p := range *known {
					if p.Kind == "Task" && row.TaskLabel == p.Name {
						return false, errSuiteCandidateCleanup
					}
				}
				continue
			}
			gone = false
			found := false
			for _, old := range *known {
				if old.Kind == kind && (old.Name == row.Name || old.UID == row.UID) {
					if old.Name != row.Name || old.UID != row.UID || old.ParentUID != parent.UID {
						return false, errSuiteCandidateCleanup
					}
					found = true
				}
			}
			if found {
				continue
			}
			if len(*known) >= suiteCandidateCleanupObjects {
				return false, errSuiteCandidateCleanup
			}
			// Full raw payloads exist transiently at the bounded transport boundary;
			// retained child metadata excludes spec/data/status.
			// This hash binds ownership metadata, not workload/data erasure.
			digest, err := suiteNativeDigest("kmx.native-orka.cleanup-child/v1", row)
			if err != nil {
				return false, errSuiteCandidateCleanup
			}
			child := suiteCandidateResource{Kind: kind, Name: row.Name, UID: row.UID, Generation: row.Generation, SpecDigest: digest, ParentKind: parent.Kind, ParentUID: parent.UID}
			*known = append(*known, child)
			r.acknowledged = append(r.acknowledged, child)
		}
		// A retained child's UID must be absent, not merely missing its parent.
		for _, child := range *known {
			if child.Kind != kind {
				continue
			}
			for _, row := range rows {
				if row.UID == child.UID {
					gone = false
				}
				if row.Name == child.Name && row.UID != child.UID {
					return false, errSuiteCandidateCleanup
				}
			}
		}
	}
	return gone, nil
}

// Orchestration supplies a frozen guarded worker and a live, detached bounded
// context. No source check,
// retarget, completion, export, unknown-UID recovery, retry or forced deletion.
func (a *App) cleanupSuiteNativeCandidate(ctx context.Context, r *suiteCandidateRun) (result suiteCandidateCleanupResult, err error) {
	result.Reason = "invalid authority"
	if a == nil || a.Cfg == nil || a.Run == nil || !a.guarded || r == nil || r.cleanupStarted || !validSuitePassingSubject(r.subject) || r.subject.EvaluationRun == "" {
		return result, errSuiteCandidateCleanup
	}
	ctx, cancel := context.WithTimeout(ctx, suiteCandidateCleanupBudget)
	defer cancel()
	worker := a.withRunContext(ctx)
	// Freeze the concrete configuration copy, not a saved destination reread.
	copyApp := *worker
	cfg := *worker.Cfg
	copyApp.Cfg = &cfg
	worker = &copyApp
	r.stopped = true
	r.cleanupStarted = true // A failed/ambiguous DELETE must never be automatically replayed.
	fail := func(reason string) (suiteCandidateCleanupResult, error) {
		result.Reason = reason
		return result, errSuiteCandidateCleanup
	}
	known := []suiteCandidateResource{}
	unknown := false
	for _, resource := range r.Resources() {
		if resource.ParentKind != "" {
			continue
		}
		if !validCandidateRootResource(resource, r.subject) {
			return fail("invalid known metadata")
		}
		if resource.UID == "" {
			unknown = true
			continue
		}
		acknowledged := false
		for _, id := range r.acknowledged {
			if reflect.DeepEqual(id, resource) {
				acknowledged = true
			}
		}
		if !acknowledged {
			return fail("missing independent acknowledgement")
		}
		for _, old := range known {
			if old.UID == resource.UID || old.Kind == resource.Kind && old.Name == resource.Name {
				return fail("duplicate identity")
			}
		}
		known = append(known, resource)
	}
	roots := append([]suiteCandidateResource{}, known...)
	for _, child := range r.Resources() {
		if child.ParentKind != "" {
			if !validCandidateChild(child, known) {
				return fail("invalid retained child chain")
			}
			known = append(known, child)
		}
	}
	if len(known) > suiteCandidateCleanupObjects {
		return fail("inventory bound")
	}
	if worker.suiteCandidateCleanupScope(ctx, r.subject) != nil {
		return fail("scope changed or unknown")
	}
	// Validate every known root before the first effect; skip unknown intents.
	for _, root := range roots {
		_, absent, foreign, readErr := worker.suiteCandidateCleanupRoot(ctx, r.subject, root)
		if readErr != nil {
			return fail("root changed or read unknown")
		}
		if absent {
			result.Absent = append(result.Absent, root)
		}
		if foreign {
			result.ForeignOccupancies = append(result.ForeignOccupancies, root)
			return fail("owned UID absent; foreign name occupied")
		}
	}
	hasTasks := false
	for _, root := range roots {
		if root.Kind == "Task" {
			hasTasks = true
		}
	}
	if hasTasks {
		_, inventoryErr := worker.suiteCandidateCleanupChildren(ctx, r, &known)
		if inventoryErr != nil {
			return fail("child closure unknown")
		}
	}
	for _, kind := range []string{"Task", "Agent", "Provider"} {
		for _, root := range roots {
			if root.Kind != kind {
				continue
			}
			if worker.suiteCandidateCleanupScope(ctx, r.subject) != nil {
				return fail("scope changed or unknown")
			}
			doc, absent, foreign, readErr := worker.suiteCandidateCleanupRoot(ctx, r.subject, root)
			if readErr != nil {
				return fail("root changed or read unknown")
			}
			if foreign {
				result.ForeignOccupancies = append(result.ForeignOccupancies, root)
				return fail("owned UID absent; foreign name occupied")
			}
			if !absent {
				// Recheck scope after the fresh spec/RV read, immediately before
				// the DELETE effect. Resource preconditions then close the RV race.
				if worker.suiteCandidateCleanupScope(ctx, r.subject) != nil {
					return fail("scope changed or unknown")
				}
				meta := doc["metadata"].(map[string]any)
				if meta["deletionTimestamp"] == nil {
					body, marshalErr := json.Marshal(map[string]any{"apiVersion": "v1", "kind": "DeleteOptions", "preconditions": map[string]any{"uid": root.UID, "resourceVersion": meta["resourceVersion"]}, "propagationPolicy": "Foreground"})
					if marshalErr != nil {
						return fail("invalid delete options")
					}
					raw, deleteErr := worker.orkaCapture(ctx, body, "delete", "--raw", suiteCandidateCleanupPath(root.Kind, r.subject.Target.Namespace, root.Name), "-f", "-")
					var ack map[string]any
					if deleteErr != nil || !unambiguousSessionsReceiptJSON(raw) || json.Unmarshal(raw, &ack) != nil || ack == nil {
						return fail("delete conflict or acknowledgement unknown")
					}
					ackMeta, _ := ack["metadata"].(map[string]any)
					statusAck := ack["apiVersion"] == "v1" && ack["kind"] == "Status" && ack["status"] == "Success"
					objectAck := ack["apiVersion"] == "core.orka.ai/v1alpha1" && ack["kind"] == root.Kind && ackMeta["name"] == root.Name && ackMeta["namespace"] == r.subject.Target.Namespace && ackMeta["uid"] == root.UID
					if !statusAck && !objectAck {
						return fail("delete conflict or acknowledgement unknown")
					}
				}
				for round := 0; round < suiteCandidateCleanupRounds; round++ {
					if worker.suiteCandidateCleanupScope(ctx, r.subject) != nil {
						return fail("scope changed or unknown")
					}
					absent, foreign, readErr = worker.suiteCandidateCleanupPollRoot(ctx, r.subject, root)
					if foreign {
						result.ForeignOccupancies = append(result.ForeignOccupancies, root)
						return fail("owned UID absent; foreign name occupied")
					}
					if readErr != nil {
						return fail("disappearance read unknown")
					}
					if hasTasks {
						_, inventoryErr := worker.suiteCandidateCleanupChildren(ctx, r, &known)
						if inventoryErr != nil {
							return fail("child closure unknown")
						}
					}
					if absent {
						break
					}
					if !suiteCandidateCleanupWait(ctx) {
						return fail("cleanup budget exhausted")
					}
				}
				if !absent {
					return fail("poll bound exhausted")
				}
				result.Absent = append(result.Absent, root)
			}
		}
		// Task404 alone is insufficient: background Job deletion can orphan Pods.
		if kind == "Task" && hasTasks {
			cleanRounds := 0
			for round := 0; round < suiteCandidateCleanupRounds; round++ {
				if worker.suiteCandidateCleanupScope(ctx, r.subject) != nil {
					return fail("scope changed or unknown")
				}
				gone, inventoryErr := worker.suiteCandidateCleanupChildren(ctx, r, &known)
				if inventoryErr != nil {
					return fail("child closure unknown")
				}
				if gone {
					cleanRounds++
				} else {
					cleanRounds = 0
				}
				if cleanRounds == 2 {
					break
				}
				if !suiteCandidateCleanupWait(ctx) {
					return fail("cleanup budget exhausted")
				}
			}
			if cleanRounds < 2 {
				return fail("child closure poll bound exhausted")
			}
		}
	}
	// Reverify the final closure, not a snapshot taken before Agent/Provider
	// deletion; controller children may have appeared during those requests.
	if hasTasks {
		cleanRounds := 0
		for round := 0; round < suiteCandidateCleanupRounds; round++ {
			if worker.suiteCandidateCleanupScope(ctx, r.subject) != nil {
				return fail("scope changed or unknown")
			}
			gone, inventoryErr := worker.suiteCandidateCleanupChildren(ctx, r, &known)
			if inventoryErr != nil {
				return fail("child closure unknown")
			}
			if gone {
				cleanRounds++
			} else {
				cleanRounds = 0
			}
			if cleanRounds == 2 {
				break
			}
			if !suiteCandidateCleanupWait(ctx) {
				return fail("cleanup budget exhausted")
			}
		}
		if cleanRounds < 2 {
			return fail("child closure poll bound exhausted")
		}
	}
	if worker.suiteCandidateCleanupScope(ctx, r.subject) != nil {
		return fail("scope changed or unknown")
	}
	for _, root := range roots {
		gone, foreign, readErr := worker.suiteCandidateCleanupPollRoot(ctx, r.subject, root)
		if foreign {
			result.ForeignOccupancies = append(result.ForeignOccupancies, root)
			return fail("owned UID absent; foreign name occupied")
		}
		if readErr != nil || !gone {
			return fail("final root absence unknown")
		}
	}
	if unknown {
		return fail("unknown UID intent remains")
	}
	if ctx.Err() != nil {
		return fail("cleanup budget exhausted")
	}
	result.AllGone = true
	result.Reason = "verified absent"
	return result, nil
}
func suiteCandidateCleanupWait(ctx context.Context) bool {
	timer := time.NewTimer(500 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
