# Changes since the seed

The seed (`toolchain/seed.lock`) is the last Go-built `ergo`. Since S1c, its `ergo fmt` is the formatter's oracle: `TestFormatAgreesWithSeed` (`internal/selfhostcheck`) formats the same inputs with the seed and with the tree, and fails on a difference. Each row below states one layout or diagnostic change made since the seed. Its first cell is a text of the seed's outcome or of the tree's, so the test allows an input whose outcome holds it. Two cases need no row, because they come from syntax the language gained after the seed:

- the seed refuses a source as a syntax error and the tree formats it;
- the seed's syntax error lies before the tree's.

A seed bump empties the table.

| Text | Change | Since |
| --- | --- | --- |
| `a composite literal mixes keyed and positional elements` | The Ergo parser reports this error at the element that mixes the two kinds. The Go parser reported it only when the literal closed, so it could stop at a later error first. | S2b (`compiler/syntax`) |
| `clear, delete, min, max` | An extern target may name Go's builtin `delete`, so the list in the error names it. | OPT7 (abb541786) |
| `unexpected character '~'` | `~` is a token since type sets (a constraint's `~T` member), so a stray `~` is a parse error at the same place (`expected …, found "~"`) where the seed's lexer refused the character. | CN ROW-PARAM r3 (e0878beb2) |
