package app

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime/debug"
	"strings"
	"testing"
	"time"
)

func publicProducerFixture() suitePublicProducer {
	modified := false
	return suitePublicProducer{Module: "github.com/kaimahi-agents/kaimahi", Version: "(devel)", Revision: strings.Repeat("a", 40), Modified: &modified}
}
func publicMetadataFixture() *suitePublicMetadata {
	return &suitePublicMetadata{SchemaVersion: 1, StartedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano), CompletedAt: time.Date(2026, 1, 1, 0, 0, 1, 0, time.UTC).Format(time.RFC3339Nano), Producer: publicProducerFixture()}
}

func TestSuitePublicBuildInfo(t *testing.T) {
	good := debug.BuildInfo{Main: debug.Module{Path: "github.com/kaimahi-agents/kaimahi", Version: "(devel)"}, Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: strings.Repeat("a", 40)}, {Key: "vcs.modified", Value: "false"}}}
	for _, variant := range []string{"good", "missing", "dirty", "unknown", "duplicate", "revision", "module", "version"} {
		t.Run(variant, func(t *testing.T) {
			b := good
			b.Settings = append([]debug.BuildSetting(nil), good.Settings...)
			switch variant {
			case "missing":
				b.Settings = nil
			case "dirty":
				b.Settings[1].Value = "true"
			case "unknown":
				b.Settings[1].Value = ""
			case "duplicate":
				b.Settings = append(b.Settings, b.Settings[0])
			case "revision":
				b.Settings[0].Value = "ABC"
			case "module":
				b.Main.Path = "private.canary.invalid"
			case "version":
				b.Main.Version = "v1+canary"
			}
			p, err := suitePublicBuildInfo(&b)
			if variant == "good" {
				if err != nil || !reflect.DeepEqual(p, publicProducerFixture()) {
					t.Fatalf("producer: %#v %v", p, err)
				}
				if v, err := p.publicVersion(); err != nil || v != "devel-git-"+strings.Repeat("a", 40) {
					t.Fatal(v, err)
				}
			} else if err == nil {
				t.Fatal("accepted invalid provenance")
			}
		})
	}
}

func TestSuitePublicCompanionBindingAndLegacy(t *testing.T) {
	store, suite, raw := passStoreFixture(t)
	if err := store.reserve(t.Context()); err != nil {
		t.Fatal(err)
	}
	m := publicMetadataFixture()
	if err := store.publishPublicMetadata(t.Context(), raw, suite, m); err != nil {
		t.Fatal(err)
	}
	if _, err := store.readPublicMetadata(t.Context(), suite); err == nil {
		t.Fatal("orphan eligible")
	}
	if _, err := store.commit(t.Context(), raw, suite); err != nil {
		t.Fatal(err)
	}
	got, err := store.readPublicMetadata(t.Context(), suite)
	if err != nil || !reflect.DeepEqual(got.Producer, m.Producer) {
		t.Fatal(got, err)
	}
	// A legacy latest completion must not fall back to the older companion.
	commitStoreReceipt(t, store, suite, raw)
	if _, err := store.readPublicMetadata(t.Context(), suite); err == nil {
		t.Fatal("fell back to old companion")
	}
	if _, _, err := store.latest(t.Context(), suite); err != nil {
		t.Fatal("metadata changed private gate", err)
	}
}

func TestSuitePublicCompanionStrictAndCapacity(t *testing.T) {
	for _, variant := range []string{"unknown", "missing", "null", "duplicate", "hash", "workspace", "suite", "sequence", "oversize", "symlink", "public"} {
		t.Run(variant, func(t *testing.T) {
			store, suite, raw := passStoreFixture(t)
			if err := store.reserve(t.Context()); err != nil {
				t.Fatal(err)
			}
			m := publicMetadataFixture()
			if err := store.publishPublicMetadata(t.Context(), raw, suite, m); err != nil {
				t.Fatal(err)
			}
			if _, err := store.commit(t.Context(), raw, suite); err != nil {
				t.Fatal(err)
			}
			name := filepath.Join(store.partition, m.filename())
			data, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			switch variant {
			case "unknown":
				data = append([]byte(`{"canary":"secret",`), data[1:]...)
			case "missing":
				data = []byte(strings.Replace(string(data), `"modified":false,`, "", 1))
			case "null":
				data = []byte(strings.Replace(string(data), `"modified":false`, `"modified":null`, 1))
			case "duplicate":
				data = append([]byte(`{"schemaVersion":1,`), data[1:]...)
			case "hash":
				data = []byte(strings.ReplaceAll(string(data), m.ReceiptDigest, "sha256:"+strings.Repeat("b", 64)))
			case "workspace":
				data = []byte(strings.ReplaceAll(string(data), m.Workspace, strings.Repeat("b", 64)))
			case "suite":
				data = []byte(strings.ReplaceAll(string(data), m.SuiteDigest, "sha256:"+strings.Repeat("b", 64)))
			case "sequence":
				data = []byte(strings.Replace(string(data), `"sequence":1`, `"sequence":2`, 1))
			case "oversize":
				data = []byte(strings.Repeat("x", 4097))
			case "symlink":
				if err := os.Remove(name); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("missing", name); err != nil {
					t.Fatal(err)
				}
			case "public":
				if err := os.Chmod(name, 0644); err != nil {
					t.Fatal(err)
				}
			}
			if variant != "symlink" {
				if err := os.WriteFile(name, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := store.readPublicMetadata(t.Context(), suite); err == nil {
				t.Fatal("invalid companion accepted")
			}
			if _, _, err := store.latest(t.Context(), suite); err != nil {
				t.Fatal("private gate poisoned", err)
			}
		})
	}
	store, suite, raw := passStoreFixture(t)
	for i := 0; i < 256; i++ {
		name := filepath.Join(store.partition, "public-"+strings.Repeat("a", i%8)+time.Unix(int64(i), 0).Format("150405")+".json")
		if err := os.WriteFile(name, []byte("orphan"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.reserve(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := store.publishPublicMetadata(t.Context(), raw, suite, publicMetadataFixture()); err == nil {
		t.Fatal("capacity exceeded")
	}
	if _, err := store.commit(t.Context(), raw, suite); err != nil {
		t.Fatal("private commit blocked", err)
	}
}
