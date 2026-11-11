mkdir -p .local/state/bookmarks .config/bookmarks
printf 'https://go.dev\tThe Go site\tgo,lang\nhttps://gleam.run\tGleam\tlang\nhttps://example.org/notes\t\tnotes\nhttps://elixir-lang.org\tElixir\tlang\n' > .local/state/bookmarks/bookmarks.tsv
printf '# the user file\nlimit = 2\ntag = "lang"\n' > .config/bookmarks/config.toml
