// Package syntax is the lexer, AST and parser of the 2026 edition: the full
// grammar of docs/spec/ v0 (docs/spec/grammar.md is normative). Where
// grammar.md is silent, or marks a production "prototype", the parser takes
// Go's production for that form, without goto, fallthrough and const.
//
// The package resolves no names; the checker, the LSP and the
// formatter (internal/sketch/format) consume its AST. Every syntax error is
// S001 (docs/spec/diagnostics.md) with a byte span, line and byte column;
// Parse reports the first error and returns no file.
//
// Prior art, where the package chooses between options:
//   - go/ast, go/parser: types are expressions, every node has Pos and End,
//     comments are kept as CommentGroups beside the tree (File.Comments) with
//     Doc groups on declarations; the composite-literal rule in control
//     clause headers; parameter grouping; nesting is bounded.
//   - cmd/compile/internal/syntax: one package for tokens, scanner and parser;
//     positions are byte offsets resolved through a line table.
//   - The printer lives apart (internal/sketch/format), as go/printer does,
//     so no checker or lowering code depends on layout.
package oldsyntax
