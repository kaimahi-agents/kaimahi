# Documentation site draft

The documentation website is a reviewable preview layer over this repository's
maintained Markdown. It does not introduce a second content tree, change product
behavior, or publish a production site.

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

## Version boundary

The preview renders the checked-out `main` documentation, which can describe
unreleased behavior. The global channel notice links to the stable v0.6.0 source.
Tagged Markdown remains the historical release record. A version selector and
generated copies of older guides are deliberately not implemented in this draft.

## Framework and publication boundary

The draft pins VitePress `2.0.0-alpha.20` exactly. It provides responsive
navigation, accessible local search, syntax highlighting, dark mode, and a small
Vue enhancement while leaving Markdown authoritative. The alpha pin and its
transitive lockfile must be reassessed before production publication; CI builds
and tests it but publishes nothing.

No GitHub Pages workflow, domain, analytics, remote search, publication token, or
repository secret is introduced. A later hosting decision should be reviewed
separately, with pull requests remaining build-only and deployment restricted to
an approved branch or tag.

## Review and removal

Review the preview at desktop and mobile widths, with keyboard-only navigation,
dark and light appearance, and reduced motion. The path chooser uses native links
and an ARIA tablist; the site retains VitePress's skip link and search controls.
This is useful accessibility behavior, not a claim of full WCAG conformance.

The draft is intentionally removable. Delete `package.json`, `package-lock.json`,
and `docs/.vitepress/`, then remove the two Make targets and the CI build step.
Every maintained Markdown document remains in its original location and requires
no migration.
