package app

// Pair metadata is path-free and optional for compatibility with schema-2
// agent-only diagnostics. Revision on the enclosing record remains the raw agent.
type fileFailurePairMetadata struct {
	SnapshotRevision string `json:"snapshotRevision"`
	State            string `json:"state"`
	SelectionKey     string `json:"selectionKey"`
	Revision         string `json:"revision"`
}

func retainedPairMetadata(snapshot string, tests *FilePreviewTestsSource) *fileFailurePairMetadata {
	if snapshot == "" || tests == nil {
		return nil
	}
	return &fileFailurePairMetadata{snapshot, tests.State, tests.SelectionKey, tests.Revision}
}
func validRetainedPairMetadata(p *fileFailurePairMetadata, agentRevision string) bool {
	if p == nil || !suitePrefixedDigest(p.SnapshotRevision) {
		return false
	}
	switch p.State {
	case "external":
		return suitePrefixedDigest(p.SelectionKey) && suitePrefixedDigest(p.Revision)
	case "inline":
		return p.Revision == agentRevision && (p.SelectionKey == "" || suitePrefixedDigest(p.SelectionKey))
	case "absent":
		return suitePrefixedDigest(p.SelectionKey) && p.Revision == ""
	case "none":
		return p.SelectionKey == "" && p.Revision == ""
	}
	return false
}
