package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/fileaccess"
)

const modelStagePrefix = ".model-stage-"
const modelStateLimit = 64 << 10

var errModelStateEncoding = errors.New("model state cannot be represented")
var modelTargetPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var modelCheckpointPattern = regexp.MustCompile(`^[0-9]{6}\.json$`)

type modelOwnedResource struct {
	APIVersion      string         `json:"apiVersion"`
	Kind            string         `json:"kind"`
	Name            string         `json:"name"`
	UID             string         `json:"uid,omitempty"`
	ResourceVersion string         `json:"resourceVersion,omitempty"`
	Marker          string         `json:"marker"`
	Purpose         string         `json:"purpose"`
	Generation      int64          `json:"generation,omitempty"`
	Spec            map[string]any `json:"spec,omitempty"`
}
type modelPendingAction struct {
	OperationID string             `json:"operationID"`
	Verb        string             `json:"verb"`
	Resource    modelOwnedResource `json:"resource"`
}
type modelCheckpoint struct {
	Version      int                  `json:"version"`
	ClusterUID   string               `json:"clusterUID"`
	NamespaceUID string               `json:"namespaceUID"`
	Connection   OrkaModelConnection  `json:"connection"`
	Pending      *modelPendingAction  `json:"pending,omitempty"`
	Resources    []modelOwnedResource `json:"resources,omitempty"`
	Complete     bool                 `json:"complete"`
}
type modelAnchor struct {
	Version int             `json:"version"`
	Target  string          `json:"target"`
	Initial modelCheckpoint `json:"initial"`
}
type modelRecord struct {
	target           string
	dir, family      *fileaccess.Directory
	lock, anchor     *fileaccess.PrivateFile
	initial, state   modelCheckpoint
	next             int
	anchored, failed bool
	publish          func(context.Context, *fileaccess.Directory, string, []byte) (fileaccess.PublicationResult, error)
}

func openModelRecord(ctx context.Context, targetKey string, create bool) (record *modelRecord, err error) {
	if err = ctx.Err(); err != nil {
		return nil, modelStateError(err)
	}
	if !modelTargetPattern.MatchString(targetKey) {
		return nil, modelStateError(errors.New("invalid model target"))
	}
	var root string
	if create {
		root, err = config.PrepareStateDir()
	} else {
		root, err = config.ExistingStateDir()
	}
	if err != nil {
		return nil, modelStateError(err)
	}
	parent, err := fileaccess.OpenPrivateDirectory(ctx, root)
	if err != nil {
		return nil, modelStateError(err)
	}
	defer parent.Close()
	child := fileaccess.OpenDirectoryAt
	if create {
		child = fileaccess.EnsurePrivateDirectoryAt
	}
	base, err := child(ctx, parent, "orka-models")
	if err != nil {
		return nil, modelStateError(err)
	}
	defer base.Close()
	if err := fileaccess.CheckPrivate(ctx, base, true); err != nil {
		return nil, modelStateError(err)
	}
	dir, err := child(ctx, base, targetKey)
	if err != nil {
		return nil, modelStateError(err)
	}
	r := &modelRecord{target: targetKey, dir: dir, publish: publishModelState}
	defer func() {
		if err != nil {
			_ = r.close()
			err = modelStateError(err)
		}
	}()
	if err = fileaccess.CheckPrivate(ctx, dir, true); err != nil {
		return nil, err
	}
	if create {
		r.lock, err = fileaccess.EnsurePrivateFileAt(ctx, dir, "lock")
	} else {
		r.lock, err = fileaccess.OpenPrivateFileAt(ctx, dir, "lock")
	}
	if err != nil {
		return nil, err
	}
	if err = fileaccess.TryLock(ctx, r.lock); err != nil {
		return nil, err
	}
	r.anchor, err = fileaccess.OpenPrivateFileAt(ctx, dir, "connection.json")
	if errors.Is(err, os.ErrNotExist) && create {
		return r, nil
	}
	if err != nil {
		return nil, err
	}
	var anchor modelAnchor
	if err = decodeModelState(r.anchor, &anchor); err != nil {
		return nil, err
	}
	if anchor.Version != 1 || anchor.Target != targetKey {
		return nil, errors.New("invalid model anchor")
	}
	if err = validateModelCheckpoint(anchor.Initial); err != nil {
		return nil, err
	}
	r.anchored = true
	r.initial = anchor.Initial
	r.family, err = fileaccess.OpenDirectoryAt(ctx, dir, anchor.Initial.Connection.ID)
	if err != nil {
		return nil, err
	}
	if err = fileaccess.CheckPrivate(ctx, r.family, true); err != nil {
		return nil, err
	}
	entries, err := r.family.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	checkpoints := entries[:0]
	for _, entry := range entries {
		// An interrupted named stage is diagnostic only: never read, adopt or
		// delete it. Only numbered publications participate in the fold.
		if entry.Type().IsRegular() && strings.HasPrefix(entry.Name(), modelStagePrefix) && modelUUID(strings.TrimPrefix(entry.Name(), modelStagePrefix)) {
			continue
		}
		checkpoints = append(checkpoints, entry)
	}
	entries = checkpoints
	if len(entries) == 0 {
		// Missing committed history is not proof that no effects ever ran.
		return nil, errors.New("model family has no acknowledged checkpoint")
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for i, entry := range entries {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		if entry.IsDir() || !modelCheckpointPattern.MatchString(entry.Name()) || entry.Name() != fmt.Sprintf("%06d.json", i) {
			return nil, errors.New("invalid model checkpoint sequence")
		}
		var state modelCheckpoint
		f, openErr := fileaccess.OpenPrivateFileAt(ctx, r.family, entry.Name())
		if openErr != nil {
			return nil, openErr
		}
		err = decodeModelState(f, &state)
		err = errors.Join(err, f.Close())
		if err != nil {
			return nil, err
		}
		if err = r.validate(state); err != nil {
			return nil, err
		}
		r.state = state
		r.next++
	}
	return r, nil
}

func (r *modelRecord) checkpoint(ctx context.Context, state modelCheckpoint) error {
	if r.failed {
		return modelStateError(errors.New("previous model publication unresolved"))
	}
	if err := ctx.Err(); err != nil {
		return modelStateError(err)
	}
	if err := r.validate(state); err != nil {
		return modelStateError(err)
	}
	data, err := encodeModelState(state)
	if err != nil {
		return modelStateError(err)
	}
	var anchor []byte
	if !r.anchored {
		anchor, err = encodeModelState(modelAnchor{Version: 1, Target: r.target, Initial: state})
		if err != nil {
			return modelStateError(err)
		}
	}
	fail := func(err error) error { r.failed = true; return modelStateError(err) }
	if !r.anchored {
		r.initial = state
		r.family, err = fileaccess.CreatePrivateDirectoryAt(ctx, r.dir, state.Connection.ID)
		if err != nil {
			return fail(err)
		}
		// The anchor is visible before a caller may send its first remote mutation.
		observed, publishErr := r.publish(ctx, r.dir, "connection.json", anchor)
		r.anchored = observed.Visible
		if publishErr != nil {
			return fail(publishErr)
		}
		r.anchor, err = fileaccess.OpenPrivateFileAt(ctx, r.dir, "connection.json")
		if err != nil {
			return fail(err)
		}
	}
	if r.next > 999999 {
		return fail(errors.New("model checkpoint sequence exhausted"))
	}
	observed, err := r.publish(ctx, r.family, fmt.Sprintf("%06d.json", r.next), data)
	if err != nil {
		return fail(err)
	}
	if !observed.Visible {
		return fail(errors.New("model checkpoint visibility unknown"))
	}
	r.state = state
	r.next++
	return nil
}

func (r *modelRecord) validate(state modelCheckpoint) error {
	if err := validateModelCheckpoint(state); err != nil {
		return err
	}
	if r.anchored {
		a, b := r.initial, state
		a.Connection.State = ""
		b.Connection.State = ""
		if a.ClusterUID != b.ClusterUID || a.NamespaceUID != b.NamespaceUID || a.Connection != b.Connection {
			return errors.New("model connection identity changed")
		}
		if r.state.Complete {
			return errors.New("completed model family is immutable")
		}
	}
	return nil
}
func validateModelCheckpoint(s modelCheckpoint) error {
	c := s.Connection
	if s.Version != 1 || !verificationIdentity(s.ClusterUID) || !verificationIdentity(s.NamespaceUID) || !modelUUID(c.ID) || !modelUUID(c.CredentialGeneration) || c.Route != "copilot" || !verificationIdentity(c.Provider) || !verificationIdentity(c.Model) {
		return errors.New("invalid model connection")
	}
	if c.State != "unknown" && c.State != "model_ready" && c.State != "gateway_stopped" {
		return errors.New("invalid model observation")
	}
	if s.Complete && (s.Pending != nil || len(s.Resources) != 0) {
		return errors.New("model effects remain unresolved")
	}
	for _, resource := range s.Resources {
		if err := validateModelResource(resource, true); err != nil {
			return err
		}
	}
	if p := s.Pending; p != nil {
		if !modelUUID(p.OperationID) || (p.Verb != "create" && p.Verb != "delete") {
			return errors.New("invalid pending model action")
		}
		if p.Verb == "create" && p.Resource.Marker != p.OperationID {
			return errors.New("model create marker does not bind its operation")
		}
		if err := validateModelResource(p.Resource, p.Verb == "delete"); err != nil {
			return err
		}
	}
	return nil
}
func validateModelResource(r modelOwnedResource, ack bool) error {
	versions := map[string]string{"Deployment": "apps/v1", "Service": "v1", "Secret": "v1", "Provider": "core.orka.ai/v1alpha1", "Agent": "core.orka.ai/v1alpha1", "Task": "core.orka.ai/v1alpha1"}
	proof := r.Kind == "Agent" || r.Kind == "Task"
	if proof && r.Purpose != "proof" || !proof && r.Purpose != "connection" {
		return errors.New("model resource purpose does not match its kind")
	}
	if versions[r.Kind] == "" || versions[r.Kind] != r.APIVersion || r.Name == "" || !modelUUID(r.Marker) || ack && (r.UID == "" || r.ResourceVersion == "") {
		return errors.New("invalid model resource identity")
	}
	return nil
}

// Use caller-owned stage names so interrupted writes remain recognizable
// without coupling the record format to fileaccess's private staging names.
func publishModelState(ctx context.Context, dir *fileaccess.Directory, name string, data []byte) (fileaccess.PublicationResult, error) {
	id, err := uuid.NewRandom()
	if err != nil {
		return fileaccess.PublicationResult{VisibilityKnown: true}, err
	}
	return fileaccess.PublishPrivateNamed(ctx, dir, name, data, modelStagePrefix+id.String())
}

func encodeModelState(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errModelStateEncoding, err)
	}
	// Apply the reader's complete byte/structure bounds before either file
	// becomes visible; the anchor envelope is larger than its initial state.
	var decoded any
	if err := decodeModelState(bytes.NewReader(data), &decoded); err != nil {
		return nil, fmt.Errorf("%w: %w", errModelStateEncoding, err)
	}
	return data, nil
}

func decodeModelState(reader io.Reader, out any) error {
	data, err := io.ReadAll(io.LimitReader(reader, modelStateLimit+1))
	if err != nil {
		return err
	}
	if len(data) > modelStateLimit {
		return errors.New("model state exceeds bound")
	}
	if !unambiguousSessionsReceiptJSON(data) {
		return errors.New("ambiguous model state")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return errors.New("model state has trailing data")
	}
	return nil
}
func (r *modelRecord) finish(ctx context.Context) error {
	if r.failed || !r.anchored || !r.state.Complete || r.anchor == nil {
		return modelStateError(errors.New("model disconnect incomplete"))
	}
	if err := fileaccess.RemovePrivateFileAt(ctx, r.dir, "connection.json", r.anchor); err != nil {
		r.failed = true
		return modelStateError(err)
	}
	r.failed = true // No action, including a second local unlink, follows this effect.
	return modelStateError(fileaccess.SyncDirectory(ctx, r.dir))
}
func (r *modelRecord) close() error {
	var err error
	if r.anchor != nil {
		err = errors.Join(err, r.anchor.Close())
	}
	if r.family != nil {
		err = errors.Join(err, r.family.Close())
	}
	if r.lock != nil {
		err = errors.Join(err, r.lock.Close())
	}
	if r.dir != nil {
		err = errors.Join(err, r.dir.Close())
	}
	return modelStateError(err)
}
