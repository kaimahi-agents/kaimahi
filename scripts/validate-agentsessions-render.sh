#!/usr/bin/env bash
# Validate the golden AgentSessions ActorTemplate renders against substrate.
#
# kmx does not import substrate. This script checks out agent-substrate/substrate
# at the commit render.go pins (SubstrateAPICommit) into a temporary directory,
# then decodes every internal/kmx/agentsessions/testdata/render-*.golden.json
# actorTemplate with substrate's own protojson types and runs its
# CreateActorTemplate request validation. It fails on any decode or validation
# error. Two negative controls (the pre-string sandbox_class enum spelling and a
# template without resources) must be refused, so a validator that accepts
# everything cannot pass.
#
# Usage: scripts/validate-agentsessions-render.sh
#   SUBSTRATE_DIR=<clone>  read the commit from a local substrate clone instead
#                          of fetching it from GitHub.
# Requires git, tar and a Go toolchain that can build substrate (toolchain
# downloads follow GOTOOLCHAIN). Needs network access for Go modules.
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
render="$root/internal/kmx/agentsessions/render.go"
commit=$(sed -n 's/^const SubstrateAPICommit = "\([0-9a-f]\{40\}\)"$/\1/p' "$render")
if [ -z "$commit" ]; then
	echo "validate-agentsessions-render: no SubstrateAPICommit in $render" >&2
	exit 1
fi

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
src="$work/substrate"
mkdir "$src"
if [ -n "${SUBSTRATE_DIR:-}" ]; then
	git -C "$SUBSTRATE_DIR" archive "$commit" | tar -x -C "$src"
else
	git -C "$src" init -q
	git -C "$src" fetch -q --depth 1 https://github.com/agent-substrate/substrate "$commit"
	git -C "$src" checkout -q FETCH_HEAD
fi

pkg="$src/cmd/ateapi/internal/apivalidation"
mkdir "$pkg/kmxrender"
count=0
for golden in "$root"/internal/kmx/agentsessions/testdata/render-*.golden.json; do
	cp "$golden" "$pkg/kmxrender/"
	count=$((count + 1))
done
if [ "$count" -eq 0 ]; then
	echo "validate-agentsessions-render: no golden renders found" >&2
	exit 1
fi

cat >"$pkg/zz_kmx_render_test.go" <<'EOF'
package apivalidation

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"google.golang.org/protobuf/encoding/protojson"
)

func kmxRenderedTemplateErrors(t *testing.T, raw json.RawMessage) []string {
	t.Helper()
	var tmpl ateapipb.ActorTemplate
	if err := protojson.Unmarshal(raw, &tmpl); err != nil {
		return []string{"protojson: " + err.Error()}
	}
	var out []string
	for _, e := range ValidateCreateActorTemplateRequest(context.Background(), &ateapipb.CreateActorTemplateRequest{ActorTemplate: &tmpl}) {
		out = append(out, e.Error())
	}
	return out
}

func TestKMXRenderedActorTemplate(t *testing.T) {
	files, err := filepath.Glob("kmxrender/*.json")
	if err != nil || len(files) == 0 {
		t.Fatalf("no golden renders: %v", err)
	}
	for _, file := range files {
		t.Run(filepath.Base(file), func(t *testing.T) {
			raw, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			var doc struct {
				ActorTemplate map[string]json.RawMessage `json:"actorTemplate"`
			}
			if err := json.Unmarshal(raw, &doc); err != nil || doc.ActorTemplate == nil {
				t.Fatalf("no actorTemplate: %v", err)
			}
			template, err := json.Marshal(doc.ActorTemplate)
			if err != nil {
				t.Fatal(err)
			}
			if errs := kmxRenderedTemplateErrors(t, template); len(errs) != 0 {
				t.Fatalf("substrate refuses the rendered ActorTemplate: %v", errs)
			}
			t.Logf("accepted %s", file)

			// Negative controls: the validator must refuse known-bad shapes.
			var sandbox map[string]any
			if err := json.Unmarshal(doc.ActorTemplate["sandbox_config"], &sandbox); err != nil {
				t.Fatal(err)
			}
			sandbox["sandbox_class"] = "SANDBOX_CLASS_MICROVM"
			enum, _ := json.Marshal(sandbox)
			for name, edit := range map[string]func(map[string]json.RawMessage){
				"enum sandbox_class": func(m map[string]json.RawMessage) { m["sandbox_config"] = enum },
				"no resources":       func(m map[string]json.RawMessage) { delete(m, "resources") },
			} {
				changed := map[string]json.RawMessage{}
				for k, v := range doc.ActorTemplate {
					changed[k] = v
				}
				edit(changed)
				bad, err := json.Marshal(changed)
				if err != nil {
					t.Fatal(err)
				}
				if errs := kmxRenderedTemplateErrors(t, bad); len(errs) == 0 {
					t.Fatalf("negative control %q accepted", name)
				}
			}
		})
	}
}
EOF

echo "validate-agentsessions-render: substrate $commit, $count golden render(s)"
(cd "$src" && go test -count=1 -v -run '^TestKMXRenderedActorTemplate$' ./cmd/ateapi/internal/apivalidation/)
