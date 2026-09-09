package blueprint_test

import (
	"errors"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/blueprint"
)

func TestBindingErrorDistinguishesInvalidFromMissing(t *testing.T) {
	b := &blueprint.Blueprint{Name: "demo", Parameters: map[string]blueprint.Parameter{
		"repo":    {Type: blueprint.TypeGitHubRepo, Required: true, Help: "the repository"},
		"ids":     {Type: blueprint.TypeIntList, Needs: []string{"repo"}},
		"tag":     {Type: blueprint.TypeString, Pattern: "^[a-z]+$"},
		"derived": {Type: blueprint.TypeString, Default: "${tag}", Pattern: "^[0-9]+$", RequiredFor: []string{"publish"}},
	}}
	for _, tc := range []struct {
		name    string
		set     map[string]string
		steps   []string
		invalid bool
	}{
		{"required", nil, nil, false},
		{"dependency", map[string]string{"ids": "1"}, nil, false},
		{"required for", map[string]string{"repo": "a/b"}, []string{"publish"}, false},
		{"unknown and missing", map[string]string{"typo": "value"}, nil, true},
		{"invalid repo", map[string]string{"repo": "wrong"}, nil, true},
		{"invalid list and missing", map[string]string{"ids": "one"}, nil, true},
		{"invalid pattern", map[string]string{"tag": "123"}, nil, true},
		{"invalid computed default", map[string]string{"repo": "a/b", "tag": "abc"}, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := b.Bind(tc.set, tc.steps)
			var binding *blueprint.BindingError
			if !errors.As(err, &binding) || binding.Invalid != tc.invalid {
				t.Fatalf("binding=%+v, error=%v, want invalid=%v", binding, err, tc.invalid)
			}
		})
	}
	_, err := b.Bind(nil, nil)
	want := "blueprint \"demo\": 1 parameter problem(s):\n  - --set repo=\u2026 is required: the repository"
	if err.Error() != want {
		t.Fatalf("diagnostic changed: %q, want %q", err, want)
	}
	if _, err := b.Bind(map[string]string{"repo": "a/b"}, nil); err != nil {
		t.Fatal(err)
	}
	_, _, err = b.BindRun(map[string]string{"ids": "bad"}, nil)
	var binding *blueprint.BindingError
	if !errors.As(err, &binding) || !binding.Invalid {
		t.Fatalf("BindRun lost typed invalid error: %v", err)
	}
}
