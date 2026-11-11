# Bookmarks

`bookmarks` keeps web addresses with titles and tags, and reads its
configuration with std `config`: typed, layered, with its defaults stated
once, and with one secret that it never prints.

```text
$ bookmarks add https://go.dev --title "The Go site" --tag go
kept The Go site <https://go.dev> [go]
$ bookmarks list --limit 5 --tag go
The Go site <https://go.dev> [go]
$ BOOKMARKS_LIMIT=ten bookmarks list
bookmarks: limit (env BOOKMARKS_LIMIT): want int
$ bookmarks sync
bookmarks: no token: set BOOKMARKS_TOKEN or token in config.toml
```

The packages, Go style, one responsibility each:

- `shelf` is the domain: a `Bookmark`, `Add` with its typed failures
  (`Duplicate | NotAWebAddress | BadTag`), the `List` query, and the text
  the store keeps. Its laws (`StoredReadsBack`, `AddressesAreUnique`,
  `ListKeepsTheQuery`) run with `ergo test shelf`.
- `settings` is the configuration: the `Settings` record, its `Defaults`,
  and `Credentials`, the secret. `config.Load[Settings]` reads it, highest
  layer first: the flags typed on the command line, the `BOOKMARKS_`
  variables, `~/.config/bookmarks/config.toml` and `config.json`, then the
  defaults. A key of the wrong kind fails `WireWrongKind`, whose path names
  the key and the layer, never the value.
- `disk` keeps the shelf in one file under the state directory
  (`~/.local/state/bookmarks/bookmarks.tsv`, XDG), over std `fs`.
- `cmd/bookmarks` parses the command tree with std `flag`, matches the
  `Cmd` union exhaustively, and is the only place that chooses the
  providers: `config.NewHost()`, `fs.NewHost()`, the terminal.

The token is a `config.Redacted[Credentials]`, read by
`config.LoadSecret`, never from a flag (the process list shows flags). The
checker refuses to interpolate it or to encode a value that holds it, a
failure report writes it as `<Redacted>`, and `sync` reads it in one
place, `signature`, through an explicit `Reveal`. With no token and a
terminal, `sync` asks for one with `prompt.Secret`, which does not echo;
with no terminal it fails and says where to set one.

`sync` only signs the shelf (the first twelve hex digits of a SHA-256 of
the token and the shelf's text); it sends nothing.
