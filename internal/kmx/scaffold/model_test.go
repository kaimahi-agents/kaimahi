package scaffold

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The scaffolder's vocabularies are DUPLICATES of the plane's, because
// the two are separate Go modules and the root one cannot import the
// plane — that boundary is deliberate (docs/repository-map.md) and this
// is the price of it.
//
// A duplicate that can drift is worse than none: a `--protocol` kmx
// accepts and the plane refuses is a scaffold that produces a config
// which will not load, discovered by a pod that will not start. So the
// pin is a test that reads the plane's own source and compares the
// literals, the same shape as the plane-image pin in kmx/app.
func TestTheScaffoldsProtocolVocabularyMatchesThePlane(t *testing.T) {
	source := planeConfigSource(t)
	for _, tc := range []struct {
		what  string
		re    *regexp.Regexp
		local []string
	}{
		// Protocols: the two ProtocolX constants in the plane's config.
		// The patterns enumerate no constant NAMES on purpose. A regex
		// naming the two it already knows would go on passing the day a
		// third arrived — it would match nothing new, find the same two,
		// and call the scaffolder's stale list correct. Matching the
		// SHAPE of the declaration is what makes an addition a failure.
		{"protocols", regexp.MustCompile(`\bProtocol[A-Z][A-Za-z]*\s+=\s+"([a-z_]+)"`), Protocols},
		{"classifications", regexp.MustCompile(`\bClass[A-Z][A-Za-z]*\s+=\s+"([a-z]+)"`), Classifications},
	} {
		var found []string
		for _, m := range tc.re.FindAllStringSubmatch(source, -1) {
			found = append(found, m[1])
		}
		if len(found) == 0 {
			t.Fatalf("no %s found in the plane's config source — the constants were renamed, "+
				"and this pin silently stopped pinning anything", tc.what)
		}
		sort.Strings(found)
		local := append([]string(nil), tc.local...)
		sort.Strings(local)
		if strings.Join(found, ",") != strings.Join(local, ",") {
			t.Fatalf("the scaffolder's %s are %v; the plane's are %v.\n"+
				"  These are separate modules and this list is a copy. A value kmx accepts and the plane\n"+
				"  refuses is a scaffold that produces a config which will not load.", tc.what, local, found)
		}
	}
}

// PathProtocol is a copy too, and the copy that matters most: it is what
// decides an upstream's protocol when the operator does not declare one.
// The plane applies the same rule at load, so a disagreement here would
// scaffold a fragment the plane then refuses for contradicting itself.
func TestPathProtocolAgreesWithThePlanesOwnRule(t *testing.T) {
	source := planeConfigSource(t)
	// The plane matches whole segments, not string suffixes. Pinning the
	// exact expressions is what makes a change to either side visible
	// here: a plain HasSuffix would read `v1/xresponses` as the Responses
	// API in the plane while this copy said otherwise.
	for _, expr := range []string{
		`p == "chat/completions" || strings.HasSuffix(p, "/chat/completions")`,
		`p == "responses" || strings.HasSuffix(p, "/responses")`,
	} {
		if !strings.Contains(source, expr) {
			t.Fatalf("the plane's PathProtocol no longer reads `%s` — the scaffolder's copy may now disagree", expr)
		}
	}
	for _, tc := range []struct{ path, want string }{
		{"v1/chat/completions", "chat_completions"},
		{"/v1/chat/completions/", "chat_completions"},
		{"openai/deployments/gpt/chat/completions", "chat_completions"},
		{"chat/completions", "chat_completions"},
		{"v1/responses", "responses"},
		{"responses", "responses"},
		{"api/generate", ""},
		{"", ""},
		// The boundary cases, and the reason this table is shared rather
		// than written twice: a suffix match reads both of these as a
		// protocol they are not, in both copies at once.
		{"v1/xresponses", ""},
		{"v1/notchat/completions", ""},
		{"v1/responsesx", ""},
	} {
		if got := PathProtocol(tc.path); got != tc.want {
			t.Fatalf("PathProtocol(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

// planeConfigSource reads the plane's config package as TEXT. It is not
// an import: the root module deliberately cannot import the plane.
func planeConfigSource(t *testing.T) string {
	t.Helper()
	path := filepath.Join("..", "..", "..", "plane", "internal", "config", "config.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the plane's config source at %s: %v", path, err)
	}
	return string(raw)
}

// The fragment is what the plane actually parses, so it has to state
// every field the table requires — and state the protocol explicitly
// rather than leaving it to be resolved a second time.
func TestTheFragmentStatesEveryFieldTheTableRequires(t *testing.T) {
	spec := ModelSpec{
		Name: "house", BaseURL: "http://vllm.demo:8000", Path: "v1/responses",
		Protocol: "responses", Classification: "free",
	}
	frag, err := spec.Fragment()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"upstreams"`, `"house"`, `"base_url": "http://vllm.demo:8000"`,
		`"path": "v1/responses"`, `"protocol": "responses"`, `"classification": "free"`,
	} {
		if !strings.Contains(frag, want) {
			t.Fatalf("the fragment is missing %s:\n%s", want, frag)
		}
	}
	// And nothing the overlay may not carry, in any spelling.
	for _, forbidden := range []string{"credential", "internet", "ca_file", "extra_headers", "prices"} {
		if strings.Contains(strings.ToLower(frag), forbidden) {
			t.Fatalf("the fragment carries %q, which an overlay may not set:\n%s", forbidden, frag)
		}
	}
}

// A model upstream's key is prefixed, because both seams share one
// overlay ConfigMap: a tool server and a model endpoint may carry the
// same name without one silently replacing the other.
func TestModelAndToolFragmentKeysCannotCollide(t *testing.T) {
	model := ModelSpec{Name: "house"}.FragmentKey()
	tool := UpstreamSpec{Name: "house"}.FragmentKey()
	if model == tool {
		t.Fatalf("a model and a tool upstream of the same name share the overlay key %q", model)
	}
	if !strings.HasSuffix(model, ".json") || !strings.HasPrefix(model, "model-") {
		t.Fatalf("the model fragment key %q is not the documented shape", model)
	}
}
