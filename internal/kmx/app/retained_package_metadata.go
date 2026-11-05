package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	suitecore "github.com/agent-primitives/suite/core"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/fileaccess"
)

func retainedEncode(value any) ([]byte, error) {
	data, err := suitecore.Marshal(value)
	if err != nil || len(data) > retainedMetadataBytes {
		return nil, errRetainedUnavailable
	}
	return data, nil
}
func retainedStage(name string) string {
	switch name {
	case "control.json":
		return "control.stage"
	case "ready.json":
		return "ready.stage"
	case "released.json":
		return "released.stage"
	}
	return ""
}
func retainedPeer(name string) string {
	switch name {
	case "control.stage":
		return "control.json"
	case "ready.stage":
		return "ready.json"
	case "released.stage":
		return "released.json"
	}
	return retainedStage(name)
}
func retainedMetadataLinks(ctx context.Context, dir *fileaccess.Directory, name string, f fileaccess.Object) error {
	n, err := fileaccess.LinkCount(ctx, f)
	if err != nil {
		return err
	}
	if n == 1 {
		return nil
	}
	peer := retainedPeer(name)
	if n != 2 || peer == "" {
		return errRetainedUnavailable
	}
	other, err := fileaccess.OpenPrivateFileAt(ctx, dir, peer)
	if err != nil {
		return errRetainedUnavailable
	}
	return errors.Join(retainedSame(ctx, f, other), other.Close())
}
func retainedRead(ctx context.Context, dir *fileaccess.Directory, name string, value any) error {
	f, err := fileaccess.OpenPrivateFileAt(ctx, dir, name)
	if err != nil {
		return err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil || before.Size() > retainedMetadataBytes {
		return errRetainedUnavailable
	}
	if err = retainedMetadataLinks(ctx, dir, name, f); err != nil {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(f, retainedMetadataBytes+1))
	if err != nil || len(data) > retainedMetadataBytes || !unambiguousSessionsReceiptJSON(data) {
		return errRetainedUnavailable
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(value); err != nil {
		return errRetainedUnavailable
	}
	if err = decoder.Decode(new(any)); err != io.EOF {
		return errRetainedUnavailable
	}
	canonical, err := retainedEncode(value)
	if err != nil || !bytes.Equal(data, canonical) {
		return errRetainedUnavailable
	}
	current, err := fileaccess.OpenPrivateFileAt(ctx, dir, name)
	if err != nil {
		return err
	}
	return errors.Join(retainedSame(ctx, f, current), retainedMetadataLinks(ctx, dir, name, current), current.Close(), f.Close())
}
func retainedPublish(ctx context.Context, dir *fileaccess.Directory, name string, value any) error {
	_, err := retainedWrite(ctx, dir, name, value, fileaccess.PublishPrivateNamed)
	return err
}
func (s *retainedStore) publishMetadata(ctx context.Context, dir *fileaccess.Directory, name string, value any) (fileaccess.PublicationResult, error) {
	return retainedWrite(ctx, dir, name, value, s.publish)
}
func retainedWrite(ctx context.Context, dir *fileaccess.Directory, name string, value any, write func(context.Context, *fileaccess.Directory, string, []byte, string) (fileaccess.PublicationResult, error)) (fileaccess.PublicationResult, error) {
	result := fileaccess.PublicationResult{VisibilityKnown: true}
	stage := retainedStage(name)
	if stage == "" {
		return result, errRetainedUnavailable
	}
	data, err := retainedEncode(value)
	if err != nil {
		return result, err
	}
	result, err = write(ctx, dir, name, data, stage)
	if err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		return result, errRetainedUnavailable
	}
	return result, nil
}
func (s *retainedStore) control(ctx context.Context, dir *fileaccess.Directory, h string) (c retainedControl, err error) {
	for _, name := range []string{"control.json", "released.json", "control.stage"} {
		c = retainedControl{}
		err = retainedRead(ctx, dir, name, &c)
		if !errors.Is(err, fileaccess.ErrMissing) {
			break
		}
	}
	if err != nil {
		return c, err
	}
	root, err := fileaccess.PhysicalIdentity(ctx, s.dir)
	if err != nil {
		return c, err
	}
	entry, err := fileaccess.PhysicalIdentity(ctx, dir)
	if err != nil || c.Domain != retainedDomain || c.Version != 1 || c.Handle != h || c.Root != root || c.Entry != entry {
		return c, errRetainedUnavailable
	}
	return c, nil
}

// Only fixed protocol stages beneath an already validated owned H are retired.
// A two-link stage must be exactly its final peer, never an external alias.
func retainedRetireStage(ctx context.Context, dir *fileaccess.Directory, name string) error {
	stage := retainedStage(name)
	if stage == "" {
		return errRetainedUnavailable
	}
	f, err := fileaccess.OpenPrivateFileAt(ctx, dir, stage)
	if errors.Is(err, fileaccess.ErrMissing) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = retainedMetadataLinks(ctx, dir, stage, f); err != nil {
		return errors.Join(err, f.Close())
	}
	removeErr := fileaccess.RemovePrivateFileAt(ctx, dir, stage, f)
	return errors.Join(removeErr, f.Close(), fileaccess.SyncDirectory(context.WithoutCancel(ctx), dir))
}
