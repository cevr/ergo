printf '%s\n' \
  'name = "edge"' \
  '[listen]' \
  'port = 80' \
  '[[upstream]]' \
  'name = "a"' \
  'adress = "10.0.0.1:9000"' > typo.toml
