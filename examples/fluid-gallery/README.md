# fluid-gallery

The docs gallery of `ergo.dev/fluid` (`modules/fluid`), the Ergo port of
Fluid Functionalism (https://www.fluidfunctionalism.com, MIT) through its
Foldkit port (https://fff.timhanlon.com). Each page is a component's docs
page, laid out as the original's: the title, the installation row, a section
per specimen with its stage, and the API reference.

- `gallery/` is the site: `Route` and its route map `Docs` (`/docs/button`,
  `/docs/badge`), the stage chrome (`stage.ergo`), the docs page (`doc.ergo`),
  the specimens (`button.ergo`, `badge.ergo`) and the glyphs (`glyph.ergo`).
  Every box is drawn with a `look.Look`; the looks resolve to atomic classes
  once, when the program starts (`kit`, `parts`, `chipParts`, `docKit`), and
  the page's head holds the one sheet built from all of them
  (`web.Style`). The counted guard `ResolvedOncePerLook` (`tests.ergo`)
  holds that a render resolves no look.
- `cmd/fluid-gallery` prints the answer to `GET` of each path it is given;
  its cases are the pages.
- `cmd/fluid-gallery-web FONT-FOLDER [ADDR]` serves the site and the fonts
  folder under `/fonts/` (the pages name `/fonts/InterVariable.ttf`, Inter
  4.0's variable font; no font is in the repo).

The pages' pixels are held to the live docs by
`fluid-ui/pixel.ts <port> --only button,badge` (0 differing pixels at 390 and
1440 pixels wide, light and dark, with the forced hover, focus and press
rows). A stage's pixels depend on the page around it: the browser rasterizes
in tiles anchored to the document, so a stage must sit at the original's
offset, and the API reference's fade mask changes how the disabled buttons
above it are painted. That is why the pages draw the original's whole docs
layout, not only the stages.

`licenses/lucide.txt` is the licence of the Lucide glyphs in
`gallery/glyph.ergo`.
