package app

import (
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
	for _, deny := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "required provenance refusal"}[deny], func(t *testing.T) {
			a, opt, _, _, _, _ := workspaceLiftFixture(t)
			var mu sync.Mutex
			objects, types, refs := map[string][]byte{}, map[string]string{}, map[string]string{}
			blobs, manifests, provenances, tags := 0, 0, 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
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
					w.Header().Set("Content-Type", "application/vnd.oci.image.index.v1+json")
					io.WriteString(w, `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[]}`)
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
						MediaType string `json:"mediaType"`
						Subject   *struct {
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
							w.WriteHeader(403)
							return
						}
						provenances++
						w.Header().Set("OCI-Subject", doc.Subject.Digest)
					}
					id := digest.FromBytes(raw).String()
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
			opt.Provenance = SuitePushProvenance{Enabled: true, Require: true}
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
			if deny {
				if err == nil || r.Push.Status != "unknown" || tagCount != 0 || len(records) != 0 || len(persistentOrkaCalls(t, os.Getenv("KMX_RECONCILE_DIR"))) != 0 {
					t.Fatalf("required provenance failure advanced: %+v %v", r, err)
				}
				return
			}
			if err != nil || r.Push.Status != "complete" || r.Lift.Status != "complete" || provenanceCount != 1 || tagCount != 1 || len(records) != 1 || !strings.Contains(r.Push.Detail, "no source commit") {
				t.Fatalf("actual publication/provenance not completed: %+v err=%v provenance=%d tags=%d records=%v", r, err, provenanceCount, tagCount, records)
			}
			p, err = PrepareWorkspaceLift(t.Context(), opt)
			if err != nil {
				t.Fatal(err)
			}
			r, err = a.LiftWorkspace(t.Context(), p)
			mu.Lock()
			same := copied == blobs && manifestCount == manifests
			mu.Unlock()
			if err != nil || r.Push.Status != "skipped" || r.Lift.Status != "skipped" || !same {
				t.Fatal("verified repeat rewrote uploaded graph", r, err)
			}
		})
	}
}
