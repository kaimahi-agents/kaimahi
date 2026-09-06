// Package kaimahi exists for one reason: it is the only place in the tree
// from which a `go:embed` directive can reach k8s/.
//
// kmx is installed with `go install github.com/kaimahi-agents/kaimahi/cmd/kmx@<sha>`
// and then run from a directory that is not a clone — there is no k8s/ on
// disk to `kubectl apply -f`. Embed patterns are resolved relative to the
// source file's own directory and may not climb out of it, so the manifests
// have to be embedded by a file at the module root. That is this file, and
// that is all it does.
package kaimahi

import "embed"

// Manifests holds the manifests kmx applies: the runtime (the Ollama model
// server, kagent's helm values, the two agents), the governance plane
// (milestone 2), the model presets `kmx govern` and `kmx use` apply, and the
// governed RemoteMCPServer `kmx tools govern` puts the tools agent behind.
//
// The files are named individually rather than embedding `k8s` wholesale, so
// that what kmx carries is a decision rather than a side effect of a
// directory listing: the Slack, GitHub, inbound and egress manifests belong
// to families kmx does not own (they are entangled with secret capture,
// which stays in the scripts because kmx accepts a credential in no form)
// and must not ride along.
//
// `k8s/models/` IS embedded whole as of milestone 3, because `kmx use` is
// `make use` and `make use PRESET=anthropic` has always been a documented
// flow — a `kmx use` that handled only the keyless presets would be a
// regression the delegating recipe would inherit. This does not put a
// credential anywhere near kmx: a preset is a ModelConfig that NAMES a
// Secret (`apiKeySecret`), it never carries a key, and minting that Secret
// stays outside kmx — `make model-secret`, `make copilot-secret`, the
// scripts. kmx still accepts a credential in no form at all.
//
// k8s/wasm/runtime.yaml is the tool sandbox's runtime: a node installer and
// the RuntimeClass that selects it. It rides along because `kmx tools
// sandbox` has the same clone-free problem as the plane.
//
// `plane/` itself is NOT here and cannot be: it carries its own go.mod, and
// go:embed refuses to cross a module boundary ("cannot embed directory: in
// different module"). That is exactly why `kmx plane` FETCHES the plane's
// source from the public Go proxy at kmx's own revision and builds it, and
// why the manifest that deploys it can nevertheless travel in the binary.
//
//go:embed k8s/ollama.yaml k8s/kagent-values.yaml k8s/hello-world.yaml k8s/tools-agent.yaml
//go:embed k8s/kaimahi-tools.yaml
//go:embed k8s/plane/namespace.yaml k8s/plane/postgres.yaml k8s/plane/proxy.yaml
//go:embed k8s/plane/upstreams.yaml k8s/plane/network-policy.yaml
//go:embed k8s/models
//go:embed k8s/wasm/runtime.yaml
var Manifests embed.FS

// Blueprints holds the governed-workflow blueprints kmx carries,
// and the scripts their ungoverned steps run.
//
// Embedded for the same reason the manifests are, and it is the whole
// reason a blueprint is usable at all: the front door is `curl
// | sh` then `kmx quickstart`, with no Go and no checkout, so a blueprint
// that lived only in this repository's tree would make `git clone` a
// prerequisite again — for the one feature whose point is that a
// workflow is easy to express.
//
// `scripts/release-publish.sh` rides along because the release
// blueprint's publish step runs it: the DECISION is governed by the
// plane, the TRANSFER is this script moving artifacts with the operator's
// own `az` and `gh`, and a step that could not find its script on a
// machine with no checkout would be a step that only works for us.
//
//go:embed blueprints
//go:embed scripts/release-publish.sh
var Blueprints embed.FS
