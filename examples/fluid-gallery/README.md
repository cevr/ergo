# fluid-gallery

The docs gallery of `ergo.dev/fluid` (`modules/fluid`; its NOTICE says
where the design comes from). Each page is a component's docs page, laid
out as the reference docs lay it out: the title, the installation row, a section
per specimen with its stage, and the API reference.

- `gallery/` is the site: `Route` and its route map `Docs` (`/docs/button`,
  `/docs/badge`), the stage chrome (`stage.ergo`), the docs page (`doc.ergo`),
  the specimens (`button.ergo`, `badge.ergo`) and the glyphs (`glyph.ergo`).
  Every box is drawn with a `look.Look`; the looks resolve to atomic classes
  once, when the program starts (`kit`, `parts`, `chipParts`, `docKit`), and
  the page's head holds the one sheet built from all of them
  (`web.Style`). Resolution is pure, so a page whose looks are constant
  folds to one constant at build time.
- `cmd/fluid-gallery` prints the answer to `GET` of each path it is given;
  its cases are the pages.
- `cmd/fluid-gallery-web FONT-FOLDER [ADDR]` serves the site and the fonts
  folder under `/fonts/` (the pages name `/fonts/InterVariable.ttf`, Inter
  4.0's variable font; no font is in the repo).

The pages' pixels are held to the live docs by
`fluid-ui/pixel.ts <port> --only button,badge` (0 differing pixels at 390 and
1440 pixels wide, light and dark, with the forced hover, focus and press
rows). A stage's pixels depend on the page around it: the browser rasterizes
in tiles anchored to the document, so a stage must sit at the reference's
offset, and the API reference's fade mask changes how the disabled buttons
above it are painted. That is why the pages draw the reference's whole docs
layout, not only the stages.

## Licences

```
The glyphs plus, arrow-right, search, copy, chevron-left and chevron-right in
gallery/glyph.ergo are path data of
Lucide (https://lucide.dev), under the licences below.

ISC License

Copyright (c) 2026 Lucide Icons and Contributors

Permission to use, copy, modify, and/or distribute this software for any
purpose with or without fee is hereby granted, provided that the above
copyright notice and this permission notice appear in all copies.

THE SOFTWARE IS PROVIDED "AS IS" AND THE AUTHOR DISCLAIMS ALL WARRANTIES
WITH REGARD TO THIS SOFTWARE INCLUDING ALL IMPLIED WARRANTIES OF
MERCHANTABILITY AND FITNESS. IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR
ANY SPECIAL, DIRECT, INDIRECT, OR CONSEQUENTIAL DAMAGES OR ANY DAMAGES
WHATSOEVER RESULTING FROM LOSS OF USE, DATA OR PROFITS, WHETHER IN AN
ACTION OF CONTRACT, NEGLIGENCE OR OTHER TORTIOUS ACTION, ARISING OUT OF
OR IN CONNECTION WITH THE USE OR PERFORMANCE OF THIS SOFTWARE.

---

The Lucide icons plus, arrow-right, search, copy, chevron-left and
chevron-right are derived from the Feather project:

The MIT License (MIT) (for the icons listed above)

Copyright (c) 2013-present Cole Bemis

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```
