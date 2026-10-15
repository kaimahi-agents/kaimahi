package policy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sample() SuitePolicy {
	return SuitePolicy{
		Version:              SchemaVersion,
		DenyWritePaths:       []string{"/workspace/cache", "/workspace/source", "/workspace/cache"},
		RequiredCapabilities: []string{FilesystemDenyWrite},
	}
}

func TestCanonicalVectorFilesAgree(t *testing.T) {
	policyPath := filepath.Join("testdata", "effective-policy-v1.json")
	digestPath := filepath.Join("testdata", "effective-policy-v1.sha256")
	data, err := os.ReadFile(policyPath)
	if err != nil {
		t.Fatal(err)
	}
	wantDigest, err := os.ReadFile(digestPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := Digest(data), strings.TrimSpace(string(wantDigest)); got != want {
		t.Fatalf("fixture digest mismatch: got %s, want %s", got, want)
	}
	var effective EffectivePolicy
	if err := decodeStrict(data, &effective); err != nil {
		t.Fatal(err)
	}
	canonical, err := CanonicalBytes(effective)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(canonical, data) {
		t.Fatalf("fixture is not canonical: %s", data)
	}

	resolved, err := Resolve(sample(), AdministrativePolicy{DenyWritePaths: []string{"/etc/secrets"}})
	if err != nil {
		t.Fatal(err)
	}
	generated, err := CanonicalBytes(resolved)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(generated, data) {
		t.Fatalf("fixture differs from resolved policy: %s", generated)
	}
}

func TestSuitePolicyDecodeIsClosed(t *testing.T) {
	valid := `{"version":"agentsuite.policy/v1","denyWritePaths":["/a"],"requiredCapabilities":["filesystem/deny-write/v1"]}`
	bad := []string{
		`{"version":"agentsuite.policy/v1","denyWritePaths":["relative"],"requiredCapabilities":["filesystem/deny-write/v1"]}`,
		`{"version":"agentsuite.policy/v1","denyWritePaths":["/a"],"requiredCapabilities":["filesystem/deny-write/v1"],"extra":1}`,
		`{"version":"agentsuite.policy/v1","version":"agentsuite.policy/v1","denyWritePaths":["/a"],"requiredCapabilities":["filesystem/deny-write/v1"]}`,
		`{"version":"future","denyWritePaths":["/a"],"requiredCapabilities":["filesystem/deny-write/v1"]}`,
		`{"version":"agentsuite.policy/v1","denyWritePaths":["/a"]}`,
		`{"version":"agentsuite.policy/v1","denyWritePaths":["/a"],"requiredCapabilities":[]}`,
		`{"version":"agentsuite.policy/v1","denyWritePaths":["/a"],"requiredCapabilities":["unknown/v1"]}`,
		valid + ` {}`,
		valid + ` trailing`,
	}
	for _, input := range bad {
		if _, err := DecodeSuitePolicy([]byte(input)); err == nil {
			t.Errorf("accepted %q", input)
		}
	}
	if _, err := DecodeSuitePolicy([]byte(valid + " \n\t")); err != nil {
		t.Fatalf("rejected trailing whitespace: %v", err)
	}
}

func TestAdministrativePolicyDecodeIsClosed(t *testing.T) {
	valid := `{"denyWritePaths":["/admin"]}`
	bad := []string{
		`null`,
		`{"denyWritePaths":["/admin"],"denyWritePaths":["/other"]}`,
		`{"denyWritePaths":["/admin"],"extra":true}`,
		valid + ` {}`,
		valid + ` trailing`,
	}
	for _, input := range bad {
		if _, err := DecodeAdministrativePolicy([]byte(input)); err == nil {
			t.Errorf("accepted %q", input)
		}
	}
}

func TestPolicyDecodeRejectsUnpairedSurrogate(t *testing.T) {
	input := `{"version":"agentsuite.policy/v1","denyWritePaths":["/safe/\udcff"],"requiredCapabilities":["filesystem/deny-write/v1"]}`
	if _, err := DecodeSuitePolicy([]byte(input)); err == nil {
		t.Fatal("accepted unpaired surrogate escape")
	}
}

func TestPolicyPathLimitsApplyAtEveryBoundary(t *testing.T) {
	paths := make([]string, 4097)
	for i := range paths {
		paths[i] = fmt.Sprintf("/path/%04d", i)
	}
	admin := AdministrativePolicy{DenyWritePaths: paths}
	data, err := json.Marshal(admin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = DecodeAdministrativePolicy(data); err == nil {
		t.Fatal("decoded oversized administrative policy")
	}
	if _, err = Resolve(sample(), admin); err == nil {
		t.Fatal("resolved oversized administrative policy")
	}
	effective := EffectivePolicy{
		Version:              SchemaVersion,
		DenyWritePaths:       paths,
		RequiredCapabilities: []string{FilesystemDenyWrite},
	}
	if _, err = CanonicalBytes(effective); err == nil {
		t.Fatal("canonicalized oversized effective policy")
	}
}

func TestCanonicalBytesUseJCSStringEncoding(t *testing.T) {
	policy := EffectivePolicy{
		Version:              SchemaVersion,
		DenyWritePaths:       []string{"/a&<>é"},
		RequiredCapabilities: []string{FilesystemDenyWrite},
	}
	got, err := CanonicalBytes(policy)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"denyWritePaths":["/a&<>é"],"requiredCapabilities":["filesystem/deny-write/v1"],"version":"agentsuite.policy/v1"}`
	if string(got) != want {
		t.Fatalf("CanonicalBytes() = %s, want %s", got, want)
	}
}

func TestCompositionCannotWeaken(t *testing.T) {
	base, err := Resolve(sample(), AdministrativePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	strengthened, err := Resolve(sample(), AdministrativePolicy{DenyWritePaths: []string{"/admin"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(strengthened.DenyWritePaths) <= len(base.DenyWritePaths) {
		t.Fatal("administrative policy weakened result")
	}
}

func TestPayloadPreservesExactBytesAndCapabilities(t *testing.T) {
	effective, err := Resolve(sample(), AdministrativePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := Payload(effective)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyPayload(payload); err != nil {
		t.Fatal(err)
	}

	cases := []ProviderPayload{
		{Version: payload.Version, PolicyBytes: append(append([]byte(nil), payload.PolicyBytes...), ' '), PolicyDigest: payload.PolicyDigest, RequiredCapabilities: payload.RequiredCapabilities},
		{Version: payload.Version, PolicyBytes: payload.PolicyBytes, PolicyDigest: payload.PolicyDigest, RequiredCapabilities: nil},
		{Version: payload.Version, PolicyBytes: payload.PolicyBytes, PolicyDigest: payload.PolicyDigest, RequiredCapabilities: []string{}},
		{Version: payload.Version, PolicyBytes: payload.PolicyBytes, PolicyDigest: payload.PolicyDigest, RequiredCapabilities: []string{"unknown/v1"}},
		{Version: payload.Version, PolicyBytes: payload.PolicyBytes, PolicyDigest: payload.PolicyDigest, RequiredCapabilities: []string{FilesystemDenyWrite, "unknown/v1"}},
	}
	for _, changed := range cases {
		if err := VerifyPayload(changed); err == nil {
			t.Errorf("accepted altered payload: %#v", changed)
		}
	}
}
