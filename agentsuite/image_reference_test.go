package agentsuite

import (
	"strings"
	"testing"
)

func TestValidateImageReference(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	tests := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{name: "registry", value: "registry.example/team/image@" + digest},
		{name: "registry port", value: "localhost:5000/team/image@" + digest},
		{name: "IPv6 registry", value: "[2001:db8::1]:5000/team/image@" + digest},
		{name: "implicit registry", value: "team/image@" + digest, wantErr: true},
		{name: "empty hostname label", value: "registry..example/team/image@" + digest, wantErr: true},
		{name: "invalid port", value: "registry.example:70000/team/image@" + digest, wantErr: true},
		{name: "tag and digest", value: "registry.example/team/image:latest@" + digest, wantErr: true},
		{name: "tag only", value: "registry.example/team/image:latest", wantErr: true},
		{name: "invalid digest", value: "registry.example/team/image@sha256:" + strings.Repeat("g", 64), wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateImageReference(test.value)
			if (err != nil) != test.wantErr {
				t.Fatalf("ValidateImageReference(%q) error = %v, wantErr %v", test.value, err, test.wantErr)
			}
		})
	}
}
