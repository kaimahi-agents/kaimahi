package fileaccess

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
)

// PublicationResult records what an exclusive publication positively observed.
// Visible can be true together with an error when complete bytes are present but
// post-link persistence was not acknowledged. When err is non-nil, Visible false
// means visibility was not observed; only VisibilityKnown distinguishes an
// observed pre-visibility failure from unresolved link reconciliation.
type PublicationResult struct {
	Visible         bool
	VisibilityKnown bool
}

var errPublicationVisibilityUnknown = errors.New("publication visibility is unknown")

var (
	publicationEntropy = rand.Read
	writePrivateBytes  = func(file *PrivateFile, data []byte) error {
		n, err := file.file.Write(data)
		if err == nil && n != len(data) {
			err = io.ErrShortWrite
		}
		return err
	}
	syncPrivateFile    = func(file *PrivateFile) error { return file.file.Sync() }
	afterPrivateLink   = func() error { return nil }
	linkPublishedEntry = linkPrivateEntry
	removeStagedEntry  = removePrivateEntry
	syncPublishedDir   = SyncDirectory
	closeObservedFile  = func(file *PrivateFile) error { return file.Close() }
)

// PublishPrivate writes complete private bytes and publishes name exactly once
// below directory. It flushes the staged file before the exclusive hard-link
// visibility point, then removes the owned stage and flushes the anchored
// directory. It never replaces, retries, or removes a visible destination.
func PublishPrivate(ctx context.Context, directory *Directory, name string, data []byte) (result PublicationResult, err error) {
	return publishPrivate(ctx, directory, name, data, "")
}

// PublishPrivateNamed is PublishPrivate with one caller-owned staging name.
// It is intended for protocols whose interrupted stage must be recognizable
// and recoverable. The stage name is never replaced or adopted.
func PublishPrivateNamed(ctx context.Context, directory *Directory, name string, data []byte, stage string) (result PublicationResult, err error) {
	result.VisibilityKnown = true
	if err := checkPrivateComponent(stage); err != nil {
		return result, wrapAccess("publish private file", err)
	}
	return publishPrivate(ctx, directory, name, data, stage)
}

func publishPrivate(ctx context.Context, directory *Directory, name string, data []byte, namedStage string) (result PublicationResult, err error) {
	result.VisibilityKnown = true
	if err := checkContext(ctx); err != nil {
		return result, err
	}
	if err := checkPrivateComponent(name); err != nil {
		return result, wrapAccess("publish private file", err)
	}
	if namedStage != "" && namedStage == name {
		return result, wrapAccess("publish private file", unsafeCause("publication stage and destination must differ"))
	}
	if err := CheckPrivate(ctx, directory, true); err != nil {
		return result, wrapAccess("publish private file", err)
	}

	stage, file, identity, err := createPublicationStage(ctx, directory, namedStage)
	if err != nil {
		return result, wrapAccess("publish private file", err)
	}
	stagePresent := true
	stageRemovalAttempted := false
	defer func() {
		var cleanupErr error
		if file != nil {
			if closeErr := file.Close(); closeErr != nil {
				cleanupErr = errors.Join(cleanupErr, closeErr)
			}
		}
		if stagePresent && !stageRemovalAttempted {
			if removeErr := removeStagedEntry(context.Background(), directory, stage, identity); removeErr != nil {
				cleanupErr = errors.Join(cleanupErr, removeErr)
			}
		}
		if cleanupErr != nil {
			err = wrapPublicationError(result.Visible, unreadableCause("private publication cleanup is uncertain"))
			return
		}
		if err != nil {
			err = wrapPublicationError(result.Visible, err)
		}
	}()

	if err := writePrivateBytes(file, data); err != nil {
		return result, wrapAccess("write private stage", err)
	}
	if err := syncPrivateFile(file); err != nil {
		return result, wrapAccess("sync private stage", err)
	}
	if err := checkContext(ctx); err != nil {
		return result, err
	}
	if err := file.Close(); err != nil {
		file = nil
		return result, wrapAccess("close private stage", err)
	}
	file = nil
	if err := CheckPrivate(ctx, directory, true); err != nil {
		return result, wrapAccess("publish private file", err)
	}
	visible, linkErr := linkPublishedEntry(ctx, directory, stage, name, identity)
	result.Visible = visible
	if errors.Is(linkErr, errPublicationVisibilityUnknown) {
		result.VisibilityKnown = false
	}
	if linkErr == nil {
		linkErr = afterPrivateLink()
	}
	if linkErr != nil {
		return result, wrapAccess("publish private file", linkErr)
	}
	if err := requirePrivateEntryIdentity(ctx, directory, name, identity); err != nil {
		return result, wrapAccess("verify published file", err)
	}
	if err := checkContext(ctx); err != nil {
		return result, err
	}
	stageRemovalAttempted = true
	if err := removeStagedEntry(ctx, directory, stage, identity); err != nil {
		return result, unreadableCause("private publication cleanup is uncertain")
	}
	stagePresent = false
	if err := syncPublishedDir(ctx, directory); err != nil {
		return result, err
	}
	if err := checkContext(ctx); err != nil {
		return result, err
	}
	return result, nil
}

// OpenPrivateFileAt opens an existing protected owner-only regular file
// immediately below directory. It creates, repairs, and truncates nothing.
func OpenPrivateFileAt(ctx context.Context, directory *Directory, name string) (*PrivateFile, error) {
	file, err := openExistingPrivateFileAt(ctx, directory, name)
	return file, wrapAccess("open private file", err)
}

// RemovePrivateFileAt removes name only when it still identifies expected.
// The caller must retain expected until this call returns. No missing entry is
// treated as success, and an ambiguous removal is never retried.
func RemovePrivateFileAt(ctx context.Context, directory *Directory, name string, expected Object) error {
	identity, err := PhysicalIdentity(ctx, expected)
	if err != nil {
		return wrapAccess("remove private file", err)
	}
	return wrapAccess("remove private file", removePrivateEntry(ctx, directory, name, identity))
}

func wrapPublicationError(visible bool, err error) error {
	if err == nil {
		return nil
	}
	operation := "publish private file"
	if visible {
		operation = "persist visible private file"
	}
	return wrapAccess(operation, err)
}

func createPublicationStage(ctx context.Context, directory *Directory, named string) (string, *PrivateFile, string, error) {
	if named != "" {
		file, err := CreatePrivateFileAt(ctx, directory, named)
		if err != nil {
			return "", nil, "", err
		}
		if file.identity == "" {
			closeErr := file.Close()
			return "", nil, "", creationFailure(unreadableCause("created private file identity is unavailable"), closeErr)
		}
		return named, file, file.identity, nil
	}
	for range 16 {
		raw := make([]byte, 16)
		if _, err := publicationEntropy(raw); err != nil {
			return "", nil, "", err
		}
		name := ".kmx-stage-" + hex.EncodeToString(raw)
		file, err := CreatePrivateFileAt(ctx, directory, name)
		if err != nil {
			if errors.Is(err, errEntryExists) {
				continue
			}
			return "", nil, "", err
		}
		if file.identity == "" {
			closeErr := file.Close()
			return "", nil, "", creationFailure(unreadableCause("created private file identity is unavailable"), closeErr)
		}
		return name, file, file.identity, nil
	}
	return "", nil, "", unsafeCause("private stage names collided")
}

func requirePrivateEntryIdentity(ctx context.Context, directory *Directory, name, identity string) error {
	matched, err := observePrivateEntryIdentity(ctx, directory, name, identity)
	if err != nil {
		return err
	}
	if !matched {
		return unsafeCause("private entry identity changed")
	}
	return nil
}

func observePrivateEntryIdentity(ctx context.Context, directory *Directory, name, identity string) (bool, error) {
	file, err := openExistingPrivateFileAt(ctx, directory, name)
	if err != nil {
		return false, wrapAccess("observe private entry", err)
	}
	got, identityErr := PhysicalIdentity(ctx, file)
	closeErr := closeObservedFile(file)
	if identityErr != nil {
		return false, creationFailure(identityErr, closeErr)
	}
	matched := got == identity
	if closeErr != nil {
		return matched, unreadableCause("private identity observation close is uncertain")
	}
	return matched, nil
}

func observedPrivateEntry(ctx context.Context, directory *Directory, name, identity string, effectErr error) (bool, error) {
	matched, err := observePrivateEntryIdentity(ctx, directory, name, identity)
	if matched {
		return true, errors.Join(effectErr, err)
	}
	if err == nil {
		return false, errors.Join(effectErr, unsafeCause("published entry identifies a different object"))
	}
	if errors.Is(err, ErrMissing) {
		return false, effectErr
	}
	return false, errors.Join(effectErr, errPublicationVisibilityUnknown, err)
}
