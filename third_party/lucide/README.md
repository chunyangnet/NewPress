# Local UI icons

The Chunnel theme and embedded core UI use a pinned subset of
`lucide-static@1.75.0`, fetched from the npm registry on 2026-11-19.
`icons.json` records the package integrity, upstream names and exact SVG
geometry. `LICENSE` contains both the Lucide ISC and inherited Feather MIT
notices. These notices are also included in each UI's THIRD_PARTY_NOTICES.md
so a standalone theme package retains its attribution.

Run `npm run build:frontend` to generate both sprites. Run
`npm run check:frontend` to verify them. Neither command needs to download
icons. Do not edit the generated template partials.

Existing symbol IDs stay stable for saved navigation. Historical names such
as `globe-2`, `code-2`, `filter` and `check-circle` map to their upstream
equivalents. `message-circle` uses the message-circle-more variant, preserving
the UI's familiar conversation dots. `heart-fill` and `star-fill` reuse the
upstream outline with a solid fill. Other shapes are unmodified.

The editor settings, focus and overflow controls use `panel-right`, `focus`
and `ellipsis` from the same pinned 1.75.0 package; their geometry is included
in the local catalog.

Article reference cards rely on their title and hover state without an extra
arrow, including in the editor preview. The component image viewer uses
Lucide's expand geometry in its CSS background.
