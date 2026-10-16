package app

import (
	"fmt"
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
