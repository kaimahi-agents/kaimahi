package fileaccess

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
)

// ErrorClass is a safe, payload-free filesystem outcome category.
type ErrorClass string

const (
	ClassMissing     ErrorClass = "missing"
	ClassUnsafe      ErrorClass = "unsafe"
	ClassUnsupported ErrorClass = "unsupported"
	ClassUnreadable  ErrorClass = "unreadable"
)

var (
	ErrMissing     = errors.New("filesystem object missing")
	ErrUnsafe      = errors.New("unsafe filesystem state")
	ErrUnsupported = errors.New("unsupported filesystem")
	ErrUnreadable  = errors.New("filesystem state unreadable")
	errEntryExists = errors.New("filesystem entry already exists")
)

// AccessError exposes a fixed operation and exclusive category, never a caller
// path, SID, object name, or operating-system diagnostic.
type AccessError struct {
	Operation string
	Class     ErrorClass
	cause     error
}

func (e *AccessError) Error() string {
	return fmt.Sprintf("%s: %s", e.Operation, classError(e.Class))
}

func (e *AccessError) Is(target error) bool {
	if target == classError(e.Class) {
		return true
	}
	if e.Class == ClassMissing && target == fs.ErrNotExist {
		return true
	}
	if target == errEntryExists && errors.Is(e.cause, target) {
		return true
	}
	return e.Class == ClassUnreadable && target == fs.ErrPermission && errors.Is(e.cause, fs.ErrPermission)
}

func classError(class ErrorClass) error {
	switch class {
	case ClassMissing:
		return ErrMissing
	case ClassUnsafe:
		return ErrUnsafe
	case ClassUnsupported:
		return ErrUnsupported
	default:
		return ErrUnreadable
	}
}

func wrapAccess(operation string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	var access *AccessError
	if errors.As(err, &access) {
		return &AccessError{Operation: operation, Class: access.Class, cause: access.cause}
	}
	var classified *classifiedCause
	if errors.As(err, &classified) {
		return &AccessError{Operation: operation, Class: classified.class, cause: classified.cause}
	}
	class := ClassUnreadable
	if errors.Is(err, fs.ErrNotExist) {
		class = ClassMissing
	}
	return &AccessError{Operation: operation, Class: class, cause: err}
}

type classifiedCause struct {
	class  ErrorClass
	cause  error
	reason string
}

func (e *classifiedCause) Error() string {
	if e.reason != "" {
		return e.reason
	}
	return classError(e.class).Error()
}

func (e *classifiedCause) Is(target error) bool { return target == classError(e.class) }

func unsafeCause(reason string) error { return &classifiedCause{class: ClassUnsafe, reason: reason} }
func unreadableCause(reason string) error {
	return &classifiedCause{class: ClassUnreadable, reason: reason}
}

func creationFailure(primary error, cleanup ...error) error {
	if errors.Join(cleanup...) != nil {
		return unreadableCause("private creation cleanup is uncertain")
	}
	return primary
}

func checkContext(ctx context.Context) error {
	if ctx == nil {
		return unsafeCause("nil context")
	}
	return ctx.Err()
}
