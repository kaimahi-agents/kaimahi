package agentsuite

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/distribution/reference"
	"github.com/opencontainers/go-digest"
)

// ValidateImageReference requires a registry-qualified, sha256-digested image
// reference with a valid registry authority.
func ValidateImageReference(value string) error {
	named, err := reference.ParseNormalizedNamed(value)
	if err != nil {
		return fmt.Errorf("must be a valid OCI image reference: %w", err)
	}
	slash := strings.IndexByte(value, '/')
	if slash <= 0 || value[:slash] != reference.Domain(named) {
		return fmt.Errorf("must include an explicit registry")
	}
	if _, tagged := named.(reference.Tagged); tagged {
		return fmt.Errorf("must not include a tag")
	}
	digested, ok := named.(reference.Digested)
	if !ok || digested.Digest().Algorithm() != digest.SHA256 {
		return fmt.Errorf("must use a sha256 digest")
	}
	if err := validateRegistryAuthority(reference.Domain(named)); err != nil {
		return err
	}
	return nil
}

func validateRegistryAuthority(authority string) error {
	parsed, err := url.Parse("registry://" + authority)
	if err != nil || parsed.Hostname() == "" || parsed.User != nil {
		return fmt.Errorf("registry authority is invalid")
	}
	if port := parsed.Port(); port != "" {
		value, err := strconv.ParseUint(port, 10, 16)
		if err != nil || value == 0 {
			return fmt.Errorf("registry port is invalid")
		}
	}
	host := parsed.Hostname()
	if net.ParseIP(host) != nil || host == "localhost" {
		return nil
	}
	if len(host) > 253 {
		return fmt.Errorf("registry hostname is invalid")
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || !asciiLetterOrDigit(label[0]) ||
			!asciiLetterOrDigit(label[len(label)-1]) {
			return fmt.Errorf("registry hostname is invalid")
		}
		for i := 1; i < len(label)-1; i++ {
			if !asciiLetterOrDigit(label[i]) && label[i] != '-' {
				return fmt.Errorf("registry hostname is invalid")
			}
		}
	}
	return nil
}

func asciiLetterOrDigit(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' ||
		value >= '0' && value <= '9'
}
