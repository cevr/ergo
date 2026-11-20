# work's Go plugin

A level-1 language plugin of `work` (SEMWS M1): it answers `UnitsOf` with
the units of a Go file, cut by `go/parser`, and every other request with
`NotProvided`. The protocol is `modules/work/work/plugin.ergo`'s: one JSON
request a line on standard input, one answer a line on standard output.

```toml
[language.go]
command = "go run ./plugins/go"
files = ["*.go"]
```

A unit is the package clause, the imports (members keyed by path), or one
top-level declaration keyed by the package and its name: `p.F`, a method
`p.T.M` (the receiver's type, pointer and type parameters dropped), a type,
var or const by its first name. A struct's fields, an interface's methods,
a group's specs and a table's elements (a var's composite literal) are its
members. The comment above a declaration and the one after it on its last
line are in its unit. Admit it with `work plugin-test go`; its own tests
(`go test ./plugins/go`) hold the Splitter laws on generated files and on
every Go file of this repository, and its mutants are
`testdata/mutants.json`.

Go merges themselves go through Mergiraf, a candidate driver line of
`work.toml` (`modules/work/README.md`, `merge-file`): this plugin cuts
units for change sets, never merges a Go file.
