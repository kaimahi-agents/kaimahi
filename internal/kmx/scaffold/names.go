package scaffold

import (
	"fmt"
	"regexp"
)

var (
	upstreamNameRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	objectNameRE   = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)
	suiteNameRE    = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)
)

// ValidSuiteName reports whether name is a valid workspace or suite name. It is
// the single grammar for workspace authoring/loading and AgentSessions rendering.
func ValidSuiteName(name string) bool { return suiteNameRE.MatchString(name) }

// ValidateNamespace checks the Kubernetes RFC 1123 label shape.
func ValidateNamespace(ns string) error {
	if !upstreamNameRE.MatchString(ns) || len(ns) > 63 {
		return fmt.Errorf("%q is not a Kubernetes namespace name (RFC 1123 label)", ns)
	}
	return nil
}

// ValidateObjectName checks referenced Secret, Deployment and ConfigMap names.
func ValidateObjectName(name string) error {
	if !objectNameRE.MatchString(name) || len(name) > 253 {
		return fmt.Errorf("%q is not a Kubernetes object name (RFC 1123 subdomain: "+
			"lowercase letters, digits, dashes and dots, starting and ending alphanumeric)", name)
	}
	return nil
}
