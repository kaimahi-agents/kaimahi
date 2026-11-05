package localartifact

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"testing"

	suitecore "github.com/agent-primitives/suite/core"
	godigest "github.com/opencontainers/go-digest"
)

func sourcePolicyFixture(t *testing.T, count int) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	for n := 0; n < count; n++ {
		files[fmt.Sprintf("file-%03d", n)] = []byte("x")
	}
	agent, tests := []byte("# agent\r\n"), []byte("# tests\r\n")
	files["metadata/kmx/source/greeter.agent.md"] = agent
	files["metadata/kmx/source/greeter.agent.test.md"] = tests
	index, err := suitecore.Marshal(map[string]any{"schemaVersion": 1, "packageProfile": "orka-source/v1", "sourceFormat": "kmx.orka-agent-markdown", "sourceSchemaVersion": 1, "packagerVersion": "kmx.orka-source-packager/v1", "memberId": "greeter", "sources": []any{
		map[string]any{"role": "agent", "path": "metadata/kmx/source/greeter.agent.md", "digest": godigest.FromBytes(agent).String(), "size": len(agent)},
		map[string]any{"role": "tests", "path": "metadata/kmx/source/greeter.agent.test.md", "digest": godigest.FromBytes(tests).String(), "size": len(tests)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	files["metadata/kmx/source/index.json"] = index
	return files
}

func sourcePolicyArchive(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	// The index is deliberately last, and no directory headers are required.
	for name, data := range files {
		if name == "metadata/kmx/source/index.json" {
			continue
		}
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if data, ok := files["metadata/kmx/source/index.json"]; ok {
		tw.WriteHeader(&tar.Header{Name: "metadata/kmx/source/index.json", Mode: 0600, Size: int64(len(data)), Typeflag: tar.TypeReg})
		tw.Write(data)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestSourceProfileTransportReaders(t *testing.T) {
	for _, count := range []int{212, 213} {
		files := sourcePolicyFixture(t, count)
		_, err := Files(context.Background(), sourcePolicyArchive(t, files))
		if (err == nil) != (count == 212) {
			t.Fatalf("base=%d total=%d: %v", count, len(files), err)
		}
	}
	for _, count := range []int{212, 213} {
		files := map[string][]byte{}
		for n := 0; n < count; n++ {
			files[fmt.Sprintf("file-%03d", n)] = []byte("x")
		}
		_, err := Files(context.Background(), sourcePolicyArchive(t, files))
		if (err == nil) != (count == 212) {
			t.Fatalf("legacy %d: %v", count, err)
		}
	}
}

func TestSourceProfileTransportByteCeilings(t *testing.T) {
	for _, extra := range []int{0, 1} {
		files := sourcePolicyFixture(t, 0)
		for n := 0; n < 8; n++ {
			files[fmt.Sprintf("base-%d", n)] = bytes.Repeat([]byte("x"), 1<<20)
		}
		if extra != 0 {
			files["extra"] = []byte("x")
		}
		_, err := Files(context.Background(), sourcePolicyArchive(t, files))
		if (err == nil) != (extra == 0) {
			t.Fatalf("8MiB base plus %d: %v", extra, err)
		}
	}
	for _, size := range []int{11 << 20, (11 << 20) + 512} {
		files := sourcePolicyFixture(t, 0)
		packed := sourcePolicyArchive(t, files)
		reader, err := gzip.NewReader(bytes.NewReader(packed))
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		reader.Close()
		raw = append(raw, make([]byte, size-len(raw))...)
		var compressed bytes.Buffer
		writer := gzip.NewWriter(&compressed)
		writer.Write(raw)
		writer.Close()
		_, err = Files(context.Background(), compressed.Bytes())
		if (err == nil) != (size == 11<<20) {
			t.Fatalf("tar bytes %d: %v", size, err)
		}
	}
	if _, err := Files(context.Background(), make([]byte, (10<<20)+1)); !errors.Is(err, ErrArtifactLimit) {
		t.Fatal("compressed bound", err)
	}
}

func TestSourceProfileSupplementRefusesMutation(t *testing.T) {
	for _, change := range []func(map[string][]byte){
		func(f map[string][]byte) { f["metadata/kmx/source/greeter.agent.md"][0] = 'X' },
		func(f map[string][]byte) { delete(f, "metadata/kmx/source/greeter.agent.test.md") },
		func(f map[string][]byte) { f["metadata/kmx/source/extra.md"] = []byte("extra") },
		func(f map[string][]byte) {
			f["metadata/kmx/source/index.json"] = append(f["metadata/kmx/source/index.json"], []byte(" ")...)
		},
	} {
		files := sourcePolicyFixture(t, 1)
		change(files)
		if _, err := Files(context.Background(), sourcePolicyArchive(t, files)); err == nil {
			t.Fatal("malformed supplement accepted")
		}
	}
}
