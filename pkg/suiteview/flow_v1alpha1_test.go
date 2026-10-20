package suiteview_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	agentsuite "github.com/agent-primitives/suite"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/localartifact"
	suiteoras "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
	"github.com/kaimahi-agents/kaimahi/pkg/suiteview"
	godigest "github.com/opencontainers/go-digest"
)

func flowSource(t *testing.T, large bool) string {
	t.Helper()
	source := filepath.Join(t.TempDir(), "source")
	copyTree(t, fixture("coordinator-workers"), source)
	input := "frozen input"
	if large {
		input = strings.Repeat("x", (1<<20)+1)
	}
	runs, min, critical := 3, 2, true
	flow := agentsuite.FlowV1Alpha1{SchemaVersion: agentsuite.FlowVersionV1Alpha1, ID: "release", Mode: "pipeline", Entry: "writer", EdgeRequirements: []agentsuite.FlowEdgeRequirementV1Alpha1{}, Steps: []agentsuite.FlowStepV1Alpha1{{ID: "draft", Member: "writer", Input: agentsuite.FlowInputV1Alpha1{Source: "run.input"}}, {ID: "review", Member: "reviewer", Input: agentsuite.FlowInputV1Alpha1{Source: "draft.output", Context: "run.input"}, Approval: "policy-controlled"}}, Cases: []agentsuite.FlowCaseV1Alpha1{{ID: "facts", Input: input, Runs: &runs, MinPasses: &min, Assertions: []agentsuite.FlowAssertionV1Alpha1{{Step: "review", Check: agentsuite.EvaluationAssertion{ID: "verdict", Type: "contains", Value: "PASS", Critical: &critical}}}}}}
	raw, err := agentsuite.Marshal(flow)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(source, "coordination"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, agentsuite.FlowPathV1Alpha1), raw, 0600); err != nil {
		t.Fatal(err)
	}
	mutateJSONFile(t, filepath.Join(source, "agentsuite.json"), func(doc map[string]any) {
		doc["extensions"] = []any{map[string]any{"name": agentsuite.FlowExtensionV1Alpha1, "critical": true, "digest": stringDigest(raw)}}
	})
	return source
}

func stringDigest(raw []byte) string {
	d, err := agentsuite.Digest(raw)
	if err != nil {
		panic(err)
	}
	return d
}

func TestFlowViewOriginalGraphAllOwnedRecipes(t *testing.T) {
	for _, large := range []bool{false, true} {
		t.Run(map[bool]string{false: "small", true: "over-1MiB"}[large], func(t *testing.T) {
			source := flowSource(t, large)
			original := readFiles(t, source)
			layout := filepath.Join(t.TempDir(), "layout")
			pushed, err := suiteoras.PrepareFlowV1Alpha1Layout(t.Context(), source, layout, "flow:v1")
			if err != nil {
				t.Fatal(err)
			}
			digest := pushed.Descriptor.Digest.String()
			got, err := suiteview.LoadLayoutFlowV1Alpha1(t.Context(), layout, digest)
			if err != nil {
				t.Fatal(err)
			}
			archive := archiveLayout(t, layout)
			archived, err := suiteview.LoadArchiveFlowV1Alpha1(t.Context(), bytes.NewReader(archive), int64(len(archive)), digest)
			if err != nil || !reflect.DeepEqual(got, archived) {
				t.Fatal("archive changed view", err)
			}
			if got.Artifact.Digest != digest || got.Artifact.MediaType != pushed.Descriptor.MediaType || got.Artifact.Size != pushed.Descriptor.Size || got.Suite.GraphDigest != stringDigest(original["agentsuite.json"]) || got.FlowDigest != stringDigest(original[agentsuite.FlowPathV1Alpha1]) || len(got.Members) != 3 {
				t.Fatal("original identity/inventory lost", got)
			}
			for i, id := range []string{"coordinator", "writer", "reviewer"} {
				want, err := suiteview.ProjectMember(t.Context(), original, id)
				if err != nil {
					t.Fatal(err)
				}
				want.Artifact = got.Artifact
				if got.Members[i].Member.ID != id || !reflect.DeepEqual(got.Members[i], want) {
					t.Fatalf("complete ordered recipe %s lost", id)
				}
			}
			a, err := localartifact.ReadLayoutFlowV1Alpha1(t.Context(), layout, digest)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(a.Files(), original) || a.Descriptor().Digest != pushed.Descriptor.Digest {
				t.Fatal("original artifact bytes lost")
			}
			retainedGraph := a.FlowV1Alpha1()
			*retainedGraph = agentsuite.FlowV1Alpha1Graph{}
			if a.FlowV1Alpha1().FlowDigest() != got.FlowDigest {
				t.Fatal("graph pointer aliases retained authority")
			}
			f := a.FlowV1Alpha1().Flow()
			f.Steps[0].Member = "changed"
			*f.Cases[0].Runs = 99
			*f.Cases[0].MinPasses = 99
			*f.Cases[0].Assertions[0].Check.Critical = false
			if a.FlowV1Alpha1().Flow().Steps[0].Member != "writer" || *a.FlowV1Alpha1().Flow().Cases[0].Runs != 3 || !*a.FlowV1Alpha1().Flow().Cases[0].Assertions[0].Check.Critical {
				t.Fatal("graph accessor aliases definitions")
			}
			got.Members[0].Instructions[0] = 'X'
			got.Flow.Cases[0].Assertions[0].Check.Value = "changed"
			fresh, err := suiteview.LoadLayoutFlowV1Alpha1(t.Context(), layout, digest)
			if err != nil || !reflect.DeepEqual(fresh, archived) {
				t.Fatal("caller mutation leaked", err)
			}
			if _, err := localartifact.ReadLayout(t.Context(), layout, digest); err == nil {
				t.Fatal("default artifact reader accepted flow")
			}
			if _, err := suiteview.LoadLayout(t.Context(), layout, suiteview.Selection{MemberID: "writer"}); err == nil {
				t.Fatal("default public reader accepted flow")
			}
			if _, err := suiteview.LoadArchive(t.Context(), bytes.NewReader(archive), int64(len(archive)), suiteview.Selection{MemberID: "writer"}); err == nil {
				t.Fatal("default archive reader accepted flow")
			}
		})
	}
}

func TestFlowViewRefusals(t *testing.T) {
	layout := filepath.Join(t.TempDir(), "layout")
	source := flowSource(t, false)
	pushed, err := suiteoras.PrepareFlowV1Alpha1Layout(t.Context(), source, layout, "flow:v1")
	if err != nil {
		t.Fatal(err)
	}
	minimal := pushFixture(t, "minimal", layout, "minimal:v1")
	if _, err := suiteview.LoadLayoutFlowV1Alpha1(t.Context(), layout, minimal.Descriptor.Digest.String()); refusalCode(t, err) != suiteview.RefusalArtifact {
		t.Fatal("explicit flow reader fell back to ordinary graph", err)
	}
	archive := archiveLayout(t, layout)
	if _, err := suiteview.LoadLayoutFlowV1Alpha1(t.Context(), layout, ""); refusalCode(t, err) != suiteview.RefusalAmbiguousArtifact {
		t.Fatal(err)
	}
	if _, err := suiteview.LoadArchiveFlowV1Alpha1(t.Context(), bytes.NewReader(archive), int64(len(archive)), ""); refusalCode(t, err) != suiteview.RefusalAmbiguousArtifact {
		t.Fatal(err)
	}
	if _, err := suiteview.LoadLayoutFlowV1Alpha1(t.Context(), layout, pushed.Descriptor.Digest.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := suiteview.LoadArchiveFlowV1Alpha1(t.Context(), bytes.NewReader(archive), int64(len(archive)), pushed.Descriptor.Digest.String()); err != nil {
		t.Fatal("exact archive selection failed", err)
	}
	if _, err := suiteview.LoadLayoutFlowV1Alpha1(t.Context(), "private-canary", "not-digest"); refusalCode(t, err) != suiteview.RefusalSelection || strings.Contains(err.Error(), "canary") {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, archiveMode := range []bool{false, true} {
		var err error
		if archiveMode {
			_, err = suiteview.LoadArchiveFlowV1Alpha1(ctx, panicReaderAt{}, 0, "")
		} else {
			_, err = suiteview.LoadLayoutFlowV1Alpha1(ctx, "private-canary", "")
		}
		if refusalCode(t, err) != suiteview.RefusalCanceled || !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	}
	for _, record := range []archiveRecord{{header: tar.Header{Name: "../private-canary", Typeflag: tar.TypeReg}}, {header: tar.Header{Name: "index.json", Typeflag: tar.TypeSymlink, Linkname: "private-canary"}}} {
		raw := makeArchive(t, []archiveRecord{record})
		_, err := suiteview.LoadArchiveFlowV1Alpha1(t.Context(), bytes.NewReader(raw), int64(len(raw)), "")
		if refusalCode(t, err) != suiteview.RefusalArtifact || strings.Contains(err.Error(), "canary") {
			t.Fatal(err)
		}
	}
}

func TestFlowViewValidatesOriginalSelectedGraph(t *testing.T) {
	for _, kind := range []string{"unknown-critical", "wrong-flow-digest", "oversized-nonflow", "unsafe-content", "extra-coordination"} {
		t.Run(kind, func(t *testing.T) {
			source := flowSource(t, false)
			layout := filepath.Join(t.TempDir(), "layout")
			if _, err := suiteoras.PrepareFlowV1Alpha1Layout(t.Context(), source, layout, "flow:v1"); err != nil {
				t.Fatal(err)
			}
			rewriteFlowLayer(t, layout, func(files map[string][]byte) {
				switch kind {
				case "unknown-critical":
					var doc map[string]any
					if err := json.Unmarshal(files["agentsuite.json"], &doc); err != nil {
						t.Fatal(err)
					}
					doc["extensions"] = append(doc["extensions"].([]any), map[string]any{"name": "unknown.required", "critical": true})
					raw, err := agentsuite.Marshal(doc)
					if err != nil {
						t.Fatal(err)
					}
					files["agentsuite.json"] = raw
				case "wrong-flow-digest":
					files[agentsuite.FlowPathV1Alpha1] = bytes.Replace(files[agentsuite.FlowPathV1Alpha1], []byte("frozen input"), []byte("changed input"), 1)
				case "oversized-nonflow":
					files["supplement.bin"] = bytes.Repeat([]byte("x"), (1<<20)+1)
				case "unsafe-content":
					files["../private-canary"] = []byte("x")
				case "extra-coordination":
					files["coordination/extra.json"] = []byte("{}")
				}
			})
			raw := archiveLayout(t, layout)
			for _, archiveMode := range []bool{false, true} {
				var err error
				if archiveMode {
					_, err = suiteview.LoadArchiveFlowV1Alpha1(t.Context(), bytes.NewReader(raw), int64(len(raw)), "")
				} else {
					_, err = suiteview.LoadLayoutFlowV1Alpha1(t.Context(), layout, "")
				}
				want := suiteview.RefusalArtifact
				if kind == "oversized-nonflow" {
					want = suiteview.RefusalLimit
				}
				if refusalCode(t, err) != want || strings.Contains(err.Error(), "private-canary") {
					t.Fatal("selected original graph accepted", err)
				}
			}
		})
	}
}

// Re-address an altered original content layer, retaining valid OCI digests so
// refusal must come from graph/content validation rather than transport hashes.
func rewriteFlowLayer(t *testing.T, layout string, mutate func(map[string][]byte)) {
	t.Helper()
	rewriteSelectedManifest(t, layout, func(manifest map[string]any) {
		layer := manifest["layers"].([]any)[0].(map[string]any)
		raw, err := os.ReadFile(filepath.Join(layout, "blobs", "sha256", strings.TrimPrefix(layer["digest"].(string), "sha256:")))
		if err != nil {
			t.Fatal(err)
		}
		reader, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		tr := tar.NewReader(reader)
		files := map[string][]byte{}
		for {
			h, err := tr.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			if h.Typeflag == tar.TypeReg {
				files[h.Name], err = io.ReadAll(tr)
				if err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := reader.Close(); err != nil {
			t.Fatal(err)
		}
		mutate(files)
		var output bytes.Buffer
		gz := gzip.NewWriter(&output)
		tw := tar.NewWriter(gz)
		for name, body := range files {
			if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0600, Typeflag: tar.TypeReg, Size: int64(len(body))}); err != nil {
				t.Fatal(err)
			}
			if _, err := tw.Write(body); err != nil {
				t.Fatal(err)
			}
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
		digest := godigest.FromBytes(output.Bytes())
		if err := os.WriteFile(filepath.Join(layout, "blobs", "sha256", digest.Encoded()), output.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
		layer["digest"], layer["size"] = digest.String(), output.Len()
	})
}
