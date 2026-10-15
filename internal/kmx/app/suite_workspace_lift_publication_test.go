package app

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/opencontainers/go-digest"
)

func TestWorkspaceLiftSkipPlanDisclosesOnlyMissingEffects(t *testing.T) {
	a, opt, _, _, _, _ := workspaceLiftFixture(t)
	a.Out, a.Err = io.Discard, io.Discard
	for run := 0; run < 2; run++ {
		p, err := PrepareWorkspaceLift(t.Context(), opt)
		if err != nil {
			t.Fatal(err)
		}
		r, err := a.LiftWorkspace(t.Context(), p)
		if err != nil {
			t.Fatal(err)
		}
		if run == 1 && (!strings.HasPrefix(r.Plan.Disclosure, "verify-only;") || strings.Contains(r.Plan.Disclosure, "fresh Tasks") || strings.Contains(r.Plan.Disclosure, "publication of")) {
			t.Fatalf("skip promised missing effects: %s", r.Plan.Disclosure)
		}
	}
}

// Implement the external registry boundary, retaining actual uploaded graph and
// provenance bytes. A tag is absent until the client successfully PUTs it.
func TestWorkspaceLiftPublishesRealGraphWithRequiredProvenance(t *testing.T) {
	for _, mode := range []string{"success", "required provenance refusal", "optional warning", "listing failure"} {
		t.Run(mode, func(t *testing.T) {
			deny, required := mode == "required provenance refusal" || mode == "optional warning", mode != "optional warning"
			a, opt, state, _, _, _ := workspaceLiftFixture(t)
			var mu sync.Mutex
			objects, types, refs := map[string][]byte{}, map[string]string{}, map[string]string{}
			var referrers []map[string]any
			listFailure := false
			blobs, manifests, provenances, tags, mutations := 0, 0, 0, 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				if r.Method != "GET" && r.Method != "HEAD" {
					mutations++
				}
				path := strings.TrimPrefix(r.URL.Path, "/v2/agents/")
				if strings.HasPrefix(path, "blobs/uploads/") {
					switch r.Method {
					case "POST":
						w.Header().Set("Location", "/v2/agents/blobs/uploads/upload")
						w.WriteHeader(202)
					case "PUT":
						raw, err := io.ReadAll(r.Body)
						if err != nil {
							t.Error(err)
							w.WriteHeader(500)
							return
						}
						id := digest.FromBytes(raw).String()
						if id != r.URL.Query().Get("digest") {
							t.Error("blob identity changed")
							w.WriteHeader(400)
							return
						}
						objects[id] = raw
						blobs++
						w.Header().Set("Docker-Content-Digest", id)
						w.Header().Set("Location", "/v2/agents/blobs/"+id)
						w.WriteHeader(201)
					default:
						t.Errorf("unexpected upload method %s", r.Method)
						w.WriteHeader(405)
					}
					return
				}
				if strings.HasPrefix(path, "referrers/") {
					if listFailure {
						w.WriteHeader(403)
						return
					}
					w.Header().Set("Content-Type", "application/vnd.oci.image.index.v1+json")
					_ = json.NewEncoder(w).Encode(map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.index.v1+json", "manifests": referrers})
					return
				}
				kind, ref, ok := strings.Cut(path, "/")
				if !ok || kind != "manifests" && kind != "blobs" {
					w.WriteHeader(404)
					return
				}
				if r.Method == "PUT" && kind == "manifests" {
					raw, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
						w.WriteHeader(500)
						return
					}
					var doc struct {
						MediaType    string `json:"mediaType"`
						ArtifactType string `json:"artifactType"`
						Subject      *struct {
							Digest string `json:"digest"`
						} `json:"subject"`
					}
					if json.Unmarshal(raw, &doc) != nil {
						t.Error("invalid manifest")
						w.WriteHeader(400)
						return
					}
					if doc.Subject != nil {
						if deny {
							w.Header().Set("Content-Type", "application/json")
							w.WriteHeader(403)
							_ = json.NewEncoder(w).Encode(map[string]any{"errors": []map[string]string{{"code": "DENIED", "message": "registry said \u001b[31mred\u001b[0m\nspoofed stage"}}})
							return
						}
						provenances++
						w.Header().Set("OCI-Subject", doc.Subject.Digest)
					}
					id := digest.FromBytes(raw).String()
					if doc.Subject != nil {
						referrers = append(referrers, map[string]any{"mediaType": doc.MediaType, "artifactType": doc.ArtifactType, "digest": id, "size": len(raw)})
					}
					objects[id], types[id], refs[ref] = raw, doc.MediaType, id
					manifests++
					if strings.HasPrefix(ref, "s-") {
						tags++
					}
					w.Header().Set("Docker-Content-Digest", id)
					w.Header().Set("Location", "/v2/agents/manifests/"+id)
					w.WriteHeader(201)
					return
				}
				if r.Method != "HEAD" && r.Method != "GET" {
					t.Errorf("unexpected registry method %s", r.Method)
					w.WriteHeader(405)
					return
				}
				if id, ok := refs[ref]; ok {
					ref = id
				}
				raw, ok := objects[ref]
				if !ok {
					w.WriteHeader(404)
					return
				}
				w.Header().Set("Docker-Content-Digest", ref)
				w.Header().Set("Content-Length", strconv.Itoa(len(raw)))
				if kind == "manifests" {
					w.Header().Set("Content-Type", types[ref])
				}
				if r.Method == "GET" {
					w.Write(raw)
				}
			}))
			defer server.Close()
			opt.Repository = strings.TrimPrefix(server.URL, "http://") + "/agents"
			opt.Provenance = SuitePushProvenance{Enabled: true, Require: required}
			a.Out, a.Err = io.Discard, io.Discard
			p, err := PrepareWorkspaceLift(t.Context(), opt)
			if err != nil {
				t.Fatal(err)
			}
			r, err := a.LiftWorkspace(t.Context(), p)
			mu.Lock()
			copied, manifestCount, provenanceCount, tagCount := blobs, manifests, provenances, tags
			mu.Unlock()
			records, e := filepath.Glob(filepath.Join(p.store.partition, "lift-publication-*.json"))
			if e != nil {
				t.Fatal(e)
			}
			if copied == 0 || manifestCount == 0 {
				t.Fatal("did not actually upload graph", copied, manifestCount)
			}
			if deny && required {
				if err == nil || r.Push.Status != "unknown" || tagCount != 0 || len(records) != 0 || len(persistentOrkaCalls(t, os.Getenv("KMX_RECONCILE_DIR"))) != 0 {
					t.Fatalf("required provenance failure advanced: %+v %v", r, err)
				}
				return
			}
			if mode == "optional warning" {
				if err != nil || r.Push.Status != "complete" || r.Lift.Status != "complete" || provenanceCount != 0 || tagCount != 1 || !strings.Contains(r.Push.Detail, "provenance was not attached") || strings.ContainsAny(r.Push.Detail, "\x1b\n\r") {
					t.Fatalf("unsafe/missing optional registry warning: %q %v", r.Push.Detail, err)
				}
				return
			}
			if err != nil || r.Push.Status != "complete" || r.Lift.Status != "complete" || provenanceCount != 1 || tagCount != 1 || len(records) != 1 || !strings.Contains(r.Push.Detail, "no source commit") {
				t.Fatalf("actual publication/provenance not completed: %+v err=%v provenance=%d tags=%d records=%v", r, err, provenanceCount, tagCount, records)
			}
			beforeRecord, e := os.ReadFile(records[0])
			if e != nil {
				t.Fatal(e)
			}
			beforeCreates := len(state.creates)
			mu.Lock()
			beforeMutations := mutations
			listFailure = mode == "listing failure"
			mu.Unlock()
			p, err = PrepareWorkspaceLift(t.Context(), opt)
			if err != nil {
				t.Fatal(err)
			}
			r, err = a.LiftWorkspace(t.Context(), p)
			mu.Lock()
			same := copied == blobs && manifestCount == manifests && beforeMutations == mutations
			mu.Unlock()
			afterRecord, e := os.ReadFile(records[0])
			if e != nil || !bytes.Equal(beforeRecord, afterRecord) || len(state.creates) != beforeCreates || !same {
				t.Fatal("repeat mutated stored/remote state", e)
			}
			if listFailure {
				if err == nil || !strings.Contains(err.Error(), "provenance") || r.Push.Status != "not-attempted" || r.Lift.Status != "not-attempted" {
					t.Fatal("unverifiable required provenance skipped", r, err)
				}
				return
			}
			if err != nil || r.Push.Status != "skipped" || r.Lift.Status != "skipped" || !same {
				t.Fatal("verified repeat rewrote uploaded graph", r, err)
			}
		})
	}
}
