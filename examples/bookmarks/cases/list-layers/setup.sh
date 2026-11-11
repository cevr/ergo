mkdir -p .local/state/bookmarks .config/bookmarks
printf 'https://go.dev\tThe Go site\tgo,lang\nhttps://gleam.run\tGleam\tlang\nhttps://example.org/notes\t\tnotes\nhttps://elixir-lang.org\tElixir\tlang\n' > .local/state/bookmarks/bookmarks.tsv
printf 'limit = 1\ntag = "notes"\n' > .config/bookmarks/config.toml
