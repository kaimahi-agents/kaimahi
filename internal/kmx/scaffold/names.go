package scaffold

import (
	"fmt"
	"regexp"
)

var (
	upstreamNameRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	objectNameRE   = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)
)

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
