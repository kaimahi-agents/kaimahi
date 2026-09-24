# Kaimahi brand assets

Kaimahi's night worker is an original workshop sprite: a quiet, capable helper
that keeps systems healthy while people sleep. The visual story is independent
of the project's te reo Māori name and does not use Māori motifs.

## Product line

> Agent Builder CLI for Kubernetes.

## Files

| File | Format and canvas | Use |
|---|---|---|
| `mascot.png` | 1536×1536 RGBA; transparent | Canonical character reference; do not redraw from memory |
| `mark.svg` / `mark.png` | SVG viewBox `0 0 512 512`; 1024×1024 RGBA transparent PNG | Organization avatar, favicon, and small-size identity |
| `wordmark.svg` | SVG viewBox `0 0 760 192`; transparent | Horizontal name lockup |
| `hero.png` | 1600×600 RGB; opaque | Organization profile hero; not embedded in the repository README |
| `social-preview.png` | 1280×640 RGB; opaque | GitHub repository social preview |
| `../docs/assets/architecture.svg` | Scalable SVG; opaque navy canvas | Governance architecture; editable source is beside it |

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

The mascot and scene illustrations were generated with OpenAI ImageGen under
human art direction. `mascot.png` is the canonical character reference used for
subsequent edits. The compact mark, wordmark, and architecture diagram are
vector-authored assets. Generation prompts and material edits are recorded in
the pull request that introduced these files.
