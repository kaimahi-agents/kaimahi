package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"

	agentsuite "github.com/agent-primitives/suite"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	godigest "github.com/opencontainers/go-digest"
)

var errReleaseUpdateUnsupported = errors.New("no update-capable retained ownership proof; agents lifted by v0.8.1 or earlier cannot be updated yet; explicitly recreate the owned pair with this version first; nothing is removed automatically (docs/suite-workspace.md)")

type releaseOwnershipRecord struct {
	SchemaVersion     int                    `json:"schemaVersion"`
	ID                string                 `json:"id"`
	Summary           ReleaseLiftSummary     `json:"summary"`
	Annotation        map[string]any         `json:"annotation"`
	State             string                 `json:"state"`
	Resources         []ReleaseLiftOutcome   `json:"resources"`
	Receipt           []byte                 `json:"-"`
	ReleaseDataDigest string                 `json:"releaseDataDigest"`
	UpdateProof       *releaseOwnershipProof `json:"updateProof"`
}

func readReleaseOwnership(ctx context.Context, id string) (*releaseOwnershipRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !releaseLiftRecordID.MatchString(id) {
		return nil, errReleaseUpdateUnsupported
	}
	dir, err := config.StateDir()
	if err != nil {
		return nil, errReleaseUpdateUnsupported
	}
	root, _, info, err := openSessionReferenceDirectory(filepath.Join(dir, "release-lifts", id, "snapshot"))
	if err != nil {
		return nil, errReleaseUpdateUnsupported
	}
	defer root.Close()
	if info.Mode().Perm()&0077 != 0 {
		return nil, errReleaseUpdateUnsupported
	}
	for _, name := range []string{"4.json", "3.json", "2.json", "1.json"} {
		info, err := root.Lstat(name)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > maxReleaseLiftRecordBytes {
			return nil, errReleaseUpdateUnsupported
		}
		file, err := root.Open(name)
		if err != nil {
			return nil, errReleaseUpdateUnsupported
		}
		opened, openErr := file.Stat()
		if openErr != nil || !os.SameFile(info, opened) {
			file.Close()
			return nil, errReleaseUpdateUnsupported
		}
		raw, readErr := io.ReadAll(io.LimitReader(file, maxReleaseLiftRecordBytes+1))
		closeErr := file.Close()
		after, afterErr := root.Lstat(name)
		if readErr != nil || closeErr != nil || afterErr != nil || !os.SameFile(info, after) || len(raw) > maxReleaseLiftRecordBytes {
			return nil, errReleaseUpdateUnsupported
		}
		var wire struct {
			SchemaVersion     int                    `json:"schemaVersion"`
			ID                string                 `json:"id"`
			Summary           ReleaseLiftSummary     `json:"summary"`
			Annotation        map[string]any         `json:"annotation"`
			State             string                 `json:"state"`
			Resources         []ReleaseLiftOutcome   `json:"resources"`
			Receipt           json.RawMessage        `json:"receipt"`
			UpdateProof       *releaseOwnershipProof `json:"updateProof"`
			ReleaseDataDigest string                 `json:"releaseDataDigest"`
		}
		if agentsuite.Unmarshal(raw, &wire) != nil || wire.SchemaVersion != 1 || wire.ID != id || (wire.State != "ready" && wire.State != "partial" && wire.State != "not-applied") || wire.UpdateProof == nil || wire.UpdateProof.Version != 2 || !validReleaseTargetDependencies(wire.UpdateProof.Dependencies) ||
			!statusOpaque(wire.UpdateProof.RecordUID) || !validReleaseStateObjects(wire.UpdateProof.Objects, true) || len(wire.Resources) != 2 || godigest.FromBytes(wire.Receipt).String() != wire.Summary.EvidenceDigest {
			return nil, errReleaseUpdateUnsupported
		}
		if name != "4.json" || !agentsuite.IsValidDigest(wire.ReleaseDataDigest) {
			return nil, errReleaseState
		}
		for i, row := range wire.Resources {
			proof := wire.UpdateProof.Objects[i]
			if row.UID != proof.UID || row.Generation != proof.Generation || "sha256:"+row.SpecDigest != proof.SpecDigest || row.Outcome == "unknown" {
				return nil, errReleaseUpdateUnsupported
			}
		}
		return &releaseOwnershipRecord{wire.SchemaVersion, wire.ID, wire.Summary, wire.Annotation, wire.State, wire.Resources, wire.Receipt, wire.ReleaseDataDigest, wire.UpdateProof}, nil
	}
	return nil, errReleaseUpdateUnsupported
}

func releaseOwnershipMatches(record *releaseOwnershipRecord, state *releaseStateSnapshot) bool {
	return record != nil && record.UpdateProof.RecordUID == state.UID && record.ReleaseDataDigest == godigest.FromString(state.data).String() && reflect.DeepEqual(record.UpdateProof.Objects, state.State.Baseline) &&
		record.Summary.Suite == state.State.Suite && record.Summary.Member == state.State.Member && record.Summary.Runtime == state.State.Runtime && statusSameTarget(record.Summary.Target, state.State.Target)
}
