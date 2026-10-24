package modelaccess

import (
	"context"
	"net/http"
)

// PreviewAccess owns one bounded invocation's private credentials. CheckContent
// checks plain decoded strings, independently of lifecycle validation. Callers
// must check all retained content before journaling and Validate before creating
// each Session. Client accepts only routes selected at setup.
//
// A provider may return a nonnil capability alongside a setup error so callers
// can withhold credential-bearing identities even when route selection fails.
// Such a capability permits content checks, never inference.
type PreviewAccess interface {
	CheckContent(...string) error
	Validate(context.Context) error
	Client(Route) (http.RoundTripper, error)
}
