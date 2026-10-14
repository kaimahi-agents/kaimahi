package app

import (
	"encoding/json"
	"slices"
	"testing"
)

var suiteReservedKeys = []string{"kaimahi.dev/suite", "kaimahi.dev/suite-digest", "kaimahi.dev/suite-member", "kaimahi.dev/suite-renderer", "kaimahi.dev/suite-binding-digest", "kaimahi.dev/suite-rendered-digest"}

func reserveSuiteDoc(doc map[string]any, key string, value any) {
	meta := doc["metadata"].(map[string]any)
	ann, _ := meta["annotations"].(map[string]any)
	if ann == nil {
		ann = map[string]any{}
		meta["annotations"] = ann
	}
	ann[key] = value
}

func persistentOrkaCalls(t *testing.T, dir string) []orkaCall {
	t.Helper()
	var writes []orkaCall
	for _, c := range orkaCalls(t, dir) {
		if slices.Contains(c.Args, "--dry-run=server") {
			continue
		}
		for _, verb := range []string{"create", "replace", "patch", "delete"} {
			if slices.Contains(c.Args, verb) {
				writes = append(writes, c)
				break
			}
		}
	}
	return writes
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
