package agentsuite

import "testing"

func TestValidateSandboxBindingRejectsUnpinnedIdentity(t *testing.T) {
	data := []byte(`{
	  "schemaVersion":"1.0.0-draft",
	  "mediaType":"application/vnd.agentsuite.sandbox.binding.v1+json",
	  "suiteDigest":"latest",
	  "agent":"writer",
	  "platform":{"os":"linux","architecture":"amd64"},
	  "buildProfile":"default",
	  "composition":{"mediaType":"application/vnd.agentsuite.composition.v1+json","digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","size":1},
	  "inventory":{"mediaType":"application/json","digest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","size":1}
	}`)
	if _, err := ValidateSandboxBinding(data); err == nil {
		t.Fatal("expected invalid suite digest to be rejected")
	}
}

func TestValidateSandboxBindingRejectsWrongCompositionMediaType(t *testing.T) {
	data := []byte(`{
	  "schemaVersion":"1.0.0-draft",
	  "mediaType":"application/vnd.agentsuite.sandbox.binding.v1+json",
	  "suiteDigest":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
	  "agent":"writer",
	  "platform":{"os":"linux","architecture":"amd64"},
	  "buildProfile":"default",
	  "composition":{"mediaType":"application/json","digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","size":1},
	  "inventory":{"mediaType":"application/json","digest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","size":1}
	}`)
	if _, err := ValidateSandboxBinding(data); err == nil {
		t.Fatal("expected composition media type to be rejected")
	}
}
