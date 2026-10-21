package agentsuite

import (
	"fmt"
	"maps"
	"os"
	"path"
	"slices"
	"strings"
	"unicode/utf8"
)

// DirectoryModes returns the authored tar modes for every declared ToolProvider
// payload path in a loose suite, keyed by the slash-separated root-relative
// path. Paths absent from the map use the portable loose defaults (file 0644,
// directory 0755, symlink 0777); host permission bits never select a mode.
//
// Declarations come from the same bounded, descriptor-checked loader used by
// sandbox planning, so changed or non-canonical child manifests refuse. All
// variants of all catalog providers contribute, matching the public loose
// reader; a path declared with two different modes refuses. This is not a
// conformance check: callers still validate the source and resulting archive
// with the public suite validator.
func DirectoryModes(root *os.Root) (map[string]uint32, error) {
	suiteBytes, err := readBuildFile(root, "agentsuite.json", maxBuildJSONBytes)
	if err != nil {
		return nil, err
	}
	metadata, err := loadBuildMetadata(root, suiteBytes)
	if err != nil {
		return nil, err
	}
	modes := map[string]uint32{}
	// Sorted traversal keeps a conflict's reported path deterministic.
	for _, key := range slices.Sorted(maps.Keys(metadata.toolProviders)) {
		for _, variant := range metadata.toolProviders[key].Variants {
			if !normalizedPayloadPath(variant.PayloadRoot) {
				return nil, fmt.Errorf("tool provider %s payloadRoot %q is not normalized", key, variant.PayloadRoot)
			}
			for _, entry := range variant.Files {
				if !normalizedPayloadPath(entry.Path) {
					return nil, fmt.Errorf("tool provider %s inventory path %q is not normalized", key, entry.Path)
				}
				// Higher bits would encode setid/sticky or file type in a tar header.
				if entry.Mode > 0o777 {
					return nil, fmt.Errorf("tool provider %s inventory path %s has invalid mode %#o", key, entry.Path, entry.Mode)
				}
				name := path.Join(variant.PayloadRoot, entry.Path)
				if previous, ok := modes[name]; ok && previous != entry.Mode {
					return nil, fmt.Errorf("content path %s has conflicting declared mode", name)
				}
				modes[name] = entry.Mode
			}
		}
	}
	return modes, nil
}

// normalizedPayloadPath mirrors readBuildFile's root-relative path predicate.
func normalizedPayloadPath(name string) bool {
	return name != "" && len(name) <= 1024 && utf8.ValidString(name) && !strings.ContainsAny(name, "\\\x00") &&
		!path.IsAbs(name) && path.Clean(name) == name && name != "." && name != ".." && !strings.HasPrefix(name, "../")
}
