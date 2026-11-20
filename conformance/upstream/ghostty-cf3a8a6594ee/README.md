# Ghostty at cf3a8a6594ee

Provenance of what `examples/vt` takes from Ghostty (https://github.com/ghostty-org/ghostty, MIT). Nothing of Ghostty's is vendored here; the example names no source (examples/README.md, "Ports").

- `examples/vt/reference/testdata/parse_table.txt` is Ghostty's VT transition table, transcribed line by line from `src/terminal/parse_table.zig` (`genTable`) at commit `cf3a8a6594ee`. The file keeps Ghostty's MIT notice, which covers the transcription. The law `reference.StepMatchesTable` holds the parser's step to it.
- The parser's step is vt100.net's DEC parser with Ghostty's three changes: a colon is a parameter separator in CSI, DEL prints in the ground state, and high bytes are data inside DCS and OSC strings. UTF-8 is decoded before the parser, as Ghostty's stream does. The collector's limits are Ghostty's: 24 parameters (`MAX_PARAMS`), 4 intermediates (`MAX_INTERMEDIATE`), a u16 parameter; a CSI other than SGR with a colon is dropped, as in Ghostty.
- The dense screen is Ghostty's page form: 8-byte cells with no pointer, a ring of pages, a counted style set (`RefCountedSet`) and its integrity check (`screen.StylesAreCounted`). The history is bounded in bytes and dropped a page at a time, as Ghostty frees its oldest page.
- The events goldens agree with a Go transliteration of Ghostty's `Parser.zig` and `parse_table.zig`, the parser of the hand-Go control `cmd/ergo/testdata/vt/control` and of `examples/vt/testdata/floor`.
- Where the example differs: one style set per screen, not one per page; a parameter of more than 255 digits is kept, clamped, where Ghostty's 8-bit digit counter wraps and drops it; OSC data is handed over raw, where Ghostty parses every OSC command in the parser.
- `examples/vt/width` takes its ranges from Markus Kuhn's `mk_wcwidth` (2007).
