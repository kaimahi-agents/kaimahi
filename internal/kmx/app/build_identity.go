package app

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/version"
)

const createdByVersionAnnotation = "kaimahi.dev/created-by-version"

func (a *App) setUserAgent(req *http.Request) {
	if a.buildIdentity.Version == "" {
		version.SetUserAgent(req)
		return
	}
	req.Header.Set("User-Agent", a.buildIdentity.UserAgent())
}

func (a *App) annotateCreatedByVersion(doc map[string]any) error {
	if a.buildIdentity.Version == "" || a.buildIdentity.Version == version.Unknown {
		return nil
	}
	meta, ok := doc["metadata"].(map[string]any)
	if !ok {
		return fmt.Errorf("cannot attach kmx build identity to malformed resource metadata")
	}
	annotations, present := meta["annotations"]
	if !present {
		meta["annotations"] = map[string]any{createdByVersionAnnotation: a.buildIdentity.Identifier()}
		return nil
	}
	values, ok := annotations.(map[string]any)
	if !ok {
		return fmt.Errorf("cannot attach kmx build identity to malformed resource annotations")
	}
	if existing, present := values[createdByVersionAnnotation]; present && existing != a.buildIdentity.Identifier() {
		return fmt.Errorf("%s is reserved for the creating kmx build", createdByVersionAnnotation)
	}
	values[createdByVersionAnnotation] = a.buildIdentity.Identifier()
	return nil
}

func createdByVersionValue(doc map[string]any) (string, bool) {
	meta, _ := doc["metadata"].(map[string]any)
	annotations, _ := meta["annotations"].(map[string]any)
	value, ok := annotations[createdByVersionAnnotation].(string)
	return value, ok && value != ""
}

func validateCreatedByVersion(raw []byte, desired map[string]any, namespace string, requireIdentity bool) error {
	if _, expected := createdByVersionValue(desired); !expected {
		return nil
	}
	var observed map[string]any
	if json.Unmarshal(raw, &observed) != nil || observed["apiVersion"] != desired["apiVersion"] || observed["kind"] != desired["kind"] {
		return fmt.Errorf("server returned an invalid build-annotated resource")
	}
	meta, _ := observed["metadata"].(map[string]any)
	wanted, _ := desired["metadata"].(map[string]any)
	if meta["name"] != wanted["name"] || meta["namespace"] != namespace || meta["deletionTimestamp"] != nil ||
		!orkaRenderedMetadataEqual(meta, wanted) {
		return fmt.Errorf("server changed the build-annotated resource")
	}
	if requireIdentity {
		uid, _ := meta["uid"].(string)
		generation, _ := meta["generation"].(float64)
		if uid == "" || generation < 1 {
			return fmt.Errorf("server returned no identity for the build-annotated resource")
		}
	}
	return nil
}

func bindCreatedByVersionProof(doc map[string]any, proof kagentObjectProof) kagentObjectProof {
	value, ok := createdByVersionValue(doc)
	if !ok {
		return proof
	}
	proof.Markers = maps.Clone(proof.Markers)
	if proof.Markers == nil {
		proof.Markers = map[string]string{}
	}
	proof.Markers[createdByVersionAnnotation] = value
	return proof
}
