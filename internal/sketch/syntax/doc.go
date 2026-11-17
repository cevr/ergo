// Package syntax is the AST of Ergo and its Go door to the parser: the
// decided grammar of #196 (docs/spec/grammar.md is normative). Since
// capstone S2d the scanner and parser are compiler/syntax's, in Ergo,
// linked into the binary (internal/linked/compiler); Parse decodes their
// rows into this AST (decode.go). Since S2e no Go parser is left: the
// parse laws (roundtrip_test.go) and the decoder property
// (decode_view_test.go) hold Parse to the formatter and to the view. The
// parser accepts that grammar and nothing else; an old Go-shaped spelling
// is a diagnostic, and a Go habit's message names the Ergo form
// (habits.go).
//
// A decided form that means the same as a Go-shaped node becomes that node
// (`let` a VarDecl, `match` a SwitchStmt or TypeSwitchStmt, `fails` an
// ErrorRow), so the consumers keep reading one tree; nodes no decided form
// produces stay declared until their consumers are rewritten, and the
// parser never builds them (TestDecidedOnly).
//
// The package resolves no names; the checker (internal/sketch/check), the LSP (#38) and the
// formatter (compiler/format, through internal/hostsyntax) consume its AST. Every syntax error is
// S001 (docs/spec/diagnostics.md), or S108 for a type that mixes fields and
// operations, with a byte span, line and byte column; Parse reports the
// first error and returns no file.
//
// Prior art, where the package chooses between options:
//   - go/ast, go/parser: types are expressions, every node has Pos and End,
//     comments are kept as CommentGroups beside the tree (File.Comments) with
//     Doc groups on declarations; the composite-literal rule in control
//     clause headers; parameter grouping; nesting is bounded.
//   - cmd/compile/internal/syntax: one package for tokens, scanner and parser;
//     positions are byte offsets resolved through a line table.
//   - ReScript's parser maps a new surface onto the tree the old front end
//     produced, so the checker stays unchanged; rustc names other
//     languages' habits ("Rust has no postfix increment operator").
//   - The printer lives apart (compiler/format), as go/printer does,
//     so no checker or lowering code depends on layout.
package syntax
