# ergo.dev/xml

An XML 1.0 codec for Ergo's derived codecs, as an installable module (not std): everything it uses, any module may use.

- `Encode(c Codec[T], v T) string` writes v as the element `c.Root`. `Text`, its writer, is a sealed operation type with one provider, so the build calls it directly and joins a record's constant tags by the law `RawJoins`; its buffer is borrowed from std `strings` with T's size hint, as std json's is.
- `Decode(c Codec[T], text string) T` checks the whole text first (`Check`), so no value is built from a text that is not well formed, then reads the value from the checked text. It fails `Malformed` (offset, line, column, reason) or with the derived decoder's failures on the value's shape, each with the value's path.
- `NewParser(text, maxDepth)` and `Next` are the pull reader: one token at a time (`StartElement`, `EndElement`, `CharData`, `Comment`, `ProcInst`, `EndDocument`), keeping only the names of the elements open.

The form: a value is the content of an element. A record's fields are child elements named by their keys, a list's values are `<item>` elements, a union value is its variant's record with `<_tag>Name</_tag>` first (an `Option` too), a null is `<null/>`, and a `Map` key that is not an XML name is `<_ key="…">`. A character XML cannot hold is written as U+FFFD. A document type declaration is refused, so no entity can be declared and expanded.

Laws (`laws.ergo`, run by `ergo test .`): `RoundTrips`, `OutputIsWellFormed`, `ControlCharacterIsRejectedWhereItIs` and `TruncationIsRejectedInsideTheText`, over records, unions, options, lists, maps, tuples, every integer width and floats; `Text.RawJoins` is proved.

Install: require it in the program's `go.mod` (`require ergo.dev/xml v…`, or `replace ergo.dev/xml => ../modules/xml` for this folder, as `examples/catalog` does) and `import ergo.dev/xml`. It uses no Go (its two `extern go append` lines bind Go's builtin, which needs no trust), so the program's `ergo.mod` needs no `trust go` line.
