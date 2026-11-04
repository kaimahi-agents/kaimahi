package localartifact

import (
	"bytes"
	"path"
	"strings"

	suitecore "github.com/agent-primitives/suite/core"
	godigest "github.com/opencontainers/go-digest"
)

// SourceProfile identifies the closed, single-member native source supplement.
const SourceProfile = "orka-source/v1"
const SourceIndexPath = "metadata/kmx/source/index.json"
const SourceFormat = "kmx.orka-agent-markdown"
const SourcePackager = "kmx.orka-source-packager/v1"
const MaxTransportFiles = MaxFiles + 3
const sourceSupplementBytes = (528 << 10)
const transportCompressedBytes = 10 << 20
const transportTarBytes = 11 << 20
const transportEntries = 272

// SourceIndex is source metadata, not execution or source-freshness authority.
type SourceIndex struct {
	SchemaVersion       int            `json:"schemaVersion"`
	PackageProfile      string         `json:"packageProfile"`
	SourceFormat        string         `json:"sourceFormat"`
	SourceSchemaVersion int            `json:"sourceSchemaVersion"`
	PackagerVersion     string         `json:"packagerVersion"`
	MemberID            string         `json:"memberId"`
	Sources             []SourceRecord `json:"sources"`
}

// SourceRecord binds one canonical logical role to exact original bytes.
type SourceRecord struct {
	Role   string `json:"role"`
	Path   string `json:"path"`
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

// SourceProfileBase checks the complete supplement and base bounds. Returned
// values borrow caller-owned file bytes; native graph validation is separate.
func SourceProfileBase(files map[string][]byte) (SourceIndex, map[string][]byte, error) {
	var index SourceIndex
	data := files[SourceIndexPath]
	if len(data) == 0 || len(data) > 16<<10 || suitecore.Unmarshal(data, &index) != nil {
		return index, nil, ErrArtifact
	}
	canonical, err := suitecore.Marshal(index)
	if err != nil || !bytes.Equal(canonical, data) || index.SchemaVersion != 1 || index.PackageProfile != SourceProfile || index.SourceFormat != SourceFormat || index.SourceSchemaVersion != 1 || index.PackagerVersion != SourcePackager || index.MemberID == "" || index.MemberID == "." || index.MemberID == ".." || path.Base(index.MemberID) != index.MemberID || strings.Contains(index.MemberID, "\\") || len(index.Sources) != 2 {
		return index, nil, ErrArtifact
	}
	allowed := map[string]bool{SourceIndexPath: true}
	for n, role := range []string{"agent", "tests"} {
		name := "metadata/kmx/source/" + index.MemberID + ".agent.md"
		if n == 1 {
			name = "metadata/kmx/source/" + index.MemberID + ".agent.test.md"
		}
		record := index.Sources[n]
		original, ok := files[name]
		if !ok || len(original) == 0 || len(original) > 256<<10 || record.Role != role || record.Path != name || record.Size != int64(len(original)) || record.Digest != godigest.FromBytes(original).String() {
			return index, nil, ErrArtifact
		}
		allowed[name] = true
	}
	base := make(map[string][]byte, len(files))
	total := 0
	for name, data := range files {
		if allowed[name] {
			continue
		}
		if strings.HasPrefix(name, "metadata/kmx/source/") {
			return index, nil, ErrArtifact
		}
		if len(data) > fileBytes {
			return index, nil, ErrArtifactLimit
		}
		base[name] = data
		total += len(data)
	}
	if len(base) > MaxFiles || total > contentBytes {
		return index, nil, ErrArtifactLimit
	}
	return index, base, nil
}

func checkInventoryPolicy(files map[string][]byte, compressed, raw, entries int, flow bool) error {
	if _, source := files[SourceIndexPath]; source {
		if flow {
			return ErrArtifact
		}
		_, _, err := SourceProfileBase(files)
		return err
	}
	total := 0
	for _, data := range files {
		total += len(data)
	}
	if len(files) > MaxFiles || total > contentBytes || compressed > compressedBytes || raw > tarBytes || entries > entriesLimit {
		return ErrArtifactLimit
	}
	return nil
}
