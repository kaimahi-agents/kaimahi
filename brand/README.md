# Kaimahi brand assets

Kaimahi's visual identity has two strands: the original night worker, a quiet
helper that keeps systems healthy while people sleep, and the compact ketu mark
used at the top of the repository README.

## Product line

> Agent Builder CLI for Kubernetes.

## Files

| File | Format and canvas | Use |
|---|---|---|
| `ketu.svg` | SVG viewBox `0 0 1473 1371`; rounded tile | Repository README icon |
| `mascot.png` | 1536×1536 RGBA; transparent | Canonical character reference; do not redraw from memory |
| `mark.svg` / `mark.png` | SVG viewBox `0 0 512 512`; 1024×1024 RGBA transparent PNG | Organization avatar, favicon, and small-size identity |
| `wordmark.svg` | SVG viewBox `0 0 760 192`; transparent | Horizontal name lockup |
| `hero.png` | 1600×600 RGB; opaque | Organization profile hero; not embedded in the repository README |
| `social-preview.png` | 1280×640 RGB; opaque | GitHub repository social preview |
| `../docs/assets/architecture.svg` | Scalable SVG; opaque navy canvas | Governance architecture; editable source is beside it |

## Ketu mark

The compact mark is a stylized **ketu**, using the te reo Māori name recorded by
[Te Aka Māori Dictionary](https://maoridictionary.co.nz/word/2587) for a pointed,
paddle-shaped implement used to loosen soil for cultivation. The working-tool
metaphor fits Kaimahi: prepare the ground so useful work can begin.

The mark is not a waka paddle (*hoe*), a traditional whakairo pattern, or a claim
that this project represents Māori culture. The cultural read recorded in
[`docs/NAMING.md`](../docs/NAMING.md) covered use of the project name; it did not
constitute a separate cultural review of this later icon.

## Palette

| Color | Hex |
|---|---|
| Deep navy | `#071827` |
| Teal | `#24D6D9` |
| Blue | `#60A5FA` |
| Amber | `#FFB547` |
| Off-white | `#F7F4E8` |

## Usage

- Keep the mark's clear space at least equal to the lantern's diameter.
- Display `ketu.svg` at 64 px or larger so the silhouette and highlights remain
  legible.
- Display the compact mark at 40 px or larger; use it instead of the full
  mascot below 96 px.
- Place transparent artwork on quiet backgrounds where its navy edges and pale
  details retain contrast. Use the dark-blue wordmark only on light backgrounds.
- Keep the hero, social preview, and architecture diagram on their supplied
  dark canvases; the diagram is designed to read in GitHub light and dark modes.
- Do not recolor individual character details or add holiday/cultural motifs.
- Do not place text over the worker or the guarded pathways.
- Technical diagrams may use the palette without including the mascot.

## Provenance

The ketu SVG was supplied by a project maintainer and adopted as the compact
repository mark. The mascot and scene illustrations were generated with OpenAI
ImageGen under human art direction. `mascot.png` is the canonical character
reference used for subsequent edits. The compact mark, wordmark, and architecture
diagram are vector-authored assets. Generation prompts and material edits are
recorded in the pull request that introduced these files.
