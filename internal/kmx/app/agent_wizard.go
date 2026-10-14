package app

import (
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

func refuseWizardCredentials(values ...string) error {
	for _, value := range values {
		if err := scaffold.RefuseKeyShapes(value); err != nil {
			return fmt.Errorf("refusing credential-shaped wizard input; supply references, never credentials")
		}
	}
	return nil
}

func slugAgentName(description string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(description) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			dash = false
		} else {
			dash = true
		}
	}
	full := strings.Trim(b.String(), "-")
	const maxDefault = 32
	if len(full) <= maxDefault {
		return full
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(strings.ToLower(description))))
	suffix := fmt.Sprintf("%x", sum[:3])
	prefix := strings.TrimRight(full[:maxDefault-len(suffix)-1], "-")
	if boundary := strings.LastIndexByte(prefix, '-'); boundary >= 12 {
		prefix = prefix[:boundary]
	}
	if prefix == "" {
		prefix = "agent"
	}
	return prefix + "-" + suffix
}
