package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	agentsuite "github.com/agent-primitives/suite"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

const maxReleaseStateBytes = 64 << 10

var errReleaseCreateOccupied = errors.New("release record already exists; plain Lift never replaces or deletes an existing placement; inspect the retained record and owned pair, or select a fresh destination (docs/suite-workspace.md)")

var errReleaseStateRBAC = errors.New("release record RBAC unavailable; require namespace-local ConfigMap get/create/patch permission before retrying; no placement writes attempted")

var errReleaseStateUnavailable = errors.New("release record read unavailable; check ConfigMap get access and API connectivity; no state inferred")

var errReleaseStateWrite = errors.New("conditional release-record write conflicted or acknowledgment is unknown; do not resend; inspect the current reservation and object outcomes; wait for an active update, never take over an unresolved one")

var errReleaseOperationInProgress = errors.New("refused: operation-in-progress; stop the original process before documented operator recovery")

var errReleaseState = errors.New("release state unavailable or unknown; stop the original process, inspect the owned Provider/Agent pair and release ConfigMap, then explicitly tear down that pair and record before recreating (docs/suite-workspace.md)")

// These are fingerprints and acknowledged identities, not specs, receipt bodies
// or recovery authorization. Original private create proof remains separate.
type releaseStateObject struct {
	Kind             string `json:"kind"`
	UID              string `json:"uid,omitempty"`
	Generation       int64  `json:"generation,omitempty"`
	SpecDigest       string `json:"specDigest"`
	OwnershipDigest  string `json:"ownershipDigest"`
	LiftRecordDigest string `json:"liftRecordDigest"`
}

type releaseStateRevision struct {
	LiftID         string               `json:"liftID"`
	SuiteDigest    string               `json:"suiteDigest"`
	EvidenceDigest string               `json:"evidenceDigest"`
	State          string               `json:"state"`
	Before         []releaseStateObject `json:"before,omitempty"`
	Objects        []releaseStateObject `json:"objects"`
}

type releaseState struct {
	Version      int                    `json:"version"`
	Suite        string                 `json:"suite"`
	Member       string                 `json:"member"`
	Runtime      string                 `json:"runtime"`
	Target       ReleaseLiftTarget      `json:"target"`
	OriginLiftID string                 `json:"originLiftID"`
	Operation    string                 `json:"operation,omitempty"`
	Baseline     []releaseStateObject   `json:"baseline,omitempty"`
	Pending      *releaseStateRevision  `json:"pending,omitempty"`
	History      []releaseStateRevision `json:"history,omitempty"`
}

type releaseStateSnapshot struct {
	UID, ResourceVersion string
	State                releaseState
	data                 string
}

func releaseStateName(suite, member, runtime string) string {
	sum := sha256.Sum256([]byte("kmx.release-state/v1\x00" + suite + "\x00" + member + "\x00" + runtime))
	return "kmx-release-" + hex.EncodeToString(sum[:20])
}

func validReleaseStateObjects(objects []releaseStateObject, acknowledged bool) bool {
	if len(objects) != 2 {
		return false
	}
	for i, object := range objects {
		if object.Kind != []string{"Provider", "Agent"}[i] || !agentsuite.IsValidDigest(object.SpecDigest) ||
			!agentsuite.IsValidDigest(object.OwnershipDigest) || !agentsuite.IsValidDigest(object.LiftRecordDigest) ||
			object.Generation < 0 || (object.UID == "") != (object.Generation == 0) {
			return false
		}
		if object.UID != "" && (!statusOpaque(object.UID) || len(object.UID) > 128) || acknowledged && object.UID == "" {
			return false
		}
	}
	return true
}

func validReleaseStateRevision(r releaseStateRevision, pending bool) bool {
	if !evaluationRunID(r.LiftID) || !agentsuite.IsValidDigest(r.SuiteDigest) || !agentsuite.IsValidDigest(r.EvidenceDigest) ||
		pending && r.State != "pending" || !pending && r.State != "completed" && r.State != "partial" {
		return false
	}
	return validReleaseStateObjects(r.Objects, !pending) && (len(r.Before) == 0 || validReleaseStateObjects(r.Before, true))
}

func validReleaseState(s releaseState) bool {
	if s.Version != 1 || !validReleaseStateIdentity(s) || !evaluationRunID(s.OriginLiftID) ||
		len(s.Baseline) != 0 && !validReleaseStateObjects(s.Baseline, true) {
		return false
	}
	if s.Pending == nil {
		if s.Operation != "" || len(s.Baseline) == 0 {
			return false
		}
	} else if !validReleaseStateRevision(*s.Pending, true) || s.Operation != s.Pending.LiftID ||
		len(s.Baseline) == 0 && s.Pending.LiftID != s.OriginLiftID {
		return false
	}
	seen := map[string]bool{}
	if s.Pending != nil {
		seen[s.Pending.LiftID] = true
	}
	for _, r := range s.History {
		if !validReleaseStateRevision(r, false) || seen[r.LiftID] {
			return false
		}
		seen[r.LiftID] = true
	}
	return true
}

func releaseStateBytes(s releaseState) ([]byte, error) {
	if !validReleaseState(s) {
		return nil, errReleaseState
	}
	completed := 0
	for _, r := range s.History {
		if r.State == "completed" {
			completed++
		}
	}
	// Only terminal completed observations roll. A partial observation and the
	// unresolved attempt never compete with completed history for a slot.
	history := make([]releaseStateRevision, 0, len(s.History))
	for _, r := range s.History {
		if completed > 10 && r.State == "completed" {
			completed--
			continue
		}
		history = append(history, r)
	}
	s.History = history
	raw, err := agentsuite.Marshal(s)
	if err != nil || len(raw) > maxReleaseStateBytes {
		return nil, errReleaseState
	}
	return raw, nil
}

func validReleaseStateIdentity(s releaseState) bool {
	return scaffold.ValidateObjectName(s.Suite) == nil && scaffold.ValidateObjectName(s.Member) == nil &&
		s.Runtime == "orka" && statusTargetValid(s.Target)
}

func sameReleaseStateIdentity(a, b releaseState) bool {
	return a.Suite == b.Suite && a.Member == b.Member && a.Runtime == b.Runtime && statusSameTarget(a.Target, b.Target)
}

func decodeReleaseState(raw []byte, expected releaseState) (*releaseStateSnapshot, error) {
	if len(raw) > 2*maxReleaseStateBytes {
		return nil, errReleaseState
	}
	var object map[string]any
	if !statusDecode(raw, &object) || object["apiVersion"] != "v1" || object["kind"] != "ConfigMap" {
		return nil, errReleaseState
	}
	meta := statusMap(object, "metadata")
	labels := statusMap(meta, "labels")
	payload := statusMap(object, "data")
	binary, _ := object["binaryData"].(map[string]any)
	uid, rv := statusString(meta, "uid"), statusString(meta, "resourceVersion")
	if statusString(meta, "name") != releaseStateName(expected.Suite, expected.Member, expected.Runtime) || statusString(meta, "namespace") != expected.Target.Namespace ||
		!statusOpaque(uid) || !statusOpaque(rv) || meta["deletionTimestamp"] != nil ||
		labels["app.kubernetes.io/managed-by"] != "kmx" || labels["kaimahi.dev/release-state"] != "v1" ||
		len(payload) != 1 || len(binary) != 0 || object["binaryData"] != nil && binary == nil || object["immutable"] != nil && object["immutable"] != false {
		return nil, errReleaseState
	}
	data := []byte(statusString(payload, "release.json"))
	var state releaseState
	if len(data) > maxReleaseStateBytes || agentsuite.Unmarshal(data, &state) != nil || !validReleaseState(state) || !sameReleaseStateIdentity(state, expected) {
		return nil, errReleaseState
	}
	completed := 0
	for _, r := range state.History {
		if r.State == "completed" {
			completed++
		}
	}
	if completed > 10 {
		return nil, fmt.Errorf("%w: completed history exceeds its bound", errReleaseState)
	}
	return &releaseStateSnapshot{uid, rv, state, string(data)}, nil
}

func (a *App) readReleaseState(ctx context.Context, expected releaseState) (*releaseStateSnapshot, error) {
	if a == nil || a.Cfg == nil || a.Cfg.KubeContext == "" || !validReleaseStateIdentity(expected) {
		return nil, errReleaseState
	}
	raw, err := a.orkaCapture(ctx, nil, "-n", expected.Target.Namespace, "get", "configmap", releaseStateName(expected.Suite, expected.Member, expected.Runtime),
		"--ignore-not-found", "-o", "json")
	if err != nil {
		return nil, errors.Join(errReleaseStateUnavailable, err)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	return decodeReleaseState(raw, expected)
}

// A successful permission probe is not authority or a reservation. The guarded
// caller still creates one fresh record, never retries or adopts an occupied key.
func (a *App) preflightReleaseStateCreate(ctx context.Context, expected releaseState) error {
	record, err := a.readReleaseState(ctx, expected)
	if err != nil {
		if errors.Is(err, errReleaseStateUnavailable) {
			return fmt.Errorf("record preflight refused before placement writes: %w", err)
		}
		return err
	}
	if record != nil {
		if record.State.Operation != "" {
			return errReleaseOperationInProgress
		}
		return errReleaseCreateOccupied
	}
	for _, verb := range []string{"get", "create", "patch"} {
		resource := "configmaps"
		if verb != "create" {
			resource += "/" + releaseStateName(expected.Suite, expected.Member, expected.Runtime)
		}
		raw, err := a.orkaCapture(ctx, nil, "-n", expected.Target.Namespace, "auth", "can-i", verb, resource)
		if err != nil || strings.TrimSpace(string(raw)) != "yes" {
			return errors.Join(errReleaseStateRBAC, ctx.Err())
		}
	}
	return nil
}

func (a *App) createReleaseState(ctx context.Context, state releaseState) (*releaseStateSnapshot, error) {
	if a == nil || a.Cfg == nil || a.Cfg.KubeContext == "" || len(state.Baseline) != 0 || len(state.History) != 0 ||
		state.Pending == nil || state.Operation != state.OriginLiftID {
		return nil, errReleaseState
	}
	raw, err := releaseStateBytes(state)
	if err != nil {
		return nil, err
	}
	for _, object := range state.Pending.Objects {
		if object.UID != "" || object.Generation != 0 {
			return nil, errReleaseState
		}
	}
	doc := map[string]any{"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]any{"name": releaseStateName(state.Suite, state.Member, state.Runtime), "namespace": state.Target.Namespace,
			"labels": map[string]any{"app.kubernetes.io/managed-by": "kmx", "kaimahi.dev/release-state": "v1"}},
		"data": map[string]any{"release.json": string(raw)}}
	body, err := json.Marshal(doc)
	if err != nil {
		return nil, errReleaseState
	}
	response, err := a.orkaCapture(ctx, body, "-n", state.Target.Namespace, "create", "-f", "-", "--validate=strict", "-o", "json")
	if err != nil {
		return nil, errors.Join(errReleaseState, err)
	}
	created, err := decodeReleaseState(response, state)
	if err != nil || !bytes.Equal([]byte(created.data), raw) {
		return nil, errReleaseState
	}
	return created, nil
}

// The caller supplies its own invocation ID, never an observed owner's ID.
// This primitive does not grant mutation authority or resolve an unknown write.
// On a rejected/ambiguous write, callers may inspect the reservation read-only
// to report another active operation, never infer success or resend the write.
func (a *App) writeReleaseState(ctx context.Context, before *releaseStateSnapshot, next releaseState, operation string) (*releaseStateSnapshot, error) {
	if a == nil || a.Cfg == nil || a.Cfg.KubeContext == "" || before == nil || !evaluationRunID(operation) ||
		!sameReleaseStateIdentity(before.State, next) || before.State.OriginLiftID != next.OriginLiftID {
		return nil, errReleaseState
	}
	if before.State.Operation != "" && before.State.Operation != operation {
		return nil, fmt.Errorf("%w: %s", errReleaseOperationInProgress, before.State.Operation)
	}
	if next.Operation != "" && next.Operation != operation || next.Operation == "" && before.State.Operation != operation {
		return nil, errReleaseState
	}
	raw, err := releaseStateBytes(next)
	if err != nil {
		return nil, err
	}
	// Test the entire observed payload too: the reservation and baseline are
	// one CAS, not separate GET/check/write decisions across clients.
	ops := []map[string]any{
		{"op": "test", "path": "/metadata/uid", "value": before.UID},
		{"op": "test", "path": "/metadata/resourceVersion", "value": before.ResourceVersion},
		{"op": "test", "path": "/data/release.json", "value": before.data},
		{"op": "replace", "path": "/data/release.json", "value": string(raw)},
	}
	body, err := json.Marshal(ops)
	if err != nil {
		return nil, errReleaseState
	}
	response, err := a.orkaCapture(ctx, body, "-n", next.Target.Namespace, "patch", "configmap", releaseStateName(next.Suite, next.Member, next.Runtime),
		"--type=json", "--patch-file=/dev/stdin", "-o", "json")
	if err != nil {
		return nil, errors.Join(errReleaseStateWrite, err)
	}
	written, err := decodeReleaseState(response, next)
	if err != nil || written.UID != before.UID || !bytes.Equal([]byte(written.data), raw) {
		return nil, errReleaseStateWrite
	}
	return written, nil
}
