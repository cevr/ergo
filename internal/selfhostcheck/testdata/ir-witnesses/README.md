# IR agreement witnesses

Add one `<group>.json` file per group, alongside `sourced-calls.json`. Each
row requires one body to reach `TestCompilerIRAgreesWithBuilder` and match
the Go builder byte for byte:

- `fixture`: the source file, relative to `../bind/`.
- `declaration`: the exact prefix of a unique declaration line, excluding
  indentation. Include enough of the signature to disambiguate the line.
- `symbol`: the split-normalized Go IR name, including receiver or copy
  suffix when applicable. Use one row per required generated symbol.

An emitted body at a different source declaration, or an emission the
Go builder cannot compare, does not satisfy the witness.
