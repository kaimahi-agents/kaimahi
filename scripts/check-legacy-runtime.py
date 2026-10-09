#!/usr/bin/env python3
"""Keep Kagent support inside the explicit exact-v0.10.2 create boundary.

kmx intentionally supports one narrow operation against a preinstalled exact
Kagent v0.10.2: offline render and create-only ModelConfig + Agent deployment,
with an optional one-shot A2A task. It still does not install or upgrade the
runtime, restore its old fixed manifests/presets or CLI, expose it through
other commands, or revive the retired gateway and approval implementation.

This is a scoped-support scanner, not a broad word allowlist:

  SCOPED VOCABULARY is the bare name, API group, v1alpha2, ModelConfig,
  RemoteMCPServer, official image namespace, and create runtime selector.
  Those spellings are allowed only in a closed set of production, test,
  current-documentation, and CI-workflow files that implement, describe, or
  prove create. A path
  grants only those named rules; it is not a whole-file bypass. The selector
  additionally needs create context on its own line.

  MULTILINE COMPOUNDS are checked in bounded windows of adjacent physical
  lines. This closes the newline escape for broad claims, command/source
  composition, option structs, installers, and retired operations. A window
  finding must actually span its first and last lines. Exact-line exemptions
  are still matched and credited only against one original physical line, so
  an overlap cannot widen one or make a stale exemption look used.

  CLOSED SURFACES remain refused even in scoped support files: installer and
  demo/preset assets, moving or non-v0.10.2 version claims, old CLI/shim/compat
  packages, up steps and AKS payloads, runtime flags on non-create commands,
  govern/use/edit, and positive claims of broader operation.

WHAT MAY STAY, AND NOTHING ELSE. Exact-line exemptions retain six categories,
each bound to paths where its claim could be true. A category is an argument
for why a line may stay, and the same argument is not available everywhere:

  historical    the changelog and dated reviews are the historical record,
                named as whole files. No current documentation may be
                listed as historical, and a floor below enforces this.
  retirement    exact production lines that refuse retired surfaces
                BY NAME, plus the historical teardown sentinel. A refusal
                has to spell what it refuses, or it refuses nothing. Only
                in the closed set of production files that do the refusing.
  negative      exact lines asserting the runtime is ABSENT: the CI
                tripwires and the bounded test cases. A negative assertion
                is the opposite of support — but only a test or the CI
                tripwires can make one, so a document cannot borrow the
                category to keep an instruction.
  future        exact lines noting an explicitly UNSUPPORTED future: a v1
                authoring surface that is an open question. A note that
                something is not supported is not support — and the line
                or its reason has to SAY so.
  retired-notice  current documentation that explicitly says a named command
                 is gone. An executable surface cannot borrow this exemption.
  fixture         exactly four reviewed official chart pull/install commands in the
                 CI workflow. They waive only installer/retired-asset findings
                 while preparing the disposable test cluster outside KMX; no
                 other path, line, or rule can borrow this category.

The scoped support paths, categories, and waivable rules are duplicated in
code and JSON and must match exactly. Adding a path or rule is therefore a
policy change, not an allowlist convenience.

HOW IT FAILS, AND WHY THAT MATTERS MORE THAN HOW IT PASSES. A scanner is a
gate that fails OPEN. Every way this one could quietly stop working is refused:

  - an enumeration that produced no files is not a clean tree;
  - files enumerated and none read is not a clean tree;
  - fewer files than the tree is known to have is a shrunken scan;
  - an allowlist entry matching nothing is stale, and a stale allowlist is
    how an exemption outlives the line it was written for;
  - a rule whose pattern stops matching its own example is gone;
  - a rule whose pattern also matches its counterexample is too wide to
    mean anything;
  - an exemption or support entry whose category cannot fit its path is an
    argument borrowed from somewhere it was earned;
  - a support path or waivable rule outside the closed set widens the boundary;
  - every existing support file is scanned directly in selftest, including
    intended files that are still untracked and absent from git ls-files;
  - a whole file declared historical outside the record itself is a
    directory-sized exemption wearing a filename.

Run:  python3 scripts/check-legacy-runtime.py
      python3 scripts/check-legacy-runtime.py --selftest
"""
from __future__ import annotations

import json
import os
import pathlib
import re
import subprocess
import sys
import tempfile

ROOT = pathlib.Path(__file__).resolve().parents[1]
ALLOWLIST = ROOT / "scripts" / "legacy-runtime-allowlist.json"

# This file and its allowlist have to spell what they forbid. Nothing else
# is exempt — not even this checker's own mutation specification, which
# quotes lines of source from here and is written so that none of the lines
# it quotes carries a banned literal. The floor below pins the set to
# exactly these two, so a third name cannot be added in silence.
SELF = {
    "scripts/check-legacy-runtime.py",
    "scripts/legacy-runtime-allowlist.json",
}

# Binary and raster image files: SVGs are text and must be scanned.
SKIP_SUFFIX = {".png", ".jpg", ".jpeg", ".gif", ".pdf", ".ico", ".woff", ".woff2"}

# The surfaces a reader takes as current. The bare name is refused on all of
# them, workflows included: a workflow runs commands, and `kubectl create
# namespace <name>` is as much a claim of support as a sentence in a guide.
# The assertions that a cluster does NOT have the runtime are still allowed,
# but one line at a time and by name, like every other exemption.
CURRENT_SURFACES = (
    "cmd/", "internal/", "plane/", "k8s/", "scripts/", "docs/", ".github/",
    "Makefile", "install.sh", "embed.go", "go.mod", "CONTRIBUTING.md", "README.md",
)

# A tree this size cannot shrink to a handful of files without something
# being wrong with the enumeration rather than with the tree.
MIN_TRACKED = 200
MIN_READ = 150

# The files that ARE the record, whole. Nothing else may be declared
# history wholesale, and a floor below fails on any other name: a whole
# file is the widest exemption in this policy, so the set is closed here
# rather than in the allowlist it judges.
HISTORICAL_FILES = ("CHANGELOG.md", "docs/reviews/")
CI_WORKFLOW = ".github/workflows/ci.yml"

# The only current files that may carry the vocabulary of the explicit Kagent
# v0.10.2 create implementation. This is duplicated, with categories and a
# reason, in the JSON that the checker judges. Changing code alone or policy
# data alone therefore fails closed.
SCOPED_SUPPORT_FILES = {
    "production": frozenset({
        "cmd/kmx/agent_commands.go",
        "internal/kmx/app/agent_bundle_evaluate.go",
        "internal/kmx/app/create.go",
        "internal/kmx/app/create_kagent.go",
        "internal/kmx/app/kagent_create_online.go",
        "internal/kmx/app/runtime_kagent_lifecycle.go",
        "internal/kmx/runtime/kagent_bindings.go",
        "internal/kmx/runtime/portable.go",
        "internal/kmx/runtime/prepared.go",
        "internal/kmx/runtime/runtime.go",
        "internal/kmx/scaffold/kagent.go",
    }),
    "test": frozenset({
        "cmd/kmx/agent_create_test.go",
        "internal/kmx/app/agent_bundle_runtime_refusal_test.go",
        "internal/kmx/app/bundle_format_compat_test.go",
        "internal/kmx/app/create_kagent_test.go",
        "internal/kmx/app/testdata/kubectl/kagent_test.go",
        "internal/kmx/runtime/kagent_bindings_test.go",
        "internal/kmx/runtime/lifecycle_test.go",
        "internal/kmx/runtime/portable_secretshapes_test.go",
        "internal/kmx/runtime/portable_test.go",
        "internal/kmx/scaffold/kagent_test.go",
    }),
    "bundle-fixture": frozenset({
        "internal/kmx/app/testdata/bundle-format/kagent/agent.yaml",
        "internal/kmx/app/testdata/bundle-format/kagent/bindings.yaml",
    }),
    # Keep this list to documents that currently carry the new contract. The
    # other docs named by the rollout earn no exemption until they need one.
    "current-doc": frozenset({
        "CONTRIBUTING.md",
        "README.md",
        "docs/FAQ.md",
        "docs/README.md",
        "docs/agent-lift.md",
        "docs/aks.md",
        "docs/bundle-format.md",
        "docs/development.md",
        "docs/entry-point-principles.md",
        "docs/getting-started.md",
        "docs/interactive-agent-tui-plan.md",
        "docs/interactive-chat.md",
        "docs/kmx.md",
        "docs/migrate.md",
        "docs/orka.md",
        "docs/releases.md",
        "docs/repository-map.md",
        "docs/runtime-adapters.md",
    }),
    # The workflow proves the same narrow create contract. It waives only
    # SCOPED_SUPPORT_RULES; the two external installer preconditions still
    # need their exact fixture entries.
    "workflow": frozenset({
        CI_WORKFLOW,
    }),
}

# Only vocabulary rules can be waived by a scoped support path. Command, pin,
# asset, path, and broad-operation rules remain active in every support file.
SCOPED_SUPPORT_RULES = frozenset({
    "api-group", "api-kind", "schema-version", "image", "create-selector", "bare-name",
})

# The production files whose job is to REFUSE retired surfaces by name.
# A retirement exemption anywhere else is a mention arguing it is a refusal.
RETIREMENT_FILES = (
    "cmd/kmx/root.go",
    "internal/kmx/app/chat.go", "internal/kmx/app/lift.go",
    "internal/kmx/lift/plan.go", "internal/kmx/lift/record.go",
    "internal/kmx/scaffold/agent.go",
)

# A `future` entry has to SAY the future is unsupported. Without this the
# category is a free-form note, and a free-form note beside a mention is
# indistinguishable from documentation of a supported path.
UNSUPPORTED = ("unsupported", "not supported", "no support", "remains open", "open question")

CATEGORIES = ("retirement", "negative", "historical", "future", "retired-notice", "fixture")

# Exact-line exemptions remain arguments, not arbitrary waivers. A negative
# assertion may exercise these retired/vocabulary rules, but it can never bless
# a future version or a newly restored operation merely by naming the line.
NEGATIVE_RULES = frozenset({
    "api-group", "api-kind", "asset", "bare-name", "create-selector",
    "fixed-demo-asset", "installer", "pin", "retired-command", "retired-payload",
    "retired-preset-command", "retired-up-step", "schema-version",
    "unsupported-runtime-command", "unsupported-runtime-options", "wrong-api-version",
})

# These current-doc notices were reviewed as refusals, not instructions.
# Unlike a grammar heuristic, this closed set cannot bless a new sentence
# merely because it includes words such as "no" or "removed". Changing one
# requires updating both the code and its reasoned exact-line allowlist.
RETIRED_NOTICES = frozenset({
    ("docs/FAQ.md", "owner-managed application back behind the plane; `kmx govern` was removed with"),
    ("docs/interactive-chat.md", "and `--runtime kagent` is refused rather than resolved to Orka. Those"),
    ("docs/kmx.md", "`kmx agent edit` is retired; its hidden stub points to kubectl."),
    ("docs/kmx.md", "`kmx govern` is retired; its hidden stub points to `kmx migrate`."),
    ("docs/spend.md", "credential. (`kmx govern`, which did this for a legacy Agent, was removed with"),
})


def current_doc(path: str) -> bool:
    """True for documentation a reader is expected to act on today."""
    return path.endswith(".md") and (path.startswith("docs/")
                                      or path in {"README.md", "CONTRIBUTING.md"})


def support_category_problem(category: str, path: str) -> str | None:
    """Why a scoped support path cannot borrow this path category."""
    if category == "production":
        if (not path.endswith(".go") or path.endswith("_test.go")
                or not path.startswith(("cmd/", "internal/"))):
            return "production support must be a non-test Go file under cmd/ or internal/"
    elif category == "test":
        if not path.endswith("_test.go") or not path.startswith(("cmd/", "internal/")):
            return "test support must be a Go test under cmd/ or internal/"
    elif category == "bundle-fixture":
        if path not in SCOPED_SUPPORT_FILES["bundle-fixture"]:
            return "bundle fixture support is restricted to the two pinned Kagent YAML files"
    elif category == "current-doc":
        if not current_doc(path):
            return "current-doc support must be current Markdown under docs/ or a root guide"
    elif category == "workflow":
        if path != CI_WORKFLOW:
            return f"workflow support is restricted to exactly {CI_WORKFLOW}"
    return None


def category_problem(entry) -> str | None:
    """Why this entry's category cannot be true of this entry's path.

    Every category is an ARGUMENT for keeping a line, and an argument is
    only available where it could hold. A test may assert an absence; a
    guide may not call an instruction an assertion. Production code may
    refuse a retired surface by name, but only the closed files that do. Without
    this, the widest category in the list is one word away from any line in
    the tree, and the reason column stops being reviewable.
    """
    path, category = entry["path"], entry["category"]
    if category == "negative":
        if not (path.endswith("_test.go") or path == ".github/workflows/ci.yml"):
            return ("only a Go test or the CI tripwires in .github/workflows/ci.yml can assert "
                    "the runtime is ABSENT; on any other surface the same words are an instruction")
        if path == CI_WORKFLOW and not ci_negative_control(entry["text"].strip()):
            return "a CI negative exemption must be an inert quoted sample, regex, or closed absence assertion"
    elif category == "retirement":
        if path not in RETIREMENT_FILES:
            return ("a retirement refusal is production code that refuses the runtime BY NAME, "
                    f"and only these do so: {', '.join(RETIREMENT_FILES)}")
    elif category == "historical":
        return ("historical exemptions are whole files only: "
                f"{', '.join(HISTORICAL_FILES)}; current documents are not history")
    elif category == "retired-notice":
        if not current_doc(path):
            return "a retired command notice belongs in current documentation, not executable code"
        if (path, entry["text"].strip()) not in RETIRED_NOTICES:
            return "only the closed, reviewed set of exact retirement notices may be exempted"
    elif category == "future":
        if not current_doc(path):
            return "a note about an unsupported future belongs in current documentation"
        said = (entry["text"] + " " + entry["why"]).lower()
        if not any(marker in said for marker in UNSUPPORTED):
            return ("neither the line nor its reason says the future is unsupported "
                    f"(looked for: {', '.join(UNSUPPORTED)})")
    elif category == "fixture":
        if path != CI_WORKFLOW:
            return f"an external runtime fixture is allowed only in {CI_WORKFLOW}"
        if entry["text"].strip() not in FIXTURE_LINES:
            return "only the four closed official chart pull/install lines are runtime fixtures"
    return None


class Rule:
    """One policy boundary, with samples that keep its pattern honest."""

    def __init__(self, name, what, regexp, example, counterexample, everywhere):
        self.name = name
        self.what = what
        self.re = re.compile(regexp)
        self.example = example
        self.counterexample = counterexample
        # True: refused in every tracked file. False: refused only on the
        # surfaces a reader takes as current.
        self.everywhere = everywhere


# The runtime's own name, assembled rather than written, so mutation fixtures
# can quote source from this checker without becoming policy findings.
NAME = "k" + "agent"

CI_NEGATIVE_ASSERTIONS = frozenset({
    f'assert "{NAME}" not in r["manifest"], r["manifest"]',
    f"grep -i {NAME} quickstart.out; exit 1",
    f"grep -i {NAME} up-after-quickstart.out; exit 1",
    f"grep -q -- '--- PASS: TestChatRefusesTheRetired{NAME.capitalize()}RuntimeWithoutReachingACluster' retirement.out",
    f'if "$k" --context kind-kmx-quickstart get ns {NAME} >/dev/null 2>&1; then',
    f"if grep -qi '{NAME}' quickstart.out; then",
    f"if grep -qi '{NAME}' up-after-quickstart.out; then",
    f"if kubectl --context kind-kaimahi-p1 get namespace {NAME} >/dev/null 2>&1; then",
    f"if kubectl --context kind-kmx-orka get namespace {NAME} >/dev/null 2>&1; then",
    f'if not re.search(r"get (?:ns|namespace) {NAME}\\b", body):',
})


def ci_negative_control(text: str) -> bool:
    """True only for inert CI samples/patterns and closed absence assertions."""
    return ((text.startswith('"') and text.endswith('",'))
            or text.startswith('(re.compile(r"')
            or text.startswith('(r"')
            or text in CI_NEGATIVE_ASSERTIONS)

# The only chart operations in current support. They pull official,
# content-addressed charts and install the verified archives into a disposable
# CI cluster before KMX runs. Building the strings from NAME lets the mutation
# specification quote these definitions without becoming a finding here.
CRDS_PULL_LINE = (
    f"helm pull oci://ghcr.io/{NAME}-dev/{NAME}/helm/{NAME}-crds@"
    "sha256:d487e679001b1a666e0ec97a0a87aab8a10bc1f52acba92b36b70c195492c7c3 "
    '--untar --untardir "$charts/crds"'
)
CHART_PULL_LINE = (
    f"helm pull oci://ghcr.io/{NAME}-dev/{NAME}/helm/{NAME}@"
    "sha256:cd8a8fe8db81e193f8a82c4d20e9162781476d2a4c1795234d107e2743ec8366 "
    '--untar --untardir "$charts/runtime"'
)
CRDS_FIXTURE_LINE = (
    f'helm install {NAME}-crds "$crds_chart" '
    f"--namespace {NAME} --create-namespace "
    f"--kube-context kind-kmx-{NAME}-create --set kmcp.enabled=false --wait --timeout 5m"
)
CHART_FIXTURE_LINE = (
    f'helm install {NAME} "$runtime_chart" '
    f"--namespace {NAME} --kube-context kind-kmx-{NAME}-create "
    f"--set kmcp.enabled=false --set {NAME}-tools.enabled=false "
    "--set k8s-agent.enabled=false --set kgateway-agent.enabled=false "
    "--set istio-agent.enabled=false --set promql-agent.enabled=false "
    "--set observability-agent.enabled=false --set argo-rollouts-agent.enabled=false "
    "--set helm-agent.enabled=false --set cilium-policy-agent.enabled=false "
    "--set cilium-manager-agent.enabled=false --set cilium-debug-agent.enabled=false "
    "--set grafana-mcp.enabled=false --set oauth2-proxy.enabled=false "
    "--set providers.default=ollama "
    "--set-string providers.ollama.config.host=http://unused.invalid:11434 "
    "--set ui.replicas=0 --wait --timeout 10m"
)
FIXTURE_LINES = frozenset({CRDS_PULL_LINE, CHART_PULL_LINE, CRDS_FIXTURE_LINE, CHART_FIXTURE_LINE})
FIXTURE_RULES = frozenset({"installer", "asset"})
# Cobra accepts its persistent flags before or inside the command path.
KMX_OPTIONS = r"(?:\s+--?(?:context|container-engine)(?:=|\s+)\S+)*"
RUNTIME_SELECTOR = (r"(?:--runtime(?:=|\s+)" + NAME + r"\b|"
                    r"[\"']--runtime[\"']\s*,\s*[\"']" + NAME + r"[\"'])")
COBRA_RUNTIME_DEFAULT = r"[\"']runtime[\"']\s*,\s*[\"']" + NAME + r"[\"']"
KAGENT_FLAG = (r"(?:" + RUNTIME_SELECTOR + r"|" + COBRA_RUNTIME_DEFAULT + r"|"
               r"--(?:legacy-)?" + NAME + r"(?:-[a-z0-9-]+)?\b)")
UNSUPPORTED_RUNTIME_OPERATION = (
    r"(?:\bkmx" + KMX_OPTIONS +
    r"\s+(?:agent" + KMX_OPTIONS +
    r"\s+(?:chat|list|show|lift|status|evaluate)\b|"
    r"(?:console|quickstart(?:-wizard)?|status)\b)|"
    r"\bcobra\.Command\s*\{.{0,160}\bUse\s*:\s*[\"'](?:chat|list|show|lift|status|evaluate)\b|"
    r"[\"']agent[\"']\s*,\s*[\"'](?:chat|list|show|lift|status|evaluate)[\"']|"
    r"[\"'](?:console|quickstart(?:-wizard)?|status)[\"'])"
)
CREATE_SELECTOR_CONTEXT = re.compile(
    (r"(?i:(?:\b(?:kmx\s+)?agent\s+create\b|"
     r"[\"']agent[\"']\s*,\s*[\"']create[\"']|"
     r"\bcreate(?:args)?\b|--" + NAME + r"-runtime\b|\bOrka-only\b).{0,160}" +
     RUNTIME_SELECTOR + r"|" + RUNTIME_SELECTOR +
     r".{0,160}(?:\bcreate\b|--" + NAME + r"-runtime\b))")
)

# Compound syntax and prose may wrap naturally, but unrelated paragraphs must
# not be joined into a claim. Only these non-waivable compound rules receive a
# two-to-four-line normalized view; token rules continue to inspect their
# original physical line. The literal set and bound are pinned again in the
# floors and exercised by mutations, because growing either silently changes
# what the policy means.
MULTILINE_WINDOW_LINES = 4
MULTILINE_RULES = frozenset({
    "asset", "installer", "moving-version", "unsupported-runtime-command",
    "unsupported-runtime-options", "retired-up-step", "retired-payload",
    "retired-preset-command", "retired-command", "broad-operation",
})

BROAD_OPERATIONS = (
    r"(?:chat|list|show|lift|status|evaluate|console|quickstart|install(?:ation|er)?|"
    r"upgrad(?:e|es|ing)|updat(?:e|es|ing)|delet(?:e|es|ion|ing)|discover(?:y|ies|ing)?|"
    r"invok(?:e|es|ing)|invocation|lifecycle|AKS|payload|gateway|approval|grant|resume|session)"
)
# Positive-claim gaps cannot cross explicit refusal language. In particular,
# `Kagent does not support` followed by a wrapped operation is truthful policy,
# not a broad-support claim.
BROAD_NEGATION = r"(?:no|not|never|without|unsupported|unavailable|refused|retired|removed)"
BROAD_GAP_100 = r"(?:(?!\b" + BROAD_NEGATION + r"\b).){0,100}"
BROAD_GAP_50 = r"(?:(?!\b" + BROAD_NEGATION + r"\b).){0,50}"
BROAD_GAP_40 = r"(?:(?!\b" + BROAD_NEGATION + r"\b).){0,40}"
BROAD_GAP_30 = r"(?:(?!\b" + BROAD_NEGATION + r"\b).){0,30}"

RULES = [
    Rule("api-group", "the Kagent API group outside scoped create support",
         NAME + r"\.dev", NAME + ".dev/v1alpha2", "core.orka.ai/v1alpha1", True),
    Rule("api-kind", "a Kagent API kind outside scoped create support",
         r"\b(?:ModelConfig|RemoteMCPServer)s?\b|\b(?:modelconfig|remotemcpserver)s?\b",
         "kind: ModelConfig", "kind: Provider", True),
    Rule("schema-version", "Kagent's v1alpha2 schema outside scoped create support",
         r"\bv1alpha2\b", "apiVersion: " + NAME + ".dev/v1alpha2",
         "apiVersion: core.orka.ai/v1alpha3", True),
    Rule("image", "a Kagent image namespace outside scoped create support",
         NAME + r"-dev\b", "ghcr.io/" + NAME + "-dev/" + NAME + "/tools:0.2.1",
         "ghcr.io/kaimahi-agents/kaimahi-proxy:p10", True),
    Rule("pin", "moving Kagent version configuration",
         r"\b" + NAME.upper() + r"_VERSION\b", NAME.upper() + "_VERSION ?= 0.9.12",
         "ORKA_VERSION ?= v0.1.3", True),
    Rule("asset", "a retired Kagent installer, CLI, shim, or compatibility asset",
         NAME + r"-(?:tools?|tool-server|values|shim|cli)\b|\b" + NAME +
         r"(?:cli|compat)\b|(?i:\b" + NAME +
         r"\b.{0,40}\b(?:CLI|binary)\b.{0,40}\b(?:download(?:er)?|fetch)\b|\b" +
         NAME + r"\s+CLI\s+(?:package|download(?:er)?)\b)",
         NAME + "-tool-server", NAME.capitalize() + " bindings", True),
    Rule("fixed-demo-asset", "a retired fixed Kagent demo or model-preset asset",
         (r"(?:k8s/)?(?:hello-world|tools-agent)\.yaml\b|"
          r"\bmodels/(?:ollama|governed-ollama|governed-copilot)\.yaml\b"),
         "apply k8s/hello-world.yaml", "agent create hello-world --out demo.yaml", True),
    Rule("installer", "a Kagent installer operation or chart",
         (r"(?i:\bhelm\s+(?:install|upgrade)\b.{0,160}\b" + NAME + r"\b)|"
          r"/" + NAME + r"/helm/|"
          r"(?i:\bkubectl\b.{0,120}\b(?:apply|create|replace)\b.{0,80}"
          r"(?:-f|--filename|-k|--kustomize)(?:=|\s+)\S*" + NAME + r")|"
          r"(?i:\bkubectl\b.{0,80}\bcreate\s+(?:ns|namespace)\s+" + NAME + r"\b)"),
         "helm install " + NAME + " oci://ghcr.io/" + NAME + "-dev/" + NAME + "/helm/" + NAME,
         "inspect the preinstalled exact " + NAME.capitalize() + " v0.10.2", True),
    Rule("moving-version", "an unpinned or non-v0.10.2 Kagent support claim",
         (r"(?i:(?:\b(?:latest|main|HEAD)\b|(?<![A-Za-z0-9.])v?(?!0\.10\.2(?:\b|$))"
          r"\d+\.\d+\.\d+(?![A-Za-z0-9.])).{0,80}\b" + NAME + r"\b|\b" + NAME +
          r"\b.{0,80}(?:\b(?:latest|main|HEAD)\b|(?<![A-Za-z0-9.])v?(?!0\.10\.2(?:\b|$))"
          r"\d+\.\d+\.\d+(?![A-Za-z0-9.])))"),
         "support " + NAME + " latest", "exact " + NAME + " v0.10.2", True),
    Rule("wrong-api-version", "a Kagent API version other than v1alpha2",
         NAME + r"\.dev/v(?!1alpha2\b)[A-Za-z0-9.-]+",
         NAME + ".dev/v1alpha1", NAME + ".dev/v1alpha2", True),
    Rule("create-selector", "the Kagent runtime selector outside scoped create support",
         RUNTIME_SELECTOR, "kmx agent create demo --runtime " + NAME,
         "kmx agent create demo --runtime orka", True),
    Rule("unsupported-runtime-command", "a Kagent flag on a non-create command",
         r"^(?=.*" + KAGENT_FLAG + r")(?=.*" + UNSUPPORTED_RUNTIME_OPERATION + r").*$",
         "kmx agent chat --runtime " + NAME + " demo",
         "kmx agent create demo --runtime " + NAME, True),
    Rule("unsupported-runtime-options", "Kagent selected in a non-create options struct",
         (r"(?i:\b(?:Chat|List|Show|Lift|Status|Evaluate|Console|Quickstart)Options\s*\{"
          r"[^\n}]*\bRuntime\s*:\s*[\"']" + NAME + r"[\"'])"),
         "ChatOptions{Runtime: \"" + NAME + "\"}",
         "CreateOptions{Runtime: \"" + NAME + "\"}", True),
    Rule("retired-up-step", "a retired Kagent or demo-agent installation step",
         (r"--step(?:=|\s+)(?:" + NAME + r"|agent|tools-agent)\b|"
          r"[\"']--step[\"']\s*,\s*[\"'](?:" + NAME + r"|agent|tools-agent)[\"']"),
         "kmx up --step " + NAME, "kmx up --step orka", True),
    Rule("retired-payload", "the retired Kagent AKS payload",
         (r"--payload(?:=|\s+)" + NAME + r"\b|"
          r"[\"']--payload[\"']\s*,\s*[\"']" + NAME + r"[\"']"),
         "kmx aks up --payload " + NAME, "kmx aks up --payload orka", True),
    Rule("retired-preset-command", "a retired preset-switch command",
         (r"\b(?:kmx" + KMX_OPTIONS + r"|make)\s+use\b|"
          r"[\"'](?:govern|use)[\"']\s*,|"
          r"[\"']agent[\"']\s*,\s*[\"']edit[\"']"),
         "make use PRESET=example", "kmx models credential copilot", True),
    Rule("retired-command", "a retired agent operation or installation step",
         r"\bkmx" + KMX_OPTIONS + r"\s+(?:up" + KMX_OPTIONS +
          r"\s+--step(?:=|\s+)(?:agent|tools-agent)\b|govern\b|agent" +
          KMX_OPTIONS + r"\s+edit\b)",
         "kmx up --step tools-agent", "kmx agent create", True),
    Rule("restored-operation-identifier", "a restored Kagent operational subsystem identifier",
         r"\b(?:Kagent|kagent)(?:Chat|List|Show|Lift|Status|Evaluate|Console|Quickstart|"
         r"Install|Upgrade|Update|Delete|Discover|Invoke|Lifecycle|Gateway|Approval|Grant|Resume|Session)\b",
         "type KagentGateway struct {}", "type KagentCreateReceipt struct {}", True),
    Rule("broad-operation", "a positive claim of broader Kagent operation",
         (r"(?i:(?:\b" + NAME + r"\b" + BROAD_GAP_100 +
          r"\b(?:supports?|provides?|implements?|enables?|offers?|manages?|installs?|upgrades?)\b" +
          BROAD_GAP_100 + r"\b" + BROAD_OPERATIONS + r"\b)|"
          r"(?:\b" + BROAD_OPERATIONS + r"\b" + BROAD_GAP_100 +
          r"\b(?:is|are|works?|supported|available|enabled|implemented|provided|offered)\b" +
          BROAD_GAP_100 + r"\b(?:for|on|with|by)\s+" + NAME + r"\b)|"
          r"(?:\b" + NAME + r"\b" + BROAD_GAP_50 + r"\b" + BROAD_OPERATIONS +
          r"\b" + BROAD_GAP_30 +
          r"\b(?:works?|is available|are available|is supported|are supported|is enabled|are enabled|is implemented|are implemented)\b)|"
          r"(?:(?<!not )(?<!never )(?<!cannot )(?<!can't )\b(?:use|run|try|open|start)\b" +
          BROAD_GAP_40 + r"\b" + NAME + r"\b" + BROAD_GAP_40 +
          r"\b" + BROAD_OPERATIONS + r"\b))"),
         NAME.capitalize() + " supports chat and status",
         NAME.capitalize() + " chat and status remain unsupported", True),
    # A boundary on the LEFT only, and both halves of that are deliberate.
    #
    # A trailing `\b` missed `NAME_usage_metadata` in a migration comment:
    # an underscore is a word character, so there was no boundary after the
    # name, and the rule read a line naming the retired runtime's own
    # telemetry field as clean. Dropping the boundary entirely goes too far
    # the other way — it fires on `pickAgent`, an Orka chat helper that
    # merely ENDS in the letters. A word ending in the name is not the name;
    # a word starting with it is.
    #
    # The second alternative is the CamelCase spelling, case-SENSITIVE and
    # scoped so the insensitive flag above cannot leak onto it. Without it
    # the historical teardown sentinel `PayloadKagent` and the tests named
    # after it would be invisible here — present in the tree, accounted for
    # nowhere. They are allowed, but they are allowed BY NAME.
    Rule("bare-name", "Kagent named outside scoped create support",
         r"(?i:\b" + NAME + r")|" + NAME.capitalize(),
         "the " + NAME + "_usage_metadata field",
         "return b.pickAgent(ctx)  // orka-system", False),
]


class PathRule:
    """A retired asset whose path alone is enough to refuse it."""

    def __init__(self, name, what, regexp, example, counterexample):
        self.name = name
        self.what = what
        self.re = re.compile(regexp)
        self.example = example
        self.counterexample = counterexample


PATH_RULES = [
    PathRule(
        "retired-kagent-asset-path",
        "a retired Kagent installer, demo, CLI, shim, or broad runtime asset",
        (r"^(?:k8s/(?:" + NAME + r"-values|hello-world|tools-agent)\.yaml|"
         r"k8s/models/.*|internal/kmx/" + NAME + r"(?:cli|compat)/.*|"
         r"internal/kmx/app/(?:" + NAME + r"_namespace|no" + NAME +
         r"_test|quickstart_" + NAME + r"_test|runtime_" + NAME + r"(?:_test)?)\.go|"
         r"spikes/" + NAME + r"-shim/.*|\.github/workflows/" + NAME + r"-shim-spike\.yml)$"),
        "k8s/" + NAME + "-values.yaml",
        "internal/kmx/app/create_" + NAME + ".go",
    ),
    PathRule(
        "retired-gateway-approval-path",
        "a retired custom gateway or approval execution source asset",
        (r"^(?:plane/internal/gateway/.*|internal/kmx/app/approvals\.go|"
         r"plane/internal/(?:proxy/admin_approvals|store/approvals)\.go|"
         r"scripts/(?:ap-await-approval|await-approval)\.sh|scripts/test_ap_approval\.py)$"),
        "plane/internal/gateway/gateway.go",
        "plane/internal/db/migrations/00003_approvals.sql",
    ),
]


def expected_support() -> dict[str, set[str]]:
    """The code-owned scoped support policy, normalized for comparison."""
    return {category: set(paths) for category, paths in SCOPED_SUPPORT_FILES.items()}


def scoped_rule_allowed(path: str, rule: Rule, line: str, support) -> bool:
    """Whether this support path earns this vocabulary waiver on this line."""
    if path not in support or rule.name not in SCOPED_SUPPORT_RULES:
        return False
    if rule.name == "create-selector":
        return bool(CREATE_SELECTOR_CONTEXT.search(line))
    return True


EXEMPTION_RULES = {
    "negative": NEGATIVE_RULES,
    "retirement": frozenset({
        "bare-name", "create-selector", "fixed-demo-asset", "retired-payload", "retired-preset-command",
    }),
    "historical": frozenset({"api-kind", "bare-name"}),
    "future": frozenset({"bare-name"}),
    "retired-notice": frozenset({"bare-name", "create-selector", "retired-command"}),
    "fixture": FIXTURE_RULES,
}


def exemption_allows(entry, rule: Rule) -> bool:
    """Whether this exact-line category may waive this specific rule."""
    return rule.name in EXEMPTION_RULES[entry["category"]]


def multiline_view(lines: list[str]) -> str:
    """Normalize adjacent physical lines without joining separate blocks."""
    parts = []
    for line in lines:
        part = line.strip()
        # A shell continuation is syntax for the newline, not a token between
        # `--step` and its value (or an installer and its chart name).
        if part.endswith("\\"):
            part = part[:-1].rstrip()
        parts.append(part)
    return " ".join(parts)


def multiline_rule_matches(rule: Rule, lines: list[str]) -> bool:
    """Whether a rule needs both endpoint lines of this compound window.

    Endpoint necessity gives every compound one minimal window. It prevents a
    single-line finding from being rediscovered with each neighboring line and
    keeps an unrelated exact exemption from being credited by overlap.
    """
    if len(lines) < 2 or any(not line.strip() for line in lines):
        return False
    if not rule.re.search(multiline_view(lines)):
        return False
    return (not rule.re.search(multiline_view(lines[:-1]))
            and not rule.re.search(multiline_view(lines[1:])))


def multiline_lines_are_connected(path: str, rule: Rule, lines: list[str]) -> bool:
    """Refuse wrapped compounds without joining unrelated adjacent entries."""
    stripped = [line.strip() for line in lines]
    joined = multiline_view(lines)
    if rule.name == "broad-operation":
        if path.endswith(".md"):
            return not any(line.startswith("|") for line in stripped)
        return all(line.startswith(("//", "#", "*")) for line in stripped)
    if any(line.endswith("\\") for line in stripped[:-1]):
        return True
    if stripped[0].startswith(("kmx ", "helm ")):
        return True
    if "Options{" in joined:
        return True
    if "cobra.Command{" in joined and "Flags().StringVar" in joined:
        return True
    if "[]string{" in joined and "}" in joined:
        return True
    return False


def load_allowlist(path=ALLOWLIST):
    """The named exemptions, or a refusal.

    An allowlist that failed to load is not an empty allowlist: the scan
    would go red on every line it names and somebody would delete the gate
    rather than the residue. An allowlist that IS empty is equally a
    failure — this tree has a historical record, and a policy claiming
    otherwise has stopped describing anything.
    """
    try:
        doc = json.loads(path.read_text())
    except (OSError, ValueError) as e:
        sys.exit(f"check-legacy-runtime: cannot read the allowlist {path}: {e}")
    files = doc.get("historical_files") or []
    lines = doc.get("lines") or []
    support = doc.get("scoped_support") or {}
    if not isinstance(support, dict):
        sys.exit(f"check-legacy-runtime: {path} scoped_support must be an object")
    support_files = support.get("files") or {}
    support_rules = support.get("rules") or []
    if not isinstance(support_files, dict) or not isinstance(support_rules, list):
        sys.exit(f"check-legacy-runtime: {path} scoped_support needs files by category and a rule list")
    normalized = {}
    for category, paths in support_files.items():
        if category not in SCOPED_SUPPORT_FILES:
            sys.exit(f"check-legacy-runtime: unknown scoped support category {category!r}; "
                     f"allowed: {', '.join(SCOPED_SUPPORT_FILES)}")
        if not isinstance(paths, list) or not paths or any(not isinstance(p, str) or not p for p in paths):
            sys.exit(f"check-legacy-runtime: scoped support category {category!r} must name paths")
        if len(paths) != len(set(paths)):
            sys.exit(f"check-legacy-runtime: scoped support category {category!r} repeats a path")
        for support_path in paths:
            wrong = support_category_problem(category, support_path)
            if wrong:
                sys.exit(f"check-legacy-runtime: {support_path} may not be scoped as "
                         f"{category!r}: {wrong}")
        normalized[category] = set(paths)
    if normalized != expected_support():
        sys.exit("check-legacy-runtime: scoped support paths differ from the closed code-owned set; "
                 "adding, removing, or recategorizing a path requires changing both policy halves")
    if len(support_rules) != len(set(support_rules)) or set(support_rules) != set(SCOPED_SUPPORT_RULES):
        sys.exit("check-legacy-runtime: scoped support rules differ from the closed code-owned set; "
                 "only the pinned vocabulary classes may be waived")
    if not files and not lines:
        sys.exit(f"check-legacy-runtime: {path} exempts nothing at all — "
                 "refusing to report a clean tree against a policy that describes no tree.")
    for entry in lines:
        for field in ("path", "text", "category", "why"):
            if not str(entry.get(field, "")).strip():
                sys.exit(f"check-legacy-runtime: an allowlist entry is missing {field}: {entry}")
        if entry["category"] not in CATEGORIES:
            sys.exit(f"check-legacy-runtime: unknown category {entry['category']!r} "
                     f"for {entry['path']} — allowed: {', '.join(CATEGORIES)}")
        wrong = category_problem(entry)
        if wrong:
            sys.exit(f"check-legacy-runtime: {entry['path']} may not be exempted as "
                     f"{entry['category']!r}: {wrong}\n      {entry['text'].strip()!r}")
    return files, lines, frozenset().union(*normalized.values())


def historical_covers(path: str, files: list[str]) -> bool:
    """True when this path IS the historical record.

    A directory entry has to end in a slash, so `docs/reviews/` covers the
    dated reviews and `docs/` would have to be written as such to cover the
    current documentation — which the floor below then refuses by name.
    """
    for entry in files:
        if entry.endswith("/"):
            if path.startswith(entry):
                return True
        elif path == entry:
            return True
    return False


def candidates(root=ROOT) -> list[pathlib.Path]:
    """Where the real repository might be, nearest first.

    Usually it is the directory this script sits in. It is NOT when
    scripts/check-mutations.py is running: that harness executes a copy of
    this file from a throwaway directory whose entries are symlinks to the
    tree, and git will not work there. The tree under judgement is the real
    one on the other end of those links, so it is tried too — or the
    enumeration comes back empty, the floors (correctly) call the scan
    broken, and every mutation then fails for that reason instead of for the
    one it was written to prove.
    """
    out = [root]
    for probe in ("docs", "internal", "cmd", "plane"):
        p = root / probe
        if p.exists():
            real = p.resolve().parent
            if real not in out:
                out.append(real)
    return out


def repo_root(root=ROOT) -> pathlib.Path:
    """The directory the tracked paths below are relative to."""
    for candidate in candidates(root):
        got = subprocess.run(["git", "rev-parse", "--show-toplevel"],
                             cwd=candidate, capture_output=True)
        if got.returncode == 0:
            return candidate
    return root


def tracked(root=None) -> list[str]:
    """Every tracked file, from git rather than from a walk: the claim is
    about what this repository PUBLISHES, and a walk would also read a
    developer's untracked scratch.

    A git that refuses is a refusal here too. An enumeration that failed is
    not an empty tree, and reading it as one is the exact fail-open shape
    this whole file exists to prevent.
    """
    for candidate in ([root] if root else candidates()):
        got = subprocess.run(["git", "ls-files", "-z"], cwd=candidate, capture_output=True)
        if got.returncode == 0 and got.stdout:
            return sorted(p for p in got.stdout.decode().split("\0") if p)
    sys.exit("check-legacy-runtime: cannot list the tracked files "
             f"(tried {[str(c) for c in ([root] if root else candidates())]}) — "
             "refusing to read a failed enumeration as an empty tree.")


def judge(paths, historical, lines, support=None, root=None):
    """(findings, files read, allowlist entries that matched).

    A finding is (path, line number, rule, the line). The matched-entry set
    comes back because an exemption nothing matches is stale, and a stale
    exemption is how a rule stops applying to a file nobody is looking at.
    """
    exact = {}
    support = support or frozenset()
    root = root or repo_root()
    for entry in lines:
        exact.setdefault(entry["path"], {}).setdefault(entry["text"].strip(), []).append(entry)

    findings, read, used = [], 0, set()
    for path in paths:
        if path in SELF or pathlib.Path(path).suffix.lower() in SKIP_SUFFIX:
            continue
        for rule in PATH_RULES:
            if rule.re.search(path):
                findings.append((path, 0, rule, path))
        p = root / path
        try:
            text = p.read_text()
        except UnicodeDecodeError:
            if pathlib.Path(path).suffix.lower() == ".svg":
                findings.append((path, 0, None, "text SVG is not UTF-8 (not scanned)"))
            continue  # other binary files have nothing textual to claim
        except OSError as e:
            findings.append((path, 0, None, f"could not be read (not scanned): {e}"))
            continue
        read += 1
        record = historical_covers(path, historical)
        current = path.startswith(CURRENT_SURFACES)
        physical_lines = text.splitlines()
        for n, line in enumerate(physical_lines, 1):
            stripped = line.strip()
            for rule in RULES:
                if not rule.re.search(line):
                    continue
                if not rule.everywhere and not current:
                    continue
                # The historical record may say what used to be true.
                if record:
                    continue
                hit = exact.get(path, {}).get(stripped)
                if hit:
                    allowed = [entry for entry in hit if exemption_allows(entry, rule)]
                    if allowed:
                        for entry in allowed:
                            used.add(id(entry))
                        continue
                # A scoped support path waives vocabulary only. Retired
                # commands, pins, assets and broad operation remain findings.
                if scoped_rule_allowed(path, rule, line, support):
                    continue
                findings.append((path, n, rule, stripped))
        if record:
            continue
        # Compound rules are deliberately separate from the physical-line
        # pass above. Exact exemptions remain exact physical lines and are not
        # looked up or credited here; endpoint necessity prevents the same
        # one-line match from reappearing in overlapping windows.
        compound_rules = [rule for rule in RULES if rule.name in MULTILINE_RULES]
        for start in range(len(physical_lines)):
            for width in range(2, MULTILINE_WINDOW_LINES + 1):
                window = physical_lines[start:start + width]
                if len(window) != width or any(not line.strip() for line in window):
                    break
                for rule in compound_rules:
                    if not rule.everywhere and not current:
                        continue
                    if multiline_lines_are_connected(path, rule, window) and multiline_rule_matches(rule, window):
                        findings.append((path, start + 1, rule, multiline_view(window)))
    return findings, read, used


def report(findings, lines, used, paths, read, support_count=0, direct_support=0):
    """The verdict, and every way of reaching it that is not a verdict."""
    problems = []

    # An exemption that matches nothing outlives the line it was written
    # for, and the next person to add residue to that file inherits it.
    for entry in lines:
        if id(entry) not in used:
            problems.append(f"{entry['path']}: the allowlist still exempts a line that is no longer "
                            f"there (or no longer matches a rule):\n      {entry['text'].strip()!r}\n"
                            f"      Remove the entry: an exemption nobody needs is one nobody reviews.")

    for path, n, rule, line in findings:
        what = rule.what if rule else line
        where = f"{path}:{n}" if n else path
        problems.append(f"{where}: {what}\n      {line[:160]}")

    if problems:
        print("check-legacy-runtime: Kagent escaped the explicit v0.10.2 create boundary", file=sys.stderr)
        for p in problems:
            print("  " + p, file=sys.stderr)
        print(f"\n{len(problems)} place(s). Remove the surface, add a legitimate create-support "
              "path to both closed policy sets, or — for an exact refusal, absence assertion, "
              "or explicitly unsupported future — name the line in "
              f"{ALLOWLIST.relative_to(ROOT)} with a category and a reason.", file=sys.stderr)
        return 1
    print(f"check-legacy-runtime: {len(paths)} tracked file(s), {read} read, "
          f"{len(RULES)} content rule(s), {len(PATH_RULES)} path rule(s), "
          f"{support_count} scoped create-support file(s) ({direct_support} read directly while untracked), "
          f"{len(lines)} exact exemption(s) — "
          "only explicit Kagent v0.10.2 create support remains")
    return 0


def floors(paths, read, historical, support=None) -> list[str]:
    """The ways a clean report can mean nothing at all."""
    bad = []
    if not paths:
        bad.append("no tracked files were enumerated — a clean tree cannot be read off an empty list")
    elif len(paths) < MIN_TRACKED:
        bad.append(f"only {len(paths)} tracked file(s) enumerated, fewer than the {MIN_TRACKED} "
                   "this tree is known to have — the enumeration shrank, not the residue")
    if paths and not read:
        bad.append(f"{len(paths)} file(s) enumerated and none read — enumerated is not examined")
    elif read and read < MIN_READ:
        bad.append(f"only {read} file(s) were read, fewer than the {MIN_READ} floor — "
                   "something is skipping the tree")
    if set(SELF) != {"scripts/check-legacy-runtime.py",
                     "scripts/legacy-runtime-allowlist.json"}:
        bad.append(f"the self-exemption set is no longer this checker and its allowlist: {sorted(SELF)}")
    if support is not None and set(support) != frozenset().union(*SCOPED_SUPPORT_FILES.values()):
        bad.append("the loaded scoped support paths no longer equal the closed production/test/bundle-fixture/current-doc/workflow set")
    if SCOPED_SUPPORT_RULES != frozenset({
            "api-group", "api-kind", "schema-version", "image", "create-selector", "bare-name"}):
        bad.append(f"the scoped support rule set widened or shrank: {sorted(SCOPED_SUPPORT_RULES)}")
    if MULTILINE_WINDOW_LINES != 4:
        bad.append(f"the compound scan window is {MULTILINE_WINDOW_LINES} lines, not the reviewed bound of 4")
    if MULTILINE_RULES != frozenset({
            "asset", "installer", "moving-version", "unsupported-runtime-command",
            "unsupported-runtime-options", "retired-up-step", "retired-payload",
            "retired-preset-command", "retired-command", "broad-operation"}):
        bad.append(f"the compound rule set widened or shrank: {sorted(MULTILINE_RULES)}")
    if MULTILINE_RULES & SCOPED_SUPPORT_RULES:
        bad.append("a compound rule is waivable as scoped vocabulary")
    if NEGATIVE_RULES != frozenset({
            "api-group", "api-kind", "asset", "bare-name", "create-selector",
            "fixed-demo-asset", "installer", "pin", "retired-command", "retired-payload",
            "retired-preset-command", "retired-up-step", "schema-version",
            "unsupported-runtime-command", "unsupported-runtime-options", "wrong-api-version"}):
        bad.append(f"the negative-control rule set widened or shrank: {sorted(NEGATIVE_RULES)}")
    if set(EXEMPTION_RULES) != set(CATEGORIES):
        bad.append("the exact-exemption categories do not have one closed rule set each")
    rule_names = {rule.name for rule in RULES}
    if not SCOPED_SUPPORT_RULES <= rule_names:
        bad.append("a scoped support rule no longer names a live content rule")
    if not MULTILINE_RULES <= rule_names:
        bad.append("a compound rule no longer names a live content rule")
    # Only the changelog and dated reviews may be declared historical;
    # a current document cannot exempt itself as a whole file.
    for entry in historical:
        if entry not in HISTORICAL_FILES:
            bad.append(f"{entry!r} is declared historical as a WHOLE FILE. Only "
                       f"{', '.join(HISTORICAL_FILES)} are the record itself; anything else "
                       "belongs in the current tree.")
    return bad


def main(argv, allowlist=None):
    if argv[:1] == ["--selftest"]:
        return selftest()
    historical, lines, support = load_allowlist(allowlist or ALLOWLIST)
    paths = tracked()
    findings, read, used = judge(paths, historical, lines, support)
    # Publishing policy is still based on git's tracked enumeration. A closed
    # support path may be under development before its first commit, however;
    # scan only those named paths directly so exact negative fixtures are not
    # stale and non-waivable residue cannot hide until commit.
    direct = sorted(set(support) - set(paths))
    direct_findings, direct_read, direct_used = judge(direct, historical, lines, support)
    findings.extend(direct_findings)
    used.update(direct_used)
    bad = floors(paths, read, historical, support)
    if bad:
        print("check-legacy-runtime: this scan proves nothing", file=sys.stderr)
        for b in bad:
            print("  " + b, file=sys.stderr)
        return 1
    return report(findings, lines, used, paths, read + direct_read, len(support), len(direct))


# --------------------------------------------------------------------------
# The self-test breaks the OTHER side: it plants residue in a fixture tree
# and requires each rule to catch it, and it removes the ground under each
# floor and requires the floor to notice. scripts/check-mutations.py breaks
# THIS file; between the two, a rule is proven from both directions.
# --------------------------------------------------------------------------

def selftest():
    failed = 0
    historical, lines, support = load_allowlist()

    def case(ok, good, bad_msg):
        nonlocal failed
        if ok:
            print("ok   " + good)
        else:
            print("FAIL " + bad_msg)
            failed += 1

    with tempfile.TemporaryDirectory() as tmp:
        d = pathlib.Path(tmp)

        # Every rule catches its own example, in a real file, read through
        # the same judge() the tree gets — not as a regex in isolation.
        for rule in RULES:
            where = "docs/planted.md" if not rule.everywhere else ".github/workflows/planted.yml"
            f = d / where
            f.parent.mkdir(parents=True, exist_ok=True)
            f.write_text(f"ordinary prose\n{rule.example}\nmore prose\n")
            got = {r.name for _, _, r, _ in judge([where], [], [], root=d)[0] if r}
            case(rule.name in got, f"{rule.name}: its example is caught in a scanned file",
                 f"{rule.name}: its own example was not caught (found: {sorted(got) or 'nothing'})")

            f.write_text(f"{rule.counterexample}\n")
            near = {r.name for _, _, r, _ in judge([where], [], [], root=d)[0] if r}
            case(rule.name not in near, f"{rule.name}: does not fire on {rule.counterexample!r}",
                 f"{rule.name}: also fires on {rule.counterexample!r}, so it pins nothing")
            f.unlink()

        # Path rules have the same example/counterexample contract as content
        # rules. They are evaluated without creating the retired path, because
        # the path itself is the finding.
        for rule in PATH_RULES:
            case(bool(rule.re.search(rule.example)),
                 f"{rule.name}: its retired path example is caught",
                 f"{rule.name}: its own path example was not caught")
            case(not rule.re.search(rule.counterexample),
                 f"{rule.name}: does not fire on {rule.counterexample!r}",
                 f"{rule.name}: also fires on {rule.counterexample!r}, so it pins nothing")
            f = d / rule.example
            f.parent.mkdir(parents=True, exist_ok=True)
            f.write_text("ordinary fixture\n")
            got = {r.name for _, _, r, _ in judge([rule.example], [], [], root=d)[0] if r}
            case(rule.name in got,
                 f"{rule.name}: its retired path is caught by the real judge",
                 f"{rule.name}: judge skipped its retired path (found: {sorted(got) or 'nothing'})")
            f.unlink()

        # The central boundary: the exact create spelling and vocabulary are
        # allowed only in a pinned support file. The same line in another
        # current file is refused, while non-create commands stay refused even
        # if planted inside an approved file.
        approved = "cmd/kmx/agent_commands.go"
        unapproved = "cmd/kmx/new_kagent_surface.go"
        (d / "cmd" / "kmx").mkdir(parents=True, exist_ok=True)
        create_line = f"Run kmx agent create demo --runtime {NAME} against {NAME}.dev/v1alpha2 ModelConfig\n"
        (d / approved).write_text(create_line)
        approved_findings = judge([approved], [], [], {approved}, root=d)[0]
        case(not approved_findings,
             "explicit create vocabulary is allowed in an approved support file",
             f"approved create support was refused: {[(r.name if r else None) for _, _, r, _ in approved_findings]}")
        (d / unapproved).write_text(create_line)
        unapproved_hits = {r.name for _, _, r, _ in judge([unapproved], [], [], {approved}, root=d)[0] if r}
        case({"create-selector", "bare-name"} <= unapproved_hits,
             "the same create spelling is refused in an unapproved current file",
             f"an unauthorized support path passed (found: {sorted(unapproved_hits) or 'nothing'})")

        (d / approved).write_text(f"pass --runtime {NAME} here\n")
        context_free = {r.name for _, _, r, _ in judge([approved], [], [], {approved}, root=d)[0] if r}
        case("create-selector" in context_free,
             "a context-free runtime selector is refused even in a support file",
             f"an approved path globally allowed the selector (found: {sorted(context_free) or 'nothing'})")

        for command, wanted in (
                (f"kmx agent chat --runtime {NAME} demo", "unsupported-runtime-command"),
                (f"kmx up --step {NAME}", "retired-up-step"),
                (f"kmx aks up --payload {NAME}", "retired-payload")):
            (d / approved).write_text(command + "\n")
            hits = {r.name for _, _, r, _ in judge([approved], [], [], {approved}, root=d)[0] if r}
            case(wanted in hits, f"{command!r} remains refused inside an approved support file",
                 f"{command!r} borrowed the create support waiver (found: {sorted(hits) or 'nothing'})")

        (d / approved).write_text(f'ChatOptions{{Runtime: "{NAME}"}}\n')
        option_hits = {r.name for _, _, r, _ in judge([approved], [], [], {approved}, root=d)[0] if r}
        case("unsupported-runtime-options" in option_hits,
             "a non-create options struct cannot select Kagent in a support file",
             f"non-create options borrowed the create waiver (found: {sorted(option_hits) or 'nothing'})")

        for claim in (f"{NAME.capitalize()} supports chat and status",
                      f"chat and status are available for {NAME}",
                      f"{NAME.capitalize()} chat works",
                      f"Use {NAME.capitalize()} chat"):
            (d / approved).write_text(claim + "\n")
            hits = {r.name for _, _, r, _ in judge([approved], [], [], {approved}, root=d)[0] if r}
            case("broad-operation" in hits,
                 f"broad operational claim {claim!r} is refused in a support file",
                 f"broad support claim passed (found: {sorted(hits) or 'nothing'})")

        # Non-waivable compounds cannot escape by wrapping source or prose.
        # Four lines is the reviewed maximum, and a blank line is a hard
        # boundary so separate paragraphs are never assembled into a claim.
        split_cases = (
            (f"// {NAME.capitalize()} supports\n// chat and status\n", "broad-operation",
             "a split positive broad-support claim"),
            (f'ChatOptions{{\nRuntime: "{NAME}",\n}}\n', "unsupported-runtime-options",
             "a split unsupported options struct"),
            ('cmd := &cobra.Command{\nUse: "chat [name]",\n}\n'
             f'cmd.Flags().StringVar(&opt.Runtime, "runtime", "{NAME}", "runtime")\n',
             "unsupported-runtime-command", "a split Cobra command and runtime default"),
            (f'return []string{{"agent",\n"chat",\n"--runtime",\n"{NAME}"}}\n',
             "unsupported-runtime-command", "a split unsupported source argv"),
            (f"helm install\n{NAME} oci://ghcr.io/{NAME}-dev/{NAME}/helm/{NAME}\n", "installer",
             "a split retired installer"),
            (f"kmx up --step\n{NAME}\n", "retired-up-step", "a split retired up operation"),
        )
        for content, wanted, what in split_cases:
            (d / approved).write_text(content)
            found = judge([approved], [], [], {approved}, root=d)[0]
            hits = [r.name for _, _, r, _ in found if r]
            case(hits.count(wanted) == 1,
                 f"{what} is refused exactly once in a bounded multiline window",
                 f"{what} passed or produced duplicate findings (found: {hits or 'nothing'})")

        truthful = f"{NAME.capitalize()} does not support\nchat or status\n"
        (d / approved).write_text(truthful)
        truthful_hits = {r.name for _, _, r, _ in judge(
            [approved], [], [], {approved}, root=d)[0] if r}
        case("broad-operation" not in truthful_hits,
             "a truthful multiline refusal is not mistaken for broad support",
             f"a truthful multiline refusal was rejected (found: {sorted(truthful_hits)})")

        separated = f"{NAME.capitalize()} supports\n\nchat and status\n"
        (d / approved).write_text(separated)
        separated_hits = {r.name for _, _, r, _ in judge(
            [approved], [], [], {approved}, root=d)[0] if r}
        case("broad-operation" not in separated_hits,
             "the compound scan does not join separate paragraphs",
             f"a blank-line boundary was ignored (found: {sorted(separated_hits)})")

        # Source that composes argv rather than writing a shell command is
        # still the create selector and is still path-scoped.
        argv_line = f'return []string{{"agent", "create", "demo", "--runtime", "{NAME}"}}\n'
        (d / approved).write_text(argv_line)
        case(not judge([approved], [], [], {approved}, root=d)[0],
             "source composing the create command is allowed in an approved file",
             "approved source composition was refused")
        (d / unapproved).write_text(argv_line)
        case(judge([unapproved], [], [], {approved}, root=d)[0],
             "source composing the create command is refused in an unapproved file",
             "unapproved source composition passed")

        # An approved path cannot resurrect the old assets or moving pin.
        for residue, wanted in ((f"{NAME.upper()}_VERSION = latest", "pin"),
                                (f"load {NAME}-values.yaml", "asset"),
                                (f"support {NAME} latest", "moving-version"),
                                (f"ghcr.io/{NAME}-dev/{NAME}/controller:0.10.3", "moving-version"),
                                (f"ghcr.io/{NAME}-dev/{NAME}/controller:1.0.0", "moving-version"),
                                (f"{NAME.capitalize()} supports deletion.", "broad-operation"),
                                (f"func {NAME.capitalize()}Delete() {{}}", "restored-operation-identifier"),
                                (f"apiVersion: {NAME}.dev/v1alpha1", "wrong-api-version"),
                                ("apply k8s/hello-world.yaml", "fixed-demo-asset")):
            (d / approved).write_text(residue + "\n")
            hits = {r.name for _, _, r, _ in judge([approved], [], [], {approved}, root=d)[0] if r}
            case(wanted in hits, f"non-waivable {wanted} residue is refused in a support file",
                 f"support scope waived the {wanted} rule")

        # The workflow is a vocabulary-scoped support path, not an installer
        # bypass. Exactly the four reviewed official chart commands can be
        # fixtures, and only at their exact CI path and exact bytes.
        workflow = CI_WORKFLOW
        workflow_support = {workflow}
        case(FIXTURE_LINES == frozenset({CRDS_PULL_LINE, CHART_PULL_LINE, CRDS_FIXTURE_LINE, CHART_FIXTURE_LINE}),
             "the external fixture set is exactly the four reviewed chart commands",
             "the external fixture set widened or lost a reviewed chart command")
        case(FIXTURE_RULES == frozenset({"installer", "asset"}),
             "fixture lines waive only installer and retired-asset findings",
             f"fixture lines waive the wrong rule classes: {sorted(FIXTURE_RULES)}")
        fixture_entries = [
            {"path": workflow, "text": line, "category": "fixture",
             "why": "external exact-version test precondition"}
            for line in sorted(FIXTURE_LINES)
        ]
        (d / workflow).write_text("\n".join(sorted(FIXTURE_LINES)) + "\n")
        fixture_findings, _, fixture_used = judge(
            [workflow], [], fixture_entries, workflow_support, root=d)
        case(not fixture_findings and len(fixture_used) == len(fixture_entries),
             "the four exact external chart commands are allowed only as CI fixtures",
             f"the closed CI fixtures were refused: {[(r.name if r else None) for _, _, r, _ in fixture_findings]}")

        arbitrary_installer = f"helm install other oci://ghcr.io/{NAME}-dev/{NAME}/helm/{NAME} --version 0.10.2"
        (d / workflow).write_text(arbitrary_installer + "\n")
        arbitrary_hits = {r.name for _, _, r, _ in judge(
            [workflow], [], [], workflow_support, root=d)[0] if r}
        case("installer" in arbitrary_hits,
             "an arbitrary installer line remains refused in the approved workflow",
             f"workflow scope waived an arbitrary installer (found: {sorted(arbitrary_hits) or 'nothing'})")

        manifest_installer = (f"kubectl apply -f https://github.com/{NAME}-dev/{NAME}/"
                              "releases/download/v0.10.2/install.yaml")
        (d / approved).write_text(manifest_installer + "\n")
        manifest_hits = {r.name for _, _, r, _ in judge(
            [approved], [], [], {approved}, root=d)[0] if r}
        case("installer" in manifest_hits,
             "a kubectl manifest installer remains refused in an approved support file",
             f"support scope waived a kubectl manifest installer (found: {sorted(manifest_hits) or 'nothing'})")

        unsupported = f"kmx agent chat --runtime {NAME} demo"
        (d / workflow).write_text(unsupported + "\n")
        unsupported_hits = {r.name for _, _, r, _ in judge(
            [workflow], [], [], workflow_support, root=d)[0] if r}
        case("unsupported-runtime-command" in unsupported_hits,
             "unsupported operations remain refused in the approved workflow",
             f"workflow scope waived a non-create operation (found: {sorted(unsupported_hits) or 'nothing'})")

        for text, wanted in ((f"support {NAME} 0.10.3", "moving-version"),
                             (f"{NAME.capitalize()} supports deletion.", "broad-operation"),
                             (f"type {NAME.capitalize()}Delete struct {{}}", "restored-operation-identifier")):
            entry = {"path": workflow, "text": text, "category": "negative",
                     "why": "planted exact negative-control bypass"}
            (d / workflow).write_text(text + "\n")
            hits = {r.name for _, _, r, _ in judge(
                [workflow], [], [entry], workflow_support, root=d)[0] if r}
            case(wanted in hits,
                 f"a negative exact line cannot waive {wanted}",
                 f"negative exact line waived {wanted} (found: {sorted(hits) or 'nothing'})")

        for path in ("docs/fixture.md", "internal/kmx/app/fixture.go"):
            target = d / path
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_text(CRDS_FIXTURE_LINE + "\n")
            borrowed = {r.name for _, _, r, _ in judge(
                [path], [], fixture_entries, workflow_support, root=d)[0] if r}
            case("installer" in borrowed,
                 f"the fixture line cannot exempt {path}",
                 f"the CI fixture exemption leaked to {path} (found: {sorted(borrowed) or 'nothing'})")

        extended_line = CRDS_FIXTURE_LINE + " --set substrate.enabled=false"
        (d / workflow).write_text(extended_line + "\n")
        extended_hits = {r.name for _, _, r, _ in judge(
            [workflow], [], fixture_entries, workflow_support, root=d)[0] if r}
        case("installer" in extended_hits,
             "editing an exact fixture line makes the exemption stale and the installer refused",
             f"a widened fixture line inherited approval (found: {sorted(extended_hits) or 'nothing'})")

        # Retired commands without the runtime's name still advertise the
        # removed operation if offered as an instruction. Test the actual
        # scan, including a flag before the command, and leave live commands.
        retired = "docs/retired-command.md"
        (d / "docs").mkdir(exist_ok=True)
        for command in ("kmx up --step agent", "kmx up --step tools-agent",
                        "kmx up --context kind-test --step agent", "kmx agent --context kind-test edit",
                        "kmx govern", "kmx agent edit", "kmx --context kind-test govern",
                        "kmx -context=kind-test govern"):
            (d / retired).write_text(f"Run `{command}`\n")
            hits = {r.name for _, _, r, _ in judge([retired], [], [], root=d)[0] if r}
            case("retired-command" in hits, f"retired command {command!r} is refused",
                 f"retired command {command!r} passed (found: {sorted(hits) or 'nothing'})")
        for command in ("kmx up --step orka", "kmx agent create", "kmx models add"):
            (d / retired).write_text(f"Run `{command}`\n")
            hits = {r.name for _, _, r, _ in judge([retired], [], [], root=d)[0] if r}
            case("retired-command" not in hits, f"supported command {command!r} remains available",
                 f"supported command {command!r} was mistaken for a retired one")
        (d / retired).unlink()

        # A context flag does not turn the retired preset switch back into a
        # supported command. The original rule caught only adjacent kmx use.
        for command in ("kmx --context kind-test use orka", "kmx -context=kind-test use orka"):
            (d / retired).write_text(f"Run `{command}`\n")
            hits = {r.name for _, _, r, _ in judge([retired], [], [], root=d)[0] if r}
            case("retired-preset-command" in hits, f"context-qualified use {command!r} is refused",
                 f"context-qualified use {command!r} passed the gate")
        (d / retired).unlink()

        (d / retired).write_text('args := []string{"agent", "edit", "demo"}\n')
        argv_retired = {r.name for _, _, r, _ in judge([retired], [], [], root=d)[0] if r}
        case("retired-preset-command" in argv_retired,
             "source composing a retired command is refused",
             f"retired argv composition passed (found: {sorted(argv_retired) or 'nothing'})")
        (d / retired).unlink()

        # SVGs are text; a bundled diagram can carry a runnable old API name
        # as readily as a Markdown guide. Raster images remain skipped.
        diagram = "docs/retired-command.svg"
        (d / diagram).write_text(f"<svg><text>agents.{NAME}.dev/v1alpha2</text></svg>\n")
        found, read_count, _ = judge([diagram], [], [], root=d)
        case(read_count == 1 and found,
             "a text SVG is scanned and its retired API name refused",
             "a text SVG with a retired API name was skipped")
        (d / diagram).write_bytes(b"\xff<svg/>")
        found, read_count, _ = judge([diagram], [], [], root=d)
        case(read_count == 0 and found,
             "an invalid UTF-8 SVG fails closed instead of being silently skipped",
             "an invalid UTF-8 SVG was treated as an unscannable binary")
        (d / diagram).unlink()

        # A word that merely ENDS in the name is not the name. This is
        # `pickAgent`, an Orka helper a boundary-free pattern refused.
        (d / "internal" / "kmx" / "app").mkdir(parents=True, exist_ok=True)
        ending = "internal/kmx/app/chat.go"
        (d / ending).write_text("func (b *orkaChatBackend) pickAgent(ctx context.Context) {\n")
        case(not judge([ending], [], [], root=d)[0],
             "a word ending in the name is not refused",
             "an unrelated identifier ending in the letters was refused as the runtime")
        # ...while a word STARTING with it is, underscore or not.
        (d / ending).write_text(f"    -- ({NAME}_usage_metadata), on outcome rows\n")
        case(judge([ending], [], [], root=d)[0],
             "a word starting with the name is refused, underscore or not",
             "the name followed by an underscore was read as clean")

        # The bare name is refused on a current surface AND in a workflow:
        # a workflow that creates the runtime's namespace is installing it,
        # whatever the hard identifiers say.
        (d / "docs").mkdir(exist_ok=True)
        (d / ".github" / "workflows").mkdir(parents=True, exist_ok=True)
        (d / "docs" / "guide.md").write_text(f"install the {NAME} runtime\n")
        (d / ".github" / "workflows" / "ci.yml").write_text(f"          kubectl create namespace {NAME}\n")
        doc_hits = {r.name for _, _, r, _ in judge(["docs/guide.md"], [], [], root=d)[0] if r}
        wf_hits = {r.name for _, _, r, _ in judge([".github/workflows/ci.yml"], [], [], root=d)[0] if r}
        case("bare-name" in doc_hits, "the bare name on a current surface is refused",
             "the bare name in a document was not refused")
        case("bare-name" in wf_hits, "a workflow creating the runtime's namespace is refused",
             "a workflow line creating the retired runtime's namespace passed "
             f"(found: {sorted(wf_hits) or 'nothing'})")
        # ...and a HARD identifier in that same workflow is refused by its
        # own rule rather than by the surface, which is what keeps "hard
        # identifiers are refused everywhere" a rule and not a sentence.
        (d / ".github" / "workflows" / "ci.yml").write_text(f"          helm install oci://ghcr.io/{NAME}-dev/x\n")
        wf_hard = {r.name for _, _, r, _ in judge([".github/workflows/ci.yml"], [], [], root=d)[0] if r}
        case("image" in wf_hard, "a hard identifier in a workflow is still refused",
             f"a workflow naming a published legacy image passed (found: {sorted(wf_hard) or 'nothing'})")

        # An exemption is an EXACT line. A substring or prefix match would
        # let the next edit to that line inherit the exemption.
        planted = "docs/exact.md"
        (d / "docs" / "exact.md").write_text(
            f"the {NAME} runtime is gone\nthe {NAME} runtime is gone, so install it from here\n")
        entry = {"path": planted, "text": f"the {NAME} runtime is gone",
                 "category": "future", "why": "fixture: not supported"}
        found, _, used = judge([planted], [], [entry], root=d)
        case(len(found) == 1 and found[0][1] == 2,
             "an exemption matches one exact line and not the line that extends it",
             f"an exact exemption leaked onto a longer line (caught {len(found)})")
        case(id(entry) in used, "a matched exemption is recorded as used",
             "a matched exemption was not recorded, so staleness cannot be judged")

        # A stale exemption is a failure, not a pass.
        (d / "docs" / "exact.md").write_text("nothing here at all\n")
        found, _, used = judge([planted], [], [entry], root=d)
        case(report(found, [entry], used, [planted], 1) == 1,
             "an exemption that matches nothing is reported as stale",
             "a stale exemption was accepted, so an exemption can outlive its line")

        # The historical record may say what used to be true; a current
        # document under the same rules may not. The planted line carries a
        # HARD identifier, so this tests the exemption rather than the
        # surface rule that would not reach a root file anyway.
        (d / "CHANGELOG.md").write_text(f"- the {NAME}.dev Agents and ModelConfigs went with the installer\n")
        found, _, _ = judge(["CHANGELOG.md"], ["CHANGELOG.md"], [], root=d)
        case(not found, "the historical record may carry past-tense evidence",
             f"the historical record was refused ({[f[2].name for f in found if f[2]]}), "
             "which would make the policy unusable")
        found, _, _ = judge(["CHANGELOG.md"], [], [], root=d)
        case(found, "the historical record is only exempt because it is NAMED",
             "an unnamed file was treated as history")

        # ...and a directory exemption needs its slash to cover a
        # directory, so a file entry cannot spread onto every path it
        # happens to prefix.
        case(historical_covers("docs/reviews/2026-09-09-x.md", ["docs/reviews/"])
             and not historical_covers("docs/reviews-plan.md", ["docs/reviews/"]),
             "a directory exemption covers its directory and nothing beside it",
             "a directory exemption leaked onto a sibling path")
        case(historical_covers("CHANGELOG.md", ["CHANGELOG.md"])
             and not historical_covers("CHANGELOG.md.bak", ["CHANGELOG.md"])
             and not historical_covers("docs/README.md", ["docs/R"]),
             "a file exemption covers that file and nothing it is a prefix of",
             "a file exemption spread onto every path beginning with its name")

        # Unreadable is not clean: a permissions problem must be reported,
        # never allowed to quietly shrink the scanned set.
        locked = d / "docs" / "locked.md"
        locked.write_text("nothing to see\n")
        locked.chmod(0o000)
        try:
            found, read_count, _ = judge(["docs/locked.md"], [], [], root=d)
            if os.geteuid() == 0:
                print("ok   (skipped) running as root, where nothing is unreadable")
            else:
                case(found and read_count == 0, "an unreadable file is reported, not skipped",
                     "an unreadable file was skipped in silence")
        finally:
            locked.chmod(0o600)

        # The self-exemption is this checker and its allowlist. A third
        # name there is a file that may carry anything.
        global SELF
        original = SELF
        try:
            SELF = original | {"docs/kmx.md"}
            case(floors(["a"] * MIN_TRACKED, MIN_READ, historical, support),
                 "a grown self-exemption set is refused",
                 "a third file could exempt itself from the checker that names it")
        finally:
            SELF = original

        # The floors. Each is the ground under a clean report.
        case(floors([], 0, historical, support), "an empty enumeration is refused",
              "an empty enumeration was called clean")
        case(floors(["a"] * MIN_TRACKED, 0, historical, support), "files enumerated and none read is refused",
              "a scan that read nothing was called clean")
        case(floors(["a"] * (MIN_TRACKED - 1), MIN_READ, historical, support),
              "a shrunken enumeration is refused",
              "an enumeration below the floor was called clean")
        case(floors(["a"] * MIN_TRACKED, MIN_READ - 1, historical, support),
              "a shrunken read count is refused",
              "a read count below the floor was called clean")
        case(not floors(["a"] * MIN_TRACKED, MIN_READ, historical, support),
              "a full enumeration over a sound allowlist clears the floors",
              "the floors refuse a sound scan, so they cannot distinguish anything")
        for widened in ("docs/", "internal/", "Makefile",
                         "docs/kmx.md", "docs/README.md", "internal/kmx/app/up.go"):
            case(floors(["a"] * MIN_TRACKED, MIN_READ, [widened], support),
                  f"declaring {widened!r} historical is refused",
                  f"{widened!r} could be declared historical, exempting current work wholesale")
        case(not floors(["a"] * MIN_TRACKED, MIN_READ, list(HISTORICAL_FILES), support),
              "the record itself may be declared historical",
              "the changelog and the dated reviews were refused as history")

        grown_support = set(support) | {"internal/kmx/app/extra_kagent.go"}
        case(floors(["a"] * MIN_TRACKED, MIN_READ, historical, grown_support),
             "a widened support path set is refused",
             "an unauthorized support path could be added without notice")

        global SCOPED_SUPPORT_RULES
        original_support_rules = SCOPED_SUPPORT_RULES
        try:
            SCOPED_SUPPORT_RULES = original_support_rules | {"asset"}
            case(floors(["a"] * MIN_TRACKED, MIN_READ, historical, support),
                 "a widened support rule set is refused",
                 "the support waiver could grow to cover retired assets")
        finally:
            SCOPED_SUPPORT_RULES = original_support_rules

        # An allowlist that exempts nothing, or names a category nobody
        # reviewed, or claims a category its path cannot support, is refused
        # rather than silently applied. The doc-instruction case is the one
        # that matters most: "install the runtime" labelled `negative` is
        # the single edit that turns this list back into a way of not
        # fixing things.
        policy = json.loads(ALLOWLIST.read_text())["scoped_support"]

        def allowlist_with(entry):
            return {"historical_files": ["CHANGELOG.md"], "scoped_support": policy,
                    "lines": [entry]}

        for doc, why in (
                ({"historical_files": [], "scoped_support": policy, "lines": []},
                 "an allowlist that exempts nothing"),
                (allowlist_with({"path": "internal/kmx/app/x_test.go", "text": "y",
                                 "category": "because", "why": "z"}),
                 "an unknown exemption category"),
                (allowlist_with({"path": "internal/kmx/app/x_test.go", "text": "y",
                                 "category": "negative", "why": ""}),
                 "an exemption with no reason"),
                (allowlist_with({"path": "docs/getting-started.md",
                                 "text": f"install the {NAME} runtime",
                                 "category": "negative",
                                 "why": "planted: an instruction calling itself an assertion"}),
                 "an active documentation instruction labelled `negative`"),
                (allowlist_with({"path": "internal/kmx/app/up.go", "text": "y",
                                 "category": "retirement", "why": "planted"}),
                 "a `retirement` exemption outside the files that do the refusing"),
                (allowlist_with({"path": "docs/kmx.md", "text": "y",
                                 "category": "historical", "why": "planted"}),
                 "a `historical` line in a document that is not the board"),
                (allowlist_with({"path": "docs/kmx.md", "text": "y",
                                 "category": "future", "why": "planted, and silent about support"}),
                 "a `future` note that never says the future is unsupported"),
                (allowlist_with({"path": "Makefile", "text": "y",
                                 "category": "future", "why": "planted: not supported"}),
                 "a `future` note outside current documentation"),
                (allowlist_with({"path": "docs/getting-started.md", "text": CRDS_FIXTURE_LINE,
                                 "category": "fixture", "why": "planted"}),
                 "a fixture exemption outside the exact CI workflow"),
                (allowlist_with({"path": "internal/kmx/app/create.go", "text": CRDS_FIXTURE_LINE,
                                 "category": "fixture", "why": "planted"}),
                 "a product-code fixture exemption outside the exact CI workflow"),
                (allowlist_with({"path": CI_WORKFLOW, "text": arbitrary_installer,
                                 "category": "fixture", "why": "planted"}),
                 "an arbitrary installer line labelled as a fixture")):
            f = d / "allowlist.json"
            f.write_text(json.dumps(doc))
            try:
                load_allowlist(f)
                case(False, "", f"{why} was accepted")
            except SystemExit:
                case(True, f"{why} is refused", "")

        # Scoped support is not a free-form JSON allowlist. Both adding a
        # path and adding a rule are rejected before any scan can use them.
        case(support_category_problem("workflow", CI_WORKFLOW) is None,
             "the workflow category accepts the one closed CI path",
             "the reviewed CI workflow was refused by its own category")
        case(support_category_problem("workflow", ".github/workflows/release.yml") is not None,
             "the workflow category directly refuses a second workflow path",
             "the workflow category itself widened beyond the reviewed CI path")
        case(support_category_problem("bundle-fixture", "internal/kmx/app/testdata/bundle-format/kagent/agent.yaml") is None,
             "the pinned portable fixture earns scoped vocabulary",
             "the pinned portable fixture was refused by its own category")
        case(support_category_problem("bundle-fixture", "internal/kmx/app/testdata/bundle-format/other/agent.yaml") is not None,
             "the bundle-fixture category refuses another YAML file",
             "the bundle-fixture category widened beyond the pinned files")
        for mutate, what in (
                (lambda p: p["files"]["production"].append("internal/kmx/app/extra_kagent.go"),
                 "an unauthorized support path"),
                (lambda p: p["rules"].append("asset"), "a widened support rule set"),
                (lambda p: p["files"]["production"].append("docs/production.md"),
                 "a support path borrowing the production category"),
                (lambda p: p["files"]["workflow"].append(".github/workflows/release.yml"),
                 "a second workflow support path"),
                (lambda p: p["files"].update({"misc": ["docs/anything.md"]}),
                 "an unknown support category")):
            policy_doc = json.loads(ALLOWLIST.read_text())
            mutate(policy_doc["scoped_support"])
            f = d / "allowlist.json"
            f.write_text(json.dumps(policy_doc))
            try:
                load_allowlist(f)
                case(False, "", f"{what} was accepted")
            except SystemExit:
                case(True, f"{what} is refused by the closed policy", "")

        # Honest notices of a retired command are current documentation, not
        # support instructions. They earn a separately bounded category:
        # current docs only, and the exact line must explicitly retire it.
        for entry, allowed, what in (
                ({"path": "docs/kmx.md",
                  "text": "`kmx agent edit` is retired; its hidden stub points to kubectl.",
                  "category": "retired-notice", "why": "retired operation"}, True,
                 "a current-doc notice explicitly retiring a command"),
                ({"path": "docs/kmx.md", "text": "Run `kmx agent edit`.",
                  "category": "retired-notice", "why": "this used to be removed"}, False,
                 "a live command instruction disguised as a notice"),
                ({"path": "docs/kmx.md", "text": "No prerequisite is needed. Run `kmx govern foo`.",
                  "category": "retired-notice", "why": "this used to be removed"}, False,
                 "an unrelated negative word hiding an active command"),
                ({"path": "docs/kmx.md", "text": "There is no `kmx agent edit`. Run `kmx govern foo`.",
                  "category": "retired-notice", "why": "a current-doc notice"}, False,
                 "a notice concealing a second retired command on the same line"),
                ({"path": "docs/kmx.md", "text": "There is no `kmx agent edit`; run it with --namespace app.",
                  "category": "retired-notice", "why": "a current-doc notice"}, False,
                 "a new instruction hidden behind an approved notice prefix"),
                ({"path": "Makefile", "text": "There is no `kmx agent edit`.",
                  "category": "retired-notice", "why": "removed operation"}, False,
                 "a notice category on an executable surface")):
            f = d / "allowlist.json"
            f.write_text(json.dumps(allowlist_with(entry)))
            try:
                load_allowlist(f)
                case(allowed, f"{what} is classified correctly", f"{what} was accepted without its boundary")
            except SystemExit:
                case(not allowed, f"{what} is refused", f"{what} was refused despite its boundary")

        # ...and each category still loads where it IS earned, or the rule
        # above would be a way of refusing the policy itself.
        for entry, what in (
                ({"path": "internal/kmx/app/x_test.go", "text": "y", "category": "negative",
                  "why": "a bounded assertion"}, "a `negative` line in a test"),
                ({"path": ".github/workflows/ci.yml", "text": f'"kmx agent chat --runtime {NAME} demo",', "category": "negative",
                  "why": "a tripwire"}, "a `negative` line in the CI tripwires"),
                ({"path": "internal/kmx/lift/plan.go", "text": "y", "category": "retirement",
                  "why": "the payload refusal"}, "a `retirement` line in a refusing file"),
                ({"path": "docs/orka.md", "text": "remains OPEN", "category": "future",
                  "why": "an open question"}, "a `future` note that says it is open"),
                ({"path": CI_WORKFLOW, "text": CRDS_FIXTURE_LINE, "category": "fixture",
                  "why": "external exact-version test precondition"}, "an exact CI fixture line")):
            f = d / "allowlist.json"
            f.write_text(json.dumps(allowlist_with(entry)))
            try:
                load_allowlist(f)
                case(True, f"{what} is accepted", "")
            except SystemExit:
                case(False, "", f"{what} was refused, so the policy cannot describe this tree")

        # And the verdict itself, through main() rather than through
        # judge(): a finding that is computed correctly and then not acted
        # on is indistinguishable from a clean tree.
        real = report([("docs/x.md", 3, RULES[0], "bad")], [], set(), ["docs/x.md"], 1)
        case(real == 1, "a finding exits non-zero", "a finding was reported clean")
        case(report([], [], set(), ["docs/x.md"], 1) == 0,
             "a clean file exits zero", "a clean file was refused")

        # The floors reach an exit code, not just a list. A verdict that is
        # computed and then not acted on is the same as no verdict, and the
        # widest exemption there is — a current document declared historical
        # as a whole file — is checked through main() for that reason.
        #
        # The fixture is the REAL allowlist with one entry added, and the
        # file it widens is deliberately one NO line entry names: a
        # directory full of exempted lines would go red for staleness
        # instead, and this case would pass with the floors switched off.
        # Built from a fresh read rather than from `historical`/`lines`
        # above, because an entry list that has already been through judge()
        # carries the identities staleness is tracked by.
        real = json.loads(ALLOWLIST.read_text())
        widened_doc = "docs/getting-started.md"
        case(all(entry["path"] != widened_doc for entry in real["lines"]),
             "the widened fixture document has no line exemptions",
             "the widened fixture document now has line exemptions that mask a broken floor")
        real["historical_files"] = list(real["historical_files"]) + [widened_doc]
        widened = d / "widened.json"
        widened.write_text(json.dumps(real))
        case(main([], allowlist=widened) == 1,
             "a run whose allowlist declares a current document historical exits non-zero",
              "a widened exemption reached an exit code of zero, so the floors decide nothing")

    # The normal gate scans tracked files by design. Directly scan every
    # existing scoped support file as well, including currently untracked
    # implementation files, so a support path cannot wait until commit to be
    # judged. Exact-line staleness remains the normal full-tree scan's job.
    support_findings, support_read, _ = judge(sorted(support), historical, lines, support)
    case(support_read == len(support),
         "every scoped support file, including untracked files, is read directly",
         f"only {support_read} of {len(support)} scoped support files were read")
    case(not support_findings,
         "existing scoped support files contain only waivable create vocabulary",
         f"a support file carries a non-waivable surface: {[(p, n, r.name if r else None) for p, n, r, _ in support_findings]}")

    # Finally, the real tree, through the real entry point. This is what
    # makes a widened exemption or a neutered rule visible even when every
    # fixture above still passes.
    rc = main([])
    case(rc == 0, "the tree this checker ships in passes its own scan",
         "the tree does not pass its own scan (output above)")

    if failed:
        print(f"check-legacy-runtime self-test: {failed} case(s) failed", file=sys.stderr)
        return 1
    print(f"check-legacy-runtime self-test: {len(RULES)} rules and every floor proved on a real scan")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
