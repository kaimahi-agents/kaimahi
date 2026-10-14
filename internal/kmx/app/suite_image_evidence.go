package app

import (
	"context"
	"encoding/hex"
	"errors"
	"reflect"

	spec "github.com/kaimahi-agents/kaimahi/agentsuite"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
)

// Image evidence is separate from native Task/A2A receipts. It contains only
// identities and assertion verdicts, never prompts, answers or credential values.
type suiteImageSubject struct {
	ReleaseDigest       string            `json:"releaseDigest"`
	SuiteDigest         string            `json:"suiteDigest"`
	Member              string            `json:"member"`
	ImageDigest         string            `json:"imageDigest"`
	Profile             string            `json:"profile"`
	ConfigurationDigest string            `json:"configurationDigest"`
	Model               string            `json:"model"`
	Target              ReleaseLiftTarget `json:"target"`
	BindingDigest       string            `json:"bindingDigest"`
	ExecutionDigest     string            `json:"executionDigest"`
}

type suiteImagePassingReceipt struct {
	SchemaVersion int                     `json:"schemaVersion"`
	Domain        string                  `json:"domain"`
	RunID         string                  `json:"runID"`
	Subject       suiteImageSubject       `json:"subject"`
	CasesDigest   string                  `json:"casesDigest"`
	Result        string                  `json:"result"`
	Cases         []suiteImagePassingCase `json:"cases"`
	Resources     []ReleaseLiftResource   `json:"resources"`
}

type suiteImagePassingCase struct {
	ID            string                  `json:"id"`
	CaseDigest    string                  `json:"caseDigest"`
	InputDigest   string                  `json:"inputDigest"`
	RequestID     string                  `json:"requestID"`
	DeploymentUID string                  `json:"deploymentUID"`
	AnswerDigest  string                  `json:"answerDigest"`
	Assertions    []suitePassingAssertion `json:"assertions"`
}

func imageInventory(suite *workspace.ImageSuite) ([]suitePassingInventoryCase, string, error) {
	if suite == nil {
		return nil, "", errSuitePassingReceipt
	}
	// Reuse only the runtime-neutral case parser and assertion inventory, never
	// native subject/receipt validation or a native execution identity.
	return suitePassingInventory(&workspace.NativeSuite{Cases: suite.Cases})
}

func canonicalImagePassing(ctx context.Context, r *suiteImagePassingReceipt, suite *workspace.ImageSuite) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r == nil || suite == nil || r.SchemaVersion != 1 || r.Domain != "kmx.image-http.passing/v1" || r.Result != "pass" || !imageRunID(r.RunID) {
		return nil, errSuitePassingReceipt
	}
	s := r.Subject
	if s.SuiteDigest != suite.Digest || s.Member != suite.MemberID || s.Profile != "kmx.image-http/v1" || s.Model == "" || !verificationIdentity(s.Target.Context) || !verificationIdentity(s.Target.Namespace) || !verificationIdentity(s.Target.ClusterUID) || !verificationIdentity(s.Target.NamespaceUID) {
		return nil, errSuitePassingReceipt
	}
	for _, digest := range []string{s.ReleaseDigest, s.SuiteDigest, s.ImageDigest, s.ConfigurationDigest, s.BindingDigest, s.ExecutionDigest} {
		if !spec.IsValidDigest(digest) {
			return nil, errSuitePassingReceipt
		}
	}
	inventory, digest, err := imageInventory(suite)
	if err != nil || r.CasesDigest != digest || len(r.Cases) != len(inventory) {
		return nil, errSuitePassingReceipt
	}
	requests := map[string]bool{}
	if len(r.Resources) != 3 {
		return nil, errSuitePassingReceipt
	}
	for i, kind := range []string{"ConfigMap", "Deployment", "Service"} {
		resource := r.Resources[i]
		if resource.Kind != kind || !verificationIdentity(resource.Name) || !verificationIdentity(resource.UID) || !spec.IsValidDigest(resource.SpecDigest) {
			return nil, errSuitePassingReceipt
		}
	}
	for i, c := range r.Cases {
		want := inventory[i].Metadata
		if c.ID != want.ID || c.CaseDigest != want.CaseDigest || c.InputDigest != want.InputDigest || !imageRunID(c.RequestID) || requests[c.RequestID] || c.DeploymentUID != r.Resources[1].UID || !spec.IsValidDigest("sha256:"+c.AnswerDigest) || !reflect.DeepEqual(c.Assertions, want.Assertions) {
			return nil, errSuitePassingReceipt
		}
		requests[c.RequestID] = true
	}
	raw, err := spec.Marshal(r)
	if err != nil || len(raw) > maxSuitePassingReceiptBytes {
		return nil, errSuitePassingReceipt
	}
	return raw, nil
}

func imageRunID(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == 16 && hex.EncodeToString(b) == s
}

func decodeImagePassing(ctx context.Context, raw []byte, suite *workspace.ImageSuite) (*suiteImagePassingReceipt, error) {
	if len(raw) > maxSuitePassingReceiptBytes {
		return nil, errSuitePassingReceipt
	}
	var r suiteImagePassingReceipt
	if err := spec.Unmarshal(raw, &r); err != nil {
		return nil, errors.New("invalid image passing evidence")
	}
	if _, err := canonicalImagePassing(ctx, &r, suite); err != nil {
		return nil, err
	}
	return &r, nil
}
