// Native Orka document rendering preserves exact order and review-only secrets.
package app

import (
	"fmt"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
	"go.yaml.in/yaml/v3"
)

func orkaRenderedDocuments(bundle *scaffold.OrkaBundle) ([]agentruntime.Document, error) {
	documents := make([]agentruntime.Document, 0, 4)
	for _, doc := range bundle.Documents() {
		encoded, err := yaml.Marshal(doc)
		if err != nil {
			return nil, fmt.Errorf("render Orka bundle: %w", err)
		}
		if kind, _ := doc["kind"].(string); kind == "Secret" {
			documents = append(documents, agentruntime.ReviewDocument(encoded))
			continue
		}
		documents = append(documents, agentruntime.ApplyDocument(encoded))
	}
	return documents, nil
}
