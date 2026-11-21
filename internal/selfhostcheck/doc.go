// Package selfhostcheck holds the properties of the compiler written in
// Ergo (capstone S1, S2): the formatter of compiler/format, linked into the
// Go module as internal/linked/compiler and called through
// internal/hostsyntax, holds its laws in process (TestSelfhostFormatLaws)
// and prints what the seed's Go formatter printed (TestFormatAgreesWithSeed);
// the layout state agrees with a model (TestSelfhostLayoutModel). The
// formatter parses with compiler/syntax and reads its rows; only the source
// and the text cross. No production code imports this package.
package selfhostcheck
