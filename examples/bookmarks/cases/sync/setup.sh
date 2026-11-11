mkdir -p .local/state/bookmarks .config/bookmarks
printf 'https://go.dev\tThe Go site\tgo,lang\nhttps://gleam.run\tGleam\tlang\n' > .local/state/bookmarks/bookmarks.tsv
printf 'remote = "https://sync.example/v1"\ntoken = "hunter2-SECRET-token"\n' > .config/bookmarks/config.toml
