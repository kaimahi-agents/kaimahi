# Documentation site

The [documentation website](https://kaimahi-agents.github.io/kaimahi/) renders
this repository's maintained Markdown. It does not introduce a second content
tree or change product behavior.

## Preview locally

Node.js 22.12 or newer and npm are required for the preview tooling. From the
repository root, run one command:

```bash
make docs-preview
```

The target installs the exact locked development dependencies, then serves the
site only on `127.0.0.1`. Follow the URL printed by VitePress. Stop it with
<kbd>Ctrl</kbd>+<kbd>C</kbd>.

To build and test the generated site without starting a server:

```bash
make docs-build
```

Generated HTML, Vite caches, and `node_modules` are ignored. They are local or CI
output, not source.

## Content and navigation

The sources remain where contributors already review them:

- the root [README](../README.md) remains the repository and site front door;
- the maintained guides remain under `docs/`;
- the normative [AgentSuite specification](../agentsuite/spec.md) remains under
  `agentsuite/`;
- source-code, fixture, and checkout-script links lead to the matching GitHub
  source rather than pretending those files are website pages.

Navigation lives in `.vitepress/config.mts`. It groups existing pages by user
task, but does not copy their instructions. The homepage path chooser links into
those same guides. Local search indexes the rendered Markdown and uses no hosted
search service or credentials.

The AgentSuite specification is expected to move to a standalone repository in
[#491](https://github.com/kaimahi-agents/kaimahi/issues/491). The site therefore
builds in CI both with and without the local `agentsuite/` page. Once the new
documentation URL exists, set `KMX_AGENTSUITE_DOCS_URL` for the no-local build;
existing specification links and navigation then point there without requiring a
second local copy.

## Version boundary

The published site and local preview render `main`, which can describe
unreleased behavior. The global channel notice links to the stable v0.7.0 source.
Tagged Markdown remains the historical release record. A version selector and
generated copies of older guides are deliberately not implemented in this draft.

## Framework and publication boundary

The private npm package and lockfile live under `docs/`, keeping the Go module
root free of Node package metadata and scoping dependency automation to the
preview tooling. The draft pins VitePress `2.0.0-alpha.20` exactly. It provides
responsive navigation, accessible local search, syntax highlighting, dark mode,
and a small Vue enhancement while leaving Markdown authoritative. The alpha pin
and its transitive lockfile must be reassessed before a stable framework claim.

`.github/workflows/docs-pages.yml` publishes from `main` to organization GitHub
Pages after relevant documentation changes, and supports an explicit manual run.
Pull requests remain build-only under the ordinary CI workflow. The deployment
uses GitHub's run token and OIDC with `pages: write` and `id-token: write` scoped
only to its deployment job; it adds no repository secret, analytics, or remote
search. The available `kaimahi.dev` domain is a candidate for a separate custom-
domain decision, not part of this first hosting step.

## Review and removal

Review the preview at desktop and mobile widths, with keyboard-only navigation,
dark and light appearance, and reduced motion. The path chooser uses native links
and an ARIA tablist; the site retains VitePress's skip link and search controls.
This is useful accessibility behavior, not a claim of full WCAG conformance.

The site remains removable. Disable GitHub Pages, delete
`.github/workflows/docs-pages.yml`, `docs/package.json`,
`docs/package-lock.json`, `docs/.vitepress/`, and this guide. Remove the two Make
targets, CI setup/build steps, and generated-output ignore rules. Remove preview
instructions or links from the root README, `docs/README.md`, `docs/development.md`,
`CONTRIBUTING.md`, `AGENTS.md`, `CHANGELOG.md`, and `docs/repository-map.md`, then
rerun the link, README-front-door, and repository-map checks. Every other
maintained Markdown document remains in its original location and requires no
migration.
